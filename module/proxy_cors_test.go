package t3relay

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A CORS preflight carries no Authorization header, so it must be answered
// before the bearer check — with a 2xx status and CORS headers — or the browser
// blocks the real request.
func TestProxyHandler_CORSPreflightBeforeAuth(t *testing.T) {
	p := &ProxyHandler{app: &RelayApp{tokenList: []string{"tok1"}}}

	req := httptest.NewRequest(http.MethodOptions, "https://repo.t3.example.com/.well-known/t3/environment", nil)
	req.Header.Set("Origin", "https://web.t3.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("missing Access-Control-Allow-Methods on preflight")
	}
}

// Even when auth fails, the response must carry CORS headers so the browser can
// read the 401 instead of reporting an opaque CORS error.
func TestProxyHandler_CORSHeadersOnAuthFailure(t *testing.T) {
	p := &ProxyHandler{app: &RelayApp{tokenList: []string{"tok1"}}}

	req := httptest.NewRequest(http.MethodGet, "https://repo.t3.example.com/.well-known/t3/environment", nil)
	req.Header.Set("Origin", "https://web.t3.example.com")
	// no Authorization header -> bearer check fails
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want * on 401", got)
	}
}
