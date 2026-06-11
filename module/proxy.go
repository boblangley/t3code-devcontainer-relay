package t3relay

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	caddy.RegisterModule(&ProxyHandler{})
	httpcaddyfile.RegisterHandlerDirective("t3code_relay_proxy", func(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
		ph := new(ProxyHandler)
		return ph, nil
	})
}

// ProxyHandler is the HTTP middleware handler for per-environment proxying.
// It resolves Host → container IP via the app's store and reverse-proxies.
//
// Caddy module: http.handlers.t3code_relay_proxy
type ProxyHandler struct {
	app *RelayApp
}

// CaddyModule implements caddy.Module.
func (*ProxyHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.t3code_relay_proxy",
		New: func() caddy.Module { return new(ProxyHandler) },
	}
}

// Provision implements caddy.Provisioner.
func (p *ProxyHandler) Provision(ctx caddy.Context) error {
	appIface, err := ctx.App("t3code_relay")
	if err != nil {
		return fmt.Errorf("t3code_relay_proxy: cannot get relay app: %w", err)
	}
	var ok bool
	p.app, ok = appIface.(*RelayApp)
	if !ok {
		return fmt.Errorf("t3code_relay_proxy: unexpected app type %T", appIface)
	}
	return nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler (no-op, no config needed).
func (p *ProxyHandler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error { return nil }

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// 1. Validate bearer token
	token := extractBearer(r.Header.Get("Authorization"))
	if !p.app.ValidateBearer(token) {
		return writeJSONError(w, http.StatusUnauthorized, "auth_invalid", "invalid_bearer")
	}

	// 2. Resolve host → environment
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	env, ok := p.app.LookupByHost(host)
	if !ok {
		return writeJSONError(w, http.StatusNotFound, "not_found", "unknown_host")
	}

	// 3. Reverse-proxy to container
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", env.IP, env.Port),
	}

	secret := p.app.SharedSecret()
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// remove client auth, inject relay secret
			req.Header.Del("Authorization")
			if len(secret) > 0 {
				req.Header.Set("X-Relay-Secret", string(secret))
			}
		},
		FlushInterval: -1, // streaming / WebSocket support
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			if strings.Contains(err.Error(), "timeout") {
				writeJSONError(rw, http.StatusGatewayTimeout, "gateway_timeout", err.Error()) //nolint:errcheck
			} else {
				writeJSONError(rw, http.StatusBadGateway, "bad_gateway", err.Error()) //nolint:errcheck
			}
		},
	}

	proxy.ServeHTTP(w, r)
	return nil
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, status int, tag, reason string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"_tag":   tag,
		"code":   tag,
		"reason": reason,
	})
	return nil
}

var (
	_ caddy.Module                = (*ProxyHandler)(nil)
	_ caddy.Provisioner           = (*ProxyHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*ProxyHandler)(nil)
)
