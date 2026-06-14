package t3relay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	dockerclient "github.com/moby/moby/client"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(&RelayApp{})
	httpcaddyfile.RegisterGlobalOption("t3code_relay", parseGlobalOption)
}

// RelayApp is the Caddy app that manages Docker discovery, SQLite store,
// and exposes accessor methods to the two HTTP handler modules.
type RelayApp struct {
	// DomainSuffix is the legacy/default base domain suffix, e.g. "t3.example.com".
	// When wildcard Caddy labels are present on the relay container, those labels
	// become the source of truth for served zones and this value is used only as a
	// fallback/default.
	DomainSuffix string `json:"domain_suffix,omitempty"`
	// RelayHost is the legacy/default relay API hostname, e.g. "relay.t3.example.com".
	RelayHost string `json:"relay_host,omitempty"`
	// DBPath is the file path to the SQLite database.
	DBPath string `json:"db_path,omitempty"`
	// DockerHost is the Docker socket or TCP address.
	DockerHost string `json:"docker_host,omitempty"`
	// ProbePort is the port to probe on each devcontainer (default 3773).
	ProbePort int `json:"probe_port,omitempty"`
	// Tokens is the comma-separated list of bearer tokens.
	Tokens string `json:"tokens,omitempty"`
	// SharedSecretFile is the path to the file containing the shared secret.
	SharedSecretFile string `json:"shared_secret_file,omitempty"`
	// TailscaleHostname is the hostname presented by the embedded tsnet node.
	TailscaleHostname string `json:"tailscale_hostname,omitempty"`
	// TailscaleAuthKey is the auth key used to join the tailnet.
	TailscaleAuthKey string `json:"tailscale_auth_key,omitempty"`
	// TailscaleStateDir is the directory used to persist tsnet state.
	TailscaleStateDir string `json:"tailscale_state_dir,omitempty"`
	// SSHHostKeyFile is the persisted private host key for the tailnet SSH gateway.
	SSHHostKeyFile string `json:"ssh_host_key_file,omitempty"`
	// SSHAllowedUser is the only username accepted by the tailnet SSH gateway.
	SSHAllowedUser string `json:"ssh_allowed_user,omitempty"`
	// SSHBackendPort is the port the devcontainer sshd feature listens on.
	SSHBackendPort int `json:"ssh_backend_port,omitempty"`
	// MountsRoot is the directory whose contents are exposed by the relay mount browser.
	MountsRoot string `json:"mounts_root,omitempty"`

	// runtime state
	store          *Store
	docker         *dockerclient.Client
	sharedSecret   []byte
	tokenList      []string
	supportedZones []string
	primaryZone    string
	logger         *zap.Logger
	cancelDisc     context.CancelFunc
	tailnet        *tailnetRuntime
}

// CaddyModule implements caddy.Module.
func (*RelayApp) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "t3code_relay",
		New: func() caddy.Module { return new(RelayApp) },
	}
}

// Provision implements caddy.Provisioner.
func (a *RelayApp) Provision(ctx caddy.Context) error {
	a.logger = ctx.Logger()

	if a.ProbePort == 0 {
		a.ProbePort = 3773
	}

	// parse tokens
	for _, t := range strings.Split(a.Tokens, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			a.tokenList = append(a.tokenList, t)
		}
	}

	// read shared secret file
	if a.SharedSecretFile != "" {
		data, err := os.ReadFile(a.SharedSecretFile)
		if err != nil {
			return fmt.Errorf("t3code_relay: reading shared_secret_file %q: %w", a.SharedSecretFile, err)
		}
		a.sharedSecret = []byte(strings.TrimSpace(string(data)))
	}

	if a.TailscaleHostname == "" {
		a.TailscaleHostname = "t3code-relay"
	}
	if a.TailscaleStateDir == "" {
		baseDir := filepath.Dir(a.DBPath)
		if baseDir == "." || baseDir == "" {
			baseDir = "/var/lib/t3code-relay"
		}
		a.TailscaleStateDir = filepath.Join(baseDir, "tsnet")
	}
	if a.SSHHostKeyFile == "" {
		baseDir := filepath.Dir(a.DBPath)
		if baseDir == "." || baseDir == "" {
			baseDir = "/var/lib/t3code-relay"
		}
		a.SSHHostKeyFile = filepath.Join(baseDir, "ssh_host_ed25519_key")
	}
	if a.SSHAllowedUser == "" {
		a.SSHAllowedUser = "vscode"
	}
	if a.SSHBackendPort == 0 {
		a.SSHBackendPort = 2222
	}
	if a.MountsRoot == "" {
		a.MountsRoot = "/mnt/t3relay"
	}

	return nil
}

