package t3relay

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// --- Store tests ---

func TestStore_UpsertListGetByHost(t *testing.T) {
	f, err := os.CreateTemp("", "relay-test-*.db")
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

	env1 := Environment{
		ID: "env1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}
	env2 := Environment{
		ID: "env2", ContainerID: "c2", Name: "otherrepo",
		Hostname: "otherrepo.t3.example.com", IP: "10.0.0.2", Port: 3773,
		Status: "running", FirstSeen: 1001, LastSeen: 1001,
	}

	if err := store.Upsert(env1); err != nil {
		t.Fatalf("Upsert env1: %v", err)
	}
	if err := store.Upsert(env2); err != nil {
		t.Fatalf("Upsert env2: %v", err)
	}

	list := store.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(list))
	}

	got, ok := store.GetByHost("myrepo.t3.example.com")
	if !ok {
		t.Fatal("GetByHost returned not-found for existing env")
	}
	if got.ID != "env1" {
		t.Errorf("expected ID=env1, got %s", got.ID)
	}

	_, ok = store.GetByHost("nonexistent.t3.example.com")
	if ok {
		t.Error("GetByHost should return false for unknown host")
	}
}

func TestStore_MarkStopped(t *testing.T) {
	f, err := os.CreateTemp("", "relay-test-*.db")
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

	env := Environment{
		ID: "env1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := store.MarkStopped("c1"); err != nil {
		t.Fatalf("MarkStopped: %v", err)
	}

	got, ok := store.GetByHost("myrepo.t3.example.com")
	if !ok {
		t.Fatal("env should still exist after MarkStopped")
	}
	if got.Status != "stopped" {
		t.Errorf("expected status=stopped, got %s", got.Status)
	}
}

func TestStore_DeleteByID(t *testing.T) {
	f, err := os.CreateTemp("", "relay-test-*.db")
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

	env := Environment{
		ID: "env1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "stopped", FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	deleted, err := store.DeleteByID("env1")
	if err != nil {
		t.Fatalf("DeleteByID: %v", err)
	}
	if !deleted {
		t.Fatal("expected DeleteByID to report a deleted row")
	}

	if _, ok := store.GetByID("env1"); ok {
		t.Fatal("environment should not exist after DeleteByID")
	}

	deleted, err = store.DeleteByID("env1")
	if err != nil {
		t.Fatalf("second DeleteByID: %v", err)
	}
	if deleted {
		t.Fatal("expected second DeleteByID to report no deleted row")
	}
}

func TestStore_UpsertConflict_UpdatesLastSeen(t *testing.T) {
	f, err := os.CreateTemp("", "relay-test-*.db")
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

	env := Environment{
		ID: "env1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	env.LastSeen = 2000
	env.Status = "unreachable"
	if err := store.Upsert(env); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, ok := store.GetByHost("myrepo.t3.example.com")
	if !ok {
		t.Fatal("env not found after second upsert")
	}
	if got.LastSeen != 2000 {
		t.Errorf("expected LastSeen=2000, got %d", got.LastSeen)
	}
	if got.Status != "unreachable" {
		t.Errorf("expected status=unreachable, got %s", got.Status)
	}
}

// --- Hostname sanitization tests ---

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"/My Repo", "my-repo"},
		{"my-repo", "my-repo"},
		{"/myrepo", "myrepo"},
		{"123abc", "123abc"},
		{"--leading-trailing--", "leading-trailing"},
	}
	for _, tc := range cases {
		got := sanitizeName(tc.input)
		// check that the result only contains allowed chars
		for _, c := range got {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				t.Errorf("sanitizeName(%q) = %q contains invalid char %q", tc.input, got, c)
			}
		}
	}
}

func TestSanitizeName_Specific(t *testing.T) {
	if got := sanitizeName("/myrepo"); got != "myrepo" {
		t.Errorf("expected 'myrepo', got %q", got)
	}
	if got := sanitizeName("My Repo"); got != "my-repo" {
		t.Errorf("expected 'my-repo', got %q", got)
	}
}

// --- Collision policy test ---

func TestCollisionPolicy(t *testing.T) {
	// Two containers with the same sanitized name; second one gets -<id[:6]> suffix.
	// We simulate the hostnameOwner map logic from reconcile.
	hostnameOwner := make(map[string]string)

	container1ID := "abcdef123456"
	container2ID := "deadbeef9999"
	baseName := "myrepo"

	// first container claims the name
	assignedHost1 := baseName
	if ownerID, exists := hostnameOwner[baseName]; exists && ownerID != container1ID {
		assignedHost1 = fmt.Sprintf("%s-%s", baseName, container1ID[:6])
	} else {
		hostnameOwner[baseName] = container1ID
	}

	// second container collides
	assignedHost2 := baseName
	if ownerID, exists := hostnameOwner[baseName]; exists && ownerID != container2ID {
		assignedHost2 = fmt.Sprintf("%s-%s", baseName, container2ID[:6])
	} else {
		hostnameOwner[baseName] = container2ID
	}

	if assignedHost1 != "myrepo" {
		t.Errorf("first container should get bare name, got %q", assignedHost1)
	}
	expected2 := "myrepo-" + container2ID[:6]
	if assignedHost2 != expected2 {
		t.Errorf("second container should get %q, got %q", expected2, assignedHost2)
	}
}

