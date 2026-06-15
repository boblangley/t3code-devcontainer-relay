package t3relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	if r.Method == http.MethodGet && (path == "/" || path == "/mounts") {
		return a.serveMountsUI(w, r)
	}

	token := extractBearer(r.Header.Get("Authorization"))
	bearerAuthorized := a.app.ValidateBearer(token)
	sharedSecretAuthorized := a.app.ValidateSharedSecret(r.Header.Get("X-Relay-Secret"))
	exposureRoute := strings.HasSuffix(path, "/exposures") || strings.Contains(path, "/exposures/")

	if !bearerAuthorized && !(exposureRoute && sharedSecretAuthorized) {
		return writeJSON(w, http.StatusUnauthorized, map[string]string{
			"_tag":   "RelayAuthInvalidError",
			"code":   "auth_invalid",
			"reason": "invalid_credentials",
		})
	}

	switch {
	case r.Method == http.MethodGet && path == "/v1/mounts/tree":
		return a.handleMountsTree(w, r)
	case r.Method == http.MethodGet && path == "/v1/mounts/children":
		return a.handleMountsChildren(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/mounts/file/"):
		return a.handleMountFile(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/exposures"):
		return a.handleListExposures(w, r, envIDFromPath(path, "/exposures"))
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/exposures"):
		return a.handleUpsertExposure(w, r, envIDFromPath(path, "/exposures"))
	case r.Method == http.MethodDelete && strings.Contains(path, "/exposures/"):
		envID, exposureName := envIDAndExposureNameFromPath(path)
		return a.handleDeleteExposure(w, r, envID, exposureName)
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

func envIDAndExposureNameFromPath(path string) (string, string) {
	const marker = "/exposures/"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return "", ""
	}
	envPath := strings.TrimPrefix(path[:idx], "/v1/environments/")
	name := path[idx+len(marker):]
	if envPath == path[:idx] || envPath == "" || name == "" || strings.Contains(name, "/") {
		return "", ""
	}
	envPath, err := url.PathUnescape(envPath)
	if err != nil {
		return "", ""
	}
	name, err = url.PathUnescape(name)
	if err != nil {
		return "", ""
	}
	if strings.Contains(envPath, "/") || strings.Contains(name, "/") {
		return "", ""
	}
	return envPath, name
}

// envIDFromPath extracts the environment ID from a path like /v1/environments/:id/status.
func envIDFromPath(path, suffix string) string {
	path = strings.TrimSuffix(path, suffix)
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	id, err := url.PathUnescape(parts[len(parts)-1])
	if err != nil {
		return ""
	}
	if strings.Contains(id, "/") {
		return ""
	}
	return id
}

// envIDFromEnvironmentPath extracts the environment ID from a path like
// /v1/environments/:id, returning empty for nested paths.
func envIDFromEnvironmentPath(path string) string {
	id := strings.TrimPrefix(path, "/v1/environments/")
	if id == path || id == "" || strings.Contains(id, "/") {
		return ""
	}
	id, err := url.PathUnescape(id)
	if err != nil {
		return ""
	}
	if strings.Contains(id, "/") {
		return ""
	}
	return id
}

// environmentEndpoint builds the endpoint object for an environment.
func environmentEndpoint(app *RelayApp, env Environment) map[string]string {
	host := app.PublishedHostname(env.Name)
	if host == "" {
		host = env.Hostname
	}
	return map[string]string{
		"httpBaseUrl":  "https://" + host,
		"wsBaseUrl":    "wss://" + host,
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
			Endpoint:      environmentEndpoint(a.app, e),
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
		"endpoint":      environmentEndpoint(a.app, env),
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

type exposureResponse struct {
	EnvironmentID string `json:"environmentId"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	URL           string `json:"url"`
	Scheme        string `json:"scheme"`
	Port          int    `json:"port"`
	ExpiresAt     string `json:"expiresAt"`
}

func exposureRecord(app *RelayApp, env Environment, exposure Exposure) exposureResponse {
	host := app.PublishedHostname(exposure.HostLabel)
	return exposureResponse{
		EnvironmentID: relayEnvironmentID(env),
		Name:          exposure.Name,
		Host:          host,
		URL:           "https://" + host,
		Scheme:        exposure.Scheme,
		Port:          exposure.Port,
		ExpiresAt:     time.Unix(exposure.ExpiresAt, 0).UTC().Format(time.RFC3339),
	}
}

func normalizeExposureName(name string) string {
	name = sanitizeName(name)
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return strings.Trim(name, "-")
}

func defaultExposureName(port int) string {
	return strconv.Itoa(port)
}

func validateExposurePort(port int) bool {
	return port > 0 && port <= 65535
}

func (a *APIHandler) handleListExposures(w http.ResponseWriter, r *http.Request, envID string) error {
	env, ok := a.lookupEnvironmentByRelayID(envID)
	if !ok {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}
	_ = a.app.GetStore().DeleteExpiredExposures()
	exposures := a.app.GetStore().ListExposures(env.ID)
	records := make([]exposureResponse, 0, len(exposures))
	for _, exposure := range exposures {
		records = append(records, exposureRecord(a.app, env, exposure))
	}
	return writeJSON(w, http.StatusOK, map[string]any{"exposures": records})
}

func (a *APIHandler) handleUpsertExposure(w http.ResponseWriter, r *http.Request, envID string) error {
	env, ok := a.lookupEnvironmentByRelayID(envID)
	if !ok {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}

	var body struct {
		Name       string `json:"name"`
		Scheme     string `json:"scheme"`
		Port       int    `json:"port"`
		TTLSeconds int    `json:"ttlSeconds"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&body); err != nil {
			return writeJSON(w, http.StatusBadRequest, map[string]string{
				"_tag": "BadRequestError", "code": "bad_request", "reason": "invalid_json",
			})
		}
	}
	if !validateExposurePort(body.Port) {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "invalid_port",
		})
	}

	scheme := strings.ToLower(strings.TrimSpace(body.Scheme))
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "unsupported_scheme",
		})
	}

	name := normalizeExposureName(body.Name)
	if name == "" {
		name = defaultExposureName(body.Port)
	}
	if name == env.Name {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "exposure_name_conflicts_with_environment",
		})
	}

	ttl := body.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl > 86400 {
		ttl = 86400
	}

	now := time.Now().Unix()
	exposure := Exposure{
		EnvironmentID: env.ID,
		Name:          name,
		HostLabel:     env.Name + "--" + name,
		Scheme:        scheme,
		Port:          body.Port,
		CreatedAt:     now,
		LastSeen:      now,
		ExpiresAt:     now + int64(ttl),
	}
	if err := a.app.GetStore().UpsertExposure(exposure); err != nil {
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "StoreError", "code": "store_error", "reason": "upsert_exposure_failed",
		})
	}

	return writeJSON(w, http.StatusOK, exposureRecord(a.app, env, exposure))
}