// Start implements caddy.App.
func (a *RelayApp) Start() error {
	var err error

	// open store
	a.store, err = OpenStore(a.DBPath)
	if err != nil {
		return fmt.Errorf("t3code_relay: open store: %w", err)
	}

	// construct Docker client
	opts := []dockerclient.Opt{dockerclient.WithAPIVersionNegotiation()}
	if a.DockerHost != "" {
		opts = append(opts, dockerclient.WithHost(a.DockerHost))
	}
	a.docker, err = dockerclient.New(opts...)
	if err != nil {
		return fmt.Errorf("t3code_relay: create docker client: %w", err)
	}

	if err := a.refreshServedZones(context.Background()); err != nil {
		return fmt.Errorf("t3code_relay: discover served zones: %w", err)
	}

	a.tailnet, err = startTailnet(context.Background(), a)
	if err != nil {
		return fmt.Errorf("t3code_relay: start tailnet: %w", err)
	}

	// start discovery
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelDisc = cancel
	go runDiscovery(ctx, a)

	a.logger.Info("t3code_relay started",
		zap.Strings("supported_zones", a.supportedZones),
		zap.String("primary_zone", a.primaryZone),
		zap.Int("probe_port", a.ProbePort),
		zap.String("tailscale_hostname", a.TailscaleHostname),
	)
	return nil
}

// Stop implements caddy.App.
func (a *RelayApp) Stop() error {
	if a.cancelDisc != nil {
		a.cancelDisc()
	}
	if a.docker != nil {
		a.docker.Close()
	}
	if a.store != nil {
		a.store.Close()
	}
	if a.tailnet != nil {
		_ = a.tailnet.Close()
	}
	return nil
}

// --- Accessor methods used by the HTTP handlers ---

// LookupByHost returns an environment by parsing a served hostname and
// resolving its left-hand environment label.
func (a *RelayApp) LookupByHost(hostname string) (Environment, bool) {
	name, _, ok := a.ParseServedHost(hostname)
	if !ok {
		return Environment{}, false
	}
	return a.store.GetByName(name)
}

func (a *RelayApp) LookupExposureByHost(hostname string) (Environment, Exposure, bool) {
	hostLabel, _, ok := a.ParseServedHost(hostname)
	if !ok {
		return Environment{}, Exposure{}, false
	}
	if _, ok := a.store.GetByName(hostLabel); ok {
		return Environment{}, Exposure{}, false
	}
	delimiter := strings.LastIndex(hostLabel, "--")
	if delimiter <= 0 || delimiter+2 >= len(hostLabel) {
		return Environment{}, Exposure{}, false
	}
	exposure, ok := a.store.GetExposureByHostLabel(hostLabel)
	if !ok {
		return Environment{}, Exposure{}, false
	}
	env, ok := a.store.GetByID(exposure.EnvironmentID)
	if !ok || env.Status == "stopped" {
		return Environment{}, Exposure{}, false
	}
	return env, exposure, true
}

func (a *RelayApp) ValidateSharedSecret(candidate string) bool {
	return validateSharedSecret(a.sharedSecret, candidate)
}

// ListEnvironments returns all environments from the store.
func (a *RelayApp) ListEnvironments() []Environment {
	return a.store.List()
}

// ValidateBearer returns true if the token matches one of the configured tokens.
// Uses constant-time comparison to prevent timing attacks.
func (a *RelayApp) ValidateBearer(token string) bool {
	return validateBearer(a.tokenList, token)
}

