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
	// CORS — mirror the relay API (corsHeaders). Browsers reach
	// <repo>.t3.<domain> directly from the web app origin, so every response
	// needs these headers. Critically, a CORS preflight (OPTIONS) carries no
	// Authorization header, so it must be answered BEFORE the bearer check
	// below — otherwise it 401s with no CORS headers and the browser blocks the
	// real request. Set up-front, these survive the reverse-proxy header copy
	// and the error handler.
	corsHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	// 1. Detect relay-admin traffic. Browser/application traffic uses
	// environment-issued credentials and must pass through transparently. When
	// the caller presents the relay bearer, translate it to the internal
	// X-Relay-Secret trusted by the environment server.
	relayAuthorized := p.app.ValidateBearer(extractBearer(r.Header.Get("Authorization")))

	// 2. Resolve host → environment
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	env, exposure, isExposure := p.app.LookupExposureByHost(host)
	if !isExposure {
		var ok bool
		env, ok = p.app.LookupByHost(host)
		if !ok {
			return writeJSONError(w, http.StatusNotFound, "not_found", "unknown_host")
		}
	}

	// 3. Reverse-proxy to container
	scheme := "http"
	port := env.Port
	if isExposure {
		scheme = exposure.Scheme
		port = exposure.Port
	}
	target := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", env.IP, port),
	}

	secret := p.app.SharedSecret()
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			if relayAuthorized && len(secret) > 0 {
				req.Header.Del("Authorization")
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
		ModifyResponse: func(res *http.Response) error {
			stripCORSHeaders(res.Header)
			return nil
		},
	}

	proxy.ServeHTTP(w, r)
	return nil
}

func stripCORSHeaders(h http.Header) {
	h.Del("Access-Control-Allow-Origin")
	h.Del("Access-Control-Allow-Methods")
	h.Del("Access-Control-Allow-Headers")
	h.Del("Access-Control-Expose-Headers")
	h.Del("Access-Control-Max-Age")
	h.Del("Access-Control-Allow-Credentials")
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