func (a *APIHandler) handleDeleteExposure(w http.ResponseWriter, r *http.Request, envID, exposureName string) error {
	if envID == "" || exposureName == "" {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_exposure",
		})
	}
	env, ok := a.lookupEnvironmentByRelayID(envID)
	if !ok {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_environment",
		})
	}
	name := normalizeExposureName(exposureName)
	deleted, err := a.app.GetStore().DeleteExposure(env.ID, name)
	if err != nil {
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "StoreError", "code": "store_error", "reason": "delete_exposure_failed",
		})
	}
	if !deleted {
		return writeJSON(w, http.StatusNotFound, map[string]string{
			"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_exposure",
		})
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type pairingCredentialResponse struct {
	Credential string `json:"credential"`
	ExpiresAt  string `json:"expiresAt"`
}

func mintEnvironmentPairingCredential(env Environment, sharedSecret []byte) (pairingCredentialResponse, int, error) {
	req, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("http://%s:%d/api/auth/pairing-token", env.IP, env.Port),
		strings.NewReader("{}"),
	)
	if err != nil {
		return pairingCredentialResponse{}, http.StatusBadGateway, err
	}
	req.Header.Set("Content-Type", "application/json")
	if len(sharedSecret) > 0 {
		req.Header.Set("X-Relay-Secret", string(sharedSecret))
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if os.IsTimeout(err) {
			return pairingCredentialResponse{}, http.StatusGatewayTimeout, err
		}
		return pairingCredentialResponse{}, http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return pairingCredentialResponse{}, http.StatusBadGateway, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pairingCredentialResponse{}, http.StatusBadGateway, fmt.Errorf("pairing-token returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var credential pairingCredentialResponse
	if err := json.Unmarshal(body, &credential); err != nil {
		return pairingCredentialResponse{}, http.StatusBadGateway, err
	}
	if strings.TrimSpace(credential.Credential) == "" || strings.TrimSpace(credential.ExpiresAt) == "" {
		return pairingCredentialResponse{}, http.StatusBadGateway, fmt.Errorf("pairing-token response missing credential or expiresAt")
	}
	return credential, http.StatusOK, nil
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

	credential, status, err := mintEnvironmentPairingCredential(env, a.app.SharedSecret())
	if err != nil {
		if status == http.StatusGatewayTimeout {
			return writeJSON(w, status, map[string]string{
				"_tag":    "RelayEnvironmentEndpointTimedOutError",
				"code":    "environment_endpoint_timed_out",
				"traceId": "self-hosted-relay",
			})
		}
		return writeJSON(w, status, map[string]string{
			"_tag":    "RelayEnvironmentEndpointUnavailableError",
			"code":    "environment_endpoint_unavailable",
			"reason":  "endpoint_request_failed",
			"traceId": "self-hosted-relay",
		})
	}

	return writeJSON(w, http.StatusOK, map[string]any{
		"environmentId": relayEnvironmentID(env),
		"endpoint":      environmentEndpoint(a.app, env),
		"credential":    credential.Credential,
		"expiresAt":     credential.ExpiresAt,
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
