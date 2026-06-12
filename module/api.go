package t3relay

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	caddy.RegisterModule(&APIHandler{})
	httpcaddyfile.RegisterHandlerDirective("t3code_relay_api", func(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
		ah := new(APIHandler)
		return ah, nil
	})
}

// APIHandler implements the relay control-plane API at relay.t3.<domain>.
//
// Caddy module: http.handlers.t3code_relay_api
type APIHandler struct {
	app *RelayApp
}

// CaddyModule implements caddy.Module.
func (*APIHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.t3code_relay_api",
		New: func() caddy.Module { return new(APIHandler) },
	}
}

// Provision implements caddy.Provisioner.
func (a *APIHandler) Provision(ctx caddy.Context) error {
	appIface, err := ctx.App("t3code_relay")
	if err != nil {
		return fmt.Errorf("t3code_relay_api: cannot get relay app: %w", err)
	}
	var ok bool
	a.app, ok = appIface.(*RelayApp)
	if !ok {
		return fmt.Errorf("t3code_relay_api: unexpected app type %T", appIface)
	}
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler (no-op).
func (a *APIHandler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error { return nil }

// corsHeaders adds CORS headers to the response.
func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "authorization, b3, traceparent, content-type, dpop")
	w.Header().Set("Access-Control-Expose-Headers", "traceparent, www-authenticate")
	w.Header().Set("Access-Control-Max-Age", "86400")
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (a *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	corsHeaders(w)

	// OPTIONS preflight
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	path := r.URL.Path

	// GET /health — no auth required
	if r.Method == http.MethodGet && path == "/health" {
		return writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "relay"})
	}

	// All other routes require Bearer auth
	token := extractBearer(r.Header.Get("Authorization"))
	if !a.app.ValidateBearer(token) {
		return writeJSON(w, http.StatusUnauthorized, map[string]string{
			"_tag":   "RelayAuthInvalidError",
			"code":   "auth_invalid",
			"reason": "invalid_bearer",
		})
	}

	switch {
	case r.Method == http.MethodGet && path == "/v1/environments":
		return a.handleListEnvironments(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/status"):
		return a.handleEnvironmentStatus(w, r, envIDFromPath(path, "/status"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/connect"):
		return a.handleEnvironmentConnect(w, r, envIDFromPath(path, "/connect"))
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/v1/environments/"):
		return a.handleDeleteEnvironment(w, r, envIDFromEnvironmentPath(path))
	default:
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag":   "NotFoundError",
			"code":   "not_found",
			"reason": "unknown_route",
		})
	}
}

