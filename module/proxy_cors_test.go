package t3relay

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

// A CORS preflight carries no Authorization header, so it must be answered
// before the bearer check — with a 2xx status and CORS headers — or the browser
// blocks the real request.
func TestProxyHandler_CORSPreflightBeforeAuth(t *testing.T) {
	p := &ProxyHandler{app: &RelayApp{
		tokenList:      []string{"tok1"},
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
	}}

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

// Even when proxy routing fails, the response must carry CORS headers so the
// browser can read the error instead of reporting an opaque CORS failure.
func TestProxyHandler_CORSHeadersOnUnknownHost(t *testing.T) {
	p, cleanup := testProxyHandlerWithEmptyStore(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "https://repo.t3.example.com/api/auth/session", nil)
	req.Header.Set("Origin", "https://web.t3.example.com")
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want * on 404", got)
	}
}

func TestProxyHandler_StripsUpstreamCORSHeaders(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "authorization")
		_, _ = w.Write([]byte(`{"environmentId":"env-1"}`))
	}))
	defer target.Close()

	p, cleanup := testProxyHandlerForTarget(t, target)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "https://repo.t3.example.com/.well-known/t3/environment", nil)
	req.Header.Set("Origin", "https://web.t3.example.com")
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	values := rec.Header().Values("Access-Control-Allow-Origin")
	if len(values) != 1 || values[0] != "*" {
		t.Fatalf("Access-Control-Allow-Origin values = %#v, want exactly [*]", values)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, DELETE, OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want relay methods", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "authorization, b3, traceparent, content-type, dpop" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want relay headers", got)
	}
}

func TestProxyHandler_PublicDescriptorPassesThroughWithoutRelaySecret(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/t3/environment" {
			t.Errorf("path = %q, want descriptor", r.URL.Path)
		}
		if got := r.Header.Get("X-Relay-Secret"); got != "" {
			t.Errorf("X-Relay-Secret = %q, want empty", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"environmentId": "env-1",
			"label":         "Repo",
			"platform":      map[string]string{"os": "linux", "arch": "x64"},
			"serverVersion": "0.0.27",
		})
	}))
	defer target.Close()

	host, portString, err := net.SplitHostPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split target address: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse target port: %v", err)
	}

	f, err := os.CreateTemp("", "relay-proxy-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	store, err := OpenStore(f.Name())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if err := store.Upsert(Environment{
		ID: "env-1", ContainerID: "c1", Name: "repo",
		Hostname: "repo.t3.example.com", IP: host, Port: port,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	p := &ProxyHandler{app: &RelayApp{
		tokenList:      []string{"tok1"},
		store:          store,
		sharedSecret:   []byte("test-secret"),
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
	}}

	req := httptest.NewRequest(http.MethodGet, "https://repo.t3.example.com/.well-known/t3/environment", nil)
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestProxyHandler_EnvironmentBearerPassesThroughWithoutRelaySecret(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer environment-token" {
			t.Errorf("Authorization = %q, want environment bearer", got)
		}
		if got := r.Header.Get("X-Relay-Secret"); got != "" {
			t.Errorf("X-Relay-Secret = %q, want empty", got)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	p, cleanup := testProxyHandlerForTarget(t, target)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "https://repo.t3.example.com/api/auth/session", nil)
	req.Header.Set("Authorization", "Bearer environment-token")
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestProxyHandler_RelayBearerInjectsRelaySecret(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want stripped", got)
		}
		if got := r.Header.Get("X-Relay-Secret"); got != "test-secret" {
			t.Errorf("X-Relay-Secret = %q, want test-secret", got)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	p, cleanup := testProxyHandlerForTarget(t, target)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "https://repo.t3.example.com/api/auth/session", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestProxyHandler_RegisteredExposureRoutesToExposurePort(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "repo.t3.example.com" {
			t.Errorf("upstream host = %q, want target host", r.Host)
		}
		_, _ = w.Write([]byte(`{"preview":true}`))
	}))
	defer target.Close()

	host, portString, err := net.SplitHostPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split target address: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse target port: %v", err)
	}

	f, err := os.CreateTemp("", "relay-proxy-exposure-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	store, err := OpenStore(f.Name())
	if err != nil {
		_ = os.Remove(f.Name())
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		store.Close()
		_ = os.Remove(f.Name())
	}()

	if err := store.Upsert(Environment{
		ID: "env-1", ContainerID: "c1", Name: "repo",
		Hostname: "repo.t3.example.com", IP: host, Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.UpsertExposure(Exposure{
		EnvironmentID: "env-1",
		Name:          "vite",
		HostLabel:     "repo--vite",
		Scheme:        "http",
		Port:          port,
		CreatedAt:     time.Now().Unix(),
		LastSeen:      time.Now().Unix(),
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("UpsertExposure: %v", err)
	}

	p := &ProxyHandler{app: &RelayApp{
		tokenList:      []string{"tok1"},
		store:          store,
		sharedSecret:   []byte("test-secret"),
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
	}}

	req := httptest.NewRequest(http.MethodGet, "https://repo--vite.t3.example.com/", nil)
	rec := httptest.NewRecorder()

	if err := p.ServeHTTP(rec, req, nil); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func testProxyHandlerForTarget(t *testing.T, target *httptest.Server) (*ProxyHandler, func()) {
	t.Helper()

	host, portString, err := net.SplitHostPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split target address: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse target port: %v", err)
	}

	f, err := os.CreateTemp("", "relay-proxy-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	store, err := OpenStore(f.Name())
	if err != nil {
		_ = os.Remove(f.Name())
		t.Fatalf("OpenStore: %v", err)
	}

	if err := store.Upsert(Environment{
		ID: "env-1", ContainerID: "c1", Name: "repo",
		Hostname: "repo.t3.example.com", IP: host, Port: port,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}); err != nil {
		store.Close()
		_ = os.Remove(f.Name())
		t.Fatalf("Upsert: %v", err)
	}

	p := &ProxyHandler{app: &RelayApp{
		tokenList:      []string{"tok1"},
		store:          store,
		sharedSecret:   []byte("test-secret"),
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
	}}

	return p, func() {
		store.Close()
		_ = os.Remove(f.Name())
	}
}

func testProxyHandlerWithEmptyStore(t *testing.T) (*ProxyHandler, func()) {
	t.Helper()

	f, err := os.CreateTemp("", "relay-proxy-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	store, err := OpenStore(f.Name())
	if err != nil {
		_ = os.Remove(f.Name())
		t.Fatalf("OpenStore: %v", err)
	}

	p := &ProxyHandler{app: &RelayApp{
		tokenList:      []string{"tok1"},
		store:          store,
		sharedSecret:   []byte("test-secret"),
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
	}}

	return p, func() {
		store.Close()
		_ = os.Remove(f.Name())
	}
}