// --- Bearer validation tests ---

func TestValidateBearer(t *testing.T) {
	tokens := []string{"secret1", "secret2"}

	if !validateBearer(tokens, "secret1") {
		t.Error("valid token should pass")
	}
	if !validateBearer(tokens, "secret2") {
		t.Error("second valid token should pass")
	}
	if validateBearer(tokens, "wrong") {
		t.Error("wrong token should fail")
	}
	if validateBearer(tokens, "") {
		t.Error("empty token should fail")
	}
	if validateBearer(nil, "secret1") {
		t.Error("nil tokens should fail")
	}
}

// --- Relay API handler tests ---

// testAPIHandler creates an APIHandler with a seeded in-memory store.
func testAPIHandler(t *testing.T, tokens []string) (*APIHandler, *Store, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "relay-api-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	cleanup := func() { os.Remove(f.Name()) }

	store, err := OpenStore(f.Name())
	if err != nil {
		cleanup()
		t.Fatalf("OpenStore: %v", err)
	}

	app := &RelayApp{
		DomainSuffix: "t3.example.com",
		ProbePort:    3773,
		tokenList:    tokens,
		store:        store,
		sharedSecret: []byte("test-secret"),
	}

	ah := &APIHandler{app: app}
	return ah, store, func() {
		store.Close()
		cleanup()
	}
}

func TestAPIHandler_Health(t *testing.T) {
	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
}

func TestAPIHandler_ListEnvironments_NoAuth(t *testing.T) {
	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/v1/environments", nil)
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIHandler_ListEnvironments_WithAuth(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	// seed an environment
	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status:    "running",
		ProbeJSON: `{"environmentId":"server-env-1","label":"My Repo","platform":{"os":"linux","arch":"x64"},"serverVersion":"0.0.27"}`,
		FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/environments", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	envs, ok := resp["environments"]
	if !ok {
		t.Fatal("response missing 'environments' key")
	}
	arr, ok := envs.([]any)
	if !ok {
		t.Fatalf("environments is not an array, got %T", envs)
	}
	if len(arr) != 1 {
		t.Errorf("expected 1 environment, got %d", len(arr))
	}
	record, ok := arr[0].(map[string]any)
	if !ok {
		t.Fatalf("environment record is not an object, got %T", arr[0])
	}
	if record["environmentId"] != "server-env-1" {
		t.Errorf("expected descriptor environment id, got %v", record["environmentId"])
	}
	if record["label"] != "myrepo" {
		t.Errorf("expected container name label, got %v", record["label"])
	}
}

func TestAPIHandler_StatusUsesDescriptorEnvironmentID(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/t3/environment" {
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
		if r.Header.Get("X-Relay-Secret") != "test-secret" {
			t.Errorf("missing relay secret header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"environmentId":"server-env-1","label":"My Repo","platform":{"os":"linux","arch":"x64"},"serverVersion":"0.0.27","capabilities":{"repositoryIdentity":true}}`))
	}))
	defer probe.Close()

	host, portString, err := net.SplitHostPort(probe.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split probe address: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse probe port: %v", err)
	}

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: host, Port: port,
		Status:    "running",
		ProbeJSON: `{"environmentId":"server-env-1","label":"My Repo","platform":{"os":"linux","arch":"x64"},"serverVersion":"0.0.27"}`,
		FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/server-env-1/status", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["environmentId"] != "server-env-1" {
		t.Errorf("expected descriptor environment id, got %v", resp["environmentId"])
	}
	descriptor, ok := resp["descriptor"].(map[string]any)
	if !ok {
		t.Fatalf("descriptor is not an object, got %T", resp["descriptor"])
	}
	if descriptor["environmentId"] != "server-env-1" {
		t.Errorf("expected matching descriptor id, got %v", descriptor["environmentId"])
	}
	if descriptor["label"] != "myrepo" {
		t.Errorf("expected container name descriptor label, got %v", descriptor["label"])
	}
}

func TestAPIHandler_ConnectUsesDescriptorEnvironmentID(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status:    "running",
		ProbeJSON: `{"environmentId":"server-env-1","label":"My Repo","platform":{"os":"linux","arch":"x64"},"serverVersion":"0.0.27"}`,
		FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/server-env-1/connect", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["environmentId"] != "server-env-1" {
		t.Errorf("expected descriptor environment id, got %v", resp["environmentId"])
	}
}

func TestAPIHandler_DeleteEnvironmentUsesDescriptorEnvironmentID(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status:    "stopped",
		ProbeJSON: `{"environmentId":"server-env-1","label":"My Repo","platform":{"os":"linux","arch":"x64"},"serverVersion":"0.0.27"}`,
		FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/environments/server-env-1", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if _, ok := store.GetByID("devcontainer-1"); ok {
		t.Fatal("environment should not exist after DELETE")
	}
}

func TestAPIHandler_DeleteEnvironment_NotFound(t *testing.T) {
	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodDelete, "/v1/environments/missing", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// noopHandler returns a caddyhttp.Handler that does nothing.
func noopHandler() caddyhttp.Handler {
	return caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return nil
	})
}