// SharedSecret returns the loaded shared secret bytes.
func (a *RelayApp) SharedSecret() []byte {
	return a.sharedSecret
}

// ProbePortNum returns the configured probe port.
func (a *RelayApp) ProbePortNum() int {
	return a.ProbePort
}

// GetStore returns the underlying store (used by API handler for direct lookups).
func (a *RelayApp) GetStore() *Store {
	return a.store
}

// GetDocker returns the Docker client (used by discovery and API handler for live probes).
func (a *RelayApp) GetDocker() *dockerclient.Client {
	return a.docker
}

// ParseServedHost parses a public host of the form <name>.<served-zone>.
func (a *RelayApp) ParseServedHost(host string) (name, zone string, ok bool) {
	return parseServedHost(host, a.supportedZones)
}

// SupportedZones returns the served wildcard zones discovered from Caddy labels.
func (a *RelayApp) SupportedZones() []string {
	return append([]string(nil), a.supportedZones...)
}

// PublishedHostname returns the canonical published hostname for an environment.
func (a *RelayApp) PublishedHostname(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	if a.primaryZone == "" {
		return name
	}
	return name + "." + a.primaryZone
}

func (a *RelayApp) refreshServedZones(ctx context.Context) error {
	zones, err := discoverServedZones(ctx, a.docker, a.logger)
	if err != nil {
		return err
	}
	a.supportedZones = zones
	a.primaryZone = primaryZone(zones, a.DomainSuffix, a.RelayHost)
	if len(a.supportedZones) == 0 && a.primaryZone != "" {
		a.supportedZones = []string{a.primaryZone}
	}
	return nil
}

// --- Caddyfile parsing ---

// UnmarshalCaddyfile implements caddyfile.Unmarshaler (not needed for global option,
// kept for completeness/testing).
func (a *RelayApp) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	// consume the opening token "t3code_relay" if present
	d.Next()
	for d.NextBlock(0) {
		switch d.Val() {
		case "domain_suffix":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.DomainSuffix = d.Val()
		case "relay_host":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.RelayHost = d.Val()
		case "db_path":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.DBPath = d.Val()
		case "docker_host":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.DockerHost = d.Val()
		case "probe_port":
			if !d.NextArg() {
				return d.ArgErr()
			}
			p, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("probe_port must be an integer: %v", err)
			}
			a.ProbePort = p
		case "tokens":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.Tokens = d.Val()
		case "shared_secret_file":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.SharedSecretFile = d.Val()
		case "tailscale_hostname":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.TailscaleHostname = d.Val()
		case "tailscale_auth_key":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.TailscaleAuthKey = d.Val()
		case "tailscale_state_dir":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.TailscaleStateDir = d.Val()
		case "ssh_host_key_file":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.SSHHostKeyFile = d.Val()
		case "ssh_allowed_user":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.SSHAllowedUser = d.Val()
		case "ssh_backend_port":
			if !d.NextArg() {
				return d.ArgErr()
			}
			p, err := strconv.Atoi(d.Val())
			if err != nil {
				return d.Errf("ssh_backend_port must be an integer: %v", err)
			}
			a.SSHBackendPort = p
		case "mounts_root":
			if !d.NextArg() {
				return d.ArgErr()
			}
			a.MountsRoot = d.Val()
		default:
			return d.Errf("unknown t3code_relay option: %s", d.Val())
		}
	}
	return nil
}

// parseGlobalOption is the RegisterGlobalOption parse function.
func parseGlobalOption(d *caddyfile.Dispenser, _ any) (any, error) {
	app := new(RelayApp)
	if err := app.UnmarshalCaddyfile(d); err != nil {
		return nil, err
	}
	return httpcaddyfile.App{
		Name:  "t3code_relay",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}

// Interface compliance compile-time checks.
var (
	_ caddy.Module      = (*RelayApp)(nil)
	_ caddy.App         = (*RelayApp)(nil)
	_ caddy.Provisioner = (*RelayApp)(nil)
)