// envIDFromPath extracts the environment ID from a path like /v1/environments/:id/status.
func envIDFromPath(path, suffix string) string {
	path = strings.TrimSuffix(path, suffix)
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// envIDFromEnvironmentPath extracts the environment ID from a path like
// /v1/environments/:id, returning empty for nested paths.
func envIDFromEnvironmentPath(path string) string {
	id := strings.TrimPrefix(path, "/v1/environments/")
	if id == path || id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

// environmentEndpoint builds the endpoint object for an environment.
func environmentEndpoint(env Environment) map[string]string {
	return map[string]string{
		"httpBaseUrl":  "https://" + env.Hostname,
		"wsBaseUrl":    "wss://" + env.Hostname,
		"providerKind": "t3_relay",
	}
}

func environmentDescriptorID(env Environment) string {
	if env.ProbeJSON == "" {
		return ""
	}
	var pr probeResult
	if err := json.Unmarshal([]byte(env.ProbeJSON), &pr); err != nil {
		return ""
	}
	return strings.TrimSpace(pr.EnvironmentID)
}

// relayEnvironmentID returns the id the relay exposes to clients. The store
// keeps the devcontainer id as its stable row key, but clients validate status
// and connect responses against the T3 server's descriptor id.
func relayEnvironmentID(env Environment) string {
	if id := environmentDescriptorID(env); id != "" {
		return id
	}
	return env.ID
}

func relayEnvironmentLabel(env Environment) string {
	if label := strings.TrimSpace(env.Name); label != "" {
		return label
	}
	return env.ID
}

func (a *APIHandler) lookupEnvironmentByRelayID(id string) (Environment, bool) {
	if env, ok := a.app.GetStore().GetByID(id); ok {
		return env, true
	}
	for _, env := range a.app.ListEnvironments() {
		if relayEnvironmentID(env) == id {
			return env, true
		}
	}
	return Environment{}, false
}

func (a *APIHandler) handleListEnvironments(w http.ResponseWriter, r *http.Request) error {
	envs := a.app.ListEnvironments()
	// Shape MUST match contracts RelayClientEnvironmentRecord exactly
	// (packages/contracts/src/relay.ts): { environmentId, label (non-empty),
	// endpoint (RelayManagedEndpoint), linkedAt (non-empty) }. The client
	// decodes this with Effect Schema, so a missing required field — or an
	// empty label/linkedAt — fails the decode. Per-environment status/platform
	// are NOT part of this record; the client reads those from the /status
	// endpoint.
	type envRecord struct {
		EnvironmentID string            `json:"environmentId"`
		Label         string            `json:"label"`
		Endpoint      map[string]string `json:"endpoint"`
		LinkedAt      string            `json:"linkedAt"`
	}
	records := make([]envRecord, 0, len(envs))
	for _, e := range envs {
		// linkedAt = first time we discovered the environment (RFC3339, non-empty).
		linkedAt := time.Unix(e.FirstSeen, 0).UTC().Format(time.RFC3339)
		records = append(records, envRecord{
			EnvironmentID: relayEnvironmentID(e),
			Label:         relayEnvironmentLabel(e),
			Endpoint:      environmentEndpoint(e),
			LinkedAt:      linkedAt,
		})
	}
	return writeJSON(w, http.StatusOK, map[string]any{"environments": records})
}

func (a *APIHandler) handleEnvironmentStatus(w http.ResponseWriter, r *http.Request, envID string) error {
	env, ok := a.lookupEnvironmentByRelayID(envID)
	if !ok {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}

	// live probe
	rawBody, pr, probeErr := probeEnvironment(env.IP, env.Port, a.app.SharedSecret(), 5*time.Second)
	status := "online"
	if probeErr != nil {
		status = "offline"
	}
	// update store
	newStatus := "running"
	if probeErr != nil {
		newStatus = "unreachable"
	}
	env.Status = newStatus
	if rawBody != "" {
		env.ProbeJSON = rawBody
	}
	env.LastSeen = time.Now().Unix()
	_ = a.app.GetStore().Upsert(env)

	// descriptor must match contracts ExecutionEnvironmentDescriptor exactly
	// (environmentId, label, platform, serverVersion, capabilities). The
	// server's probe response already IS that shape, so pass the raw JSON
	// through unmodified rather than a lossy typed re-marshal (probeResult
	// omits capabilities). descriptor is optional, so leave it unset if the
	// probe body didn't parse.
	var descriptor any
	if rawBody != "" {
		var d map[string]any
		if err := json.Unmarshal([]byte(rawBody), &d); err == nil {
			d["label"] = relayEnvironmentLabel(env)
			descriptor = d
		}
	}
	_ = pr // parsed form retained by probeEnvironment; raw body used for the wire

	responseEnvironmentID := relayEnvironmentID(env)
	resp := map[string]any{
		"environmentId": responseEnvironmentID,
		"endpoint":      environmentEndpoint(env),
		"status":        status,
		"checkedAt":     time.Now().UTC().Format(time.RFC3339),
	}
	if descriptor != nil {
		resp["descriptor"] = descriptor
	}
	return writeJSON(w, http.StatusOK, resp)
}

func (a *APIHandler) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request, envID string) error {
	if envID == "" {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}

	env, ok := a.lookupEnvironmentByRelayID(envID)
	if !ok {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}

	deleted, err := a.app.GetStore().DeleteByID(env.ID)
	if err != nil {
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "StoreError", "code": "store_error", "reason": "delete_failed",
		})
	}
	if !deleted {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}

const credChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randomCredential(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = credChars[rand.Intn(len(credChars))]
	}
	return string(b)
}

func (a *APIHandler) handleEnvironmentConnect(w http.ResponseWriter, r *http.Request, envID string) error {
	// Read body (may contain clientProofKeyThumbprint etc., but we ignore it)
	if r.Body != nil {
		_, _ = io.ReadAll(io.LimitReader(r.Body, 4096))
	}

	env, ok := a.lookupEnvironmentByRelayID(envID)
	if !ok {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}

	credential := randomCredential(12)
	expiresAt := time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339)

	return writeJSON(w, http.StatusOK, map[string]any{
		"environmentId": relayEnvironmentID(env),
		"endpoint":      environmentEndpoint(env),
		"credential":    credential,
		"expiresAt":     expiresAt,
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

var (
	_ caddy.Module                = (*APIHandler)(nil)
	_ caddy.Provisioner           = (*APIHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*APIHandler)(nil)
)
