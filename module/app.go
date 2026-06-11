package t3relay

import (
	"context"
	"fmt"
	"os"
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
	// DomainSuffix is the base domain suffix, e.g. "t3.example.com".
	DomainSuffix string `json:"domain_suffix,omitempty"`
	// RelayHost is the hostname serving the relay API, e.g. "relay.t3.example.com".
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

	// runtime state
	store        *Store
	docker       *dockerclient.Client
	sharedSecret []byte
	tokenList    []string
	logger       *zap.Logger
	cancelDisc   context.CancelFunc
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

	// start discovery
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelDisc = cancel
	go runDiscovery(ctx, a)

	a.logger.Info("t3code_relay started",
		zap.String("domain_suffix", a.DomainSuffix),
		zap.String("relay_host", a.RelayHost),
		zap.Int("probe_port", a.ProbePort),
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
	return nil
}

// --- Accessor methods used by the HTTP handlers ---

// LookupByHost returns an environment by its full hostname.
func (a *RelayApp) LookupByHost(hostname string) (Environment, bool) {
	return a.store.GetByHost(hostname)
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
