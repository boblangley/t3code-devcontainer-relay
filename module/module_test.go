package t3relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
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

func TestStore_ExposureLifecycle(t *testing.T) {
	f, err := os.CreateTemp("", "relay-exposure-test-*.db")
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
		t.Fatalf("Upsert env: %v", err)
	}

	exposure := Exposure{
		EnvironmentID: "env1",
		Name:          "vite",
		HostLabel:     "myrepo--vite",
		Scheme:        "http",
		Port:          5173,
		CreatedAt:     2000,
		LastSeen:      2000,
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
	}
	if err := store.UpsertExposure(exposure); err != nil {
		t.Fatalf("UpsertExposure: %v", err)
	}

	got, ok := store.GetExposureByHostLabel("myrepo--vite")
	if !ok {
		t.Fatal("expected exposure by host label")
	}
	if got.Name != "vite" || got.Port != 5173 {
		t.Fatalf("exposure = %#v, want vite:5173", got)
	}

	list := store.ListExposures("env1")
	if len(list) != 1 {
		t.Fatalf("ListExposures length = %d, want 1", len(list))
	}

	deleted, err := store.DeleteExposure("env1", "vite")
	if err != nil {
		t.Fatalf("DeleteExposure: %v", err)
	}
	if !deleted {
		t.Fatal("expected DeleteExposure to report deletion")
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

func TestWildcardZonesFromLabels(t *testing.T) {
	labels := map[string]string{
		"caddy_0":                "relay.t3.example.com",
		"caddy_1":                "*.t3.example.com",
		"caddy_1.tls.email":      "ops@example.com",
		"caddy_2":                "*.t3.example.net, relay.t3.example.net",
		"caddy_3":                "web.t3.example.org",
		"caddy_4":                "*.not-t3.example.com",
		"something_else":         "*.t3.ignored.example",
		"caddy_10.reverse_proxy": "{{upstreams 80}}",
	}

	got := wildcardZonesFromLabels(labels)
	want := []string{"t3.example.com", "t3.example.net"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("wildcardZonesFromLabels = %v, want %v", got, want)
	}
}

func TestParseServedHost(t *testing.T) {
	name, zone, ok := parseServedHost("repo.t3.example.net", []string{"t3.example.com", "t3.example.net"})
	if !ok {
		t.Fatal("expected host to parse")
	}
	if name != "repo" || zone != "t3.example.net" {
		t.Fatalf("parseServedHost = (%q, %q, %v), want (%q, %q, true)", name, zone, ok, "repo", "t3.example.net")
	}

	if _, _, ok := parseServedHost("repo.deep.t3.example.net", []string{"t3.example.net"}); ok {
		t.Fatal("expected multi-label left hand side to be rejected")
	}
}

func TestTailnetDNSPacketListenAddr(t *testing.T) {
	ip4 := netip.MustParseAddr("100.64.39.97")
	ip6 := netip.MustParseAddr("fd7a:115c:a1e0::da38:2762")

	got, ok := tailnetDNSPacketListenAddr(ip4, ip6)
	if !ok {
		t.Fatal("expected IPv4 listen address")
	}
	if want := netip.AddrPortFrom(ip4, 53); got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}

	got, ok = tailnetDNSPacketListenAddr(netip.Addr{}, ip6)
	if !ok {
		t.Fatal("expected IPv6 fallback listen address")
	}
	if want := netip.AddrPortFrom(ip6, 53); got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}

	if got, ok = tailnetDNSPacketListenAddr(netip.Addr{}, netip.Addr{}); ok {
		t.Fatalf("expected no listen address, got %s", got)
	}
}

func TestProxyBidirectional_AllowsResponseAfterClientHalfClose(t *testing.T) {
	client, proxyClient := tcpConnPair(t)
	defer client.Close()
	defer proxyClient.Close()

	proxyBackend, backend := tcpConnPair(t)
	defer proxyBackend.Close()
	defer backend.Close()

	go proxyBidirectional(proxyClient, proxyBackend, zap.NewNop(), 1)

	serverDone := make(chan error, 1)
	go func() {
		got, err := io.ReadAll(backend)
		if err != nil {
			serverDone <- fmt.Errorf("backend read: %w", err)
			return
		}
		if string(got) != "request" {
			serverDone <- fmt.Errorf("backend read %q, want request", got)
			return
		}
		if _, err := backend.Write([]byte("response")); err != nil {
			serverDone <- fmt.Errorf("backend write: %w", err)
			return
		}
		if cw, ok := backend.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		serverDone <- nil
	}()

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if cw, ok := client.(closeWriter); ok {
		if err := cw.CloseWrite(); err != nil {
			t.Fatalf("client CloseWrite: %v", err)
		}
	} else {
		t.Fatal("test TCP client does not support CloseWrite")
	}

	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(got) != "response" {
		t.Fatalf("client read %q, want response", got)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func tcpConnPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("dial tcp: %v", err)
	}

	select {
	case server := <-accepted:
		deadline := time.Now().Add(2 * time.Second)
		_ = client.SetDeadline(deadline)
		_ = server.SetDeadline(deadline)
		return client, server
	case err := <-acceptErr:
		_ = client.Close()
		t.Fatalf("accept tcp: %v", err)
	case <-time.After(time.Second):
		_ = client.Close()
		t.Fatal("accept tcp timed out")
	}

	panic("unreachable")
}

func TestResolveSSHForwardTarget(t *testing.T) {
	f, err := os.CreateTemp("", "relay-ssh-test-*.db")
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

	app := &RelayApp{
		store:          store,
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
		SSHBackendPort: 2222,
	}
	if err := store.Upsert(Environment{
		ID: "env1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	env, err := app.ResolveSSHForwardTarget("myrepo.t3.example.com", 22)
	if err != nil {
		t.Fatalf("ResolveSSHForwardTarget: %v", err)
	}
	if env.Name != "myrepo" {
		t.Fatalf("expected myrepo, got %q", env.Name)
	}

	if _, err := app.ResolveSSHForwardTarget("myrepo.t3.example.com", 2222); err == nil {
		t.Fatal("expected non-22 port to be rejected")
	}
	if _, err := app.ResolveSSHForwardTarget("myrepo.evil.example.com", 22); err == nil {
		t.Fatal("expected unsupported hostname to be rejected")
	}
	if _, err := app.ResolveSSHForwardTarget("repo.deep.t3.example.com", 22); err == nil {
		t.Fatal("expected multi-label environment name to be rejected")
	}
}

func TestLoadOrCreateSSHSigner_PersistsHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh_host_ed25519_key")

	signer1, err := loadOrCreateSSHSigner(path)
	if err != nil {
		t.Fatalf("first loadOrCreateSSHSigner: %v", err)
	}
	signer2, err := loadOrCreateSSHSigner(path)
	if err != nil {
		t.Fatalf("second loadOrCreateSSHSigner: %v", err)
	}

	if got, want := string(signer2.PublicKey().Marshal()), string(signer1.PublicKey().Marshal()); got != want {
		t.Fatal("expected persisted host key to be reused")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected key mode 0600, got %o", mode)
	}
}

func TestTailnetSSHGateway_ForwardsOnlyValidatedDirectTCPIP(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	defer backend.Close()
	backendPort := backend.Addr().(*net.TCPAddr).Port
	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "ok\n")
	}()

	f, err := os.CreateTemp("", "relay-ssh-forward-test-*.db")
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

	app := &RelayApp{
		store:          store,
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
		SSHAllowedUser: "vscode",
		SSHBackendPort: backendPort,
		SSHHostKeyFile: filepath.Join(t.TempDir(), "ssh_host_ed25519_key"),
		logger:         zap.NewNop(),
	}
	if err := store.Upsert(Environment{
		ID: "env1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "127.0.0.1", Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	gateway, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen gateway: %v", err)
	}
	defer gateway.Close()
	go serveTailnetSSH(gateway, app)

	client := dialTestSSHGateway(t, gateway.Addr().String(), "vscode")
	defer client.Close()

	forward, err := client.Dial("tcp", "myrepo.t3.example.com:22")
	if err != nil {
		t.Fatalf("direct-tcpip dial: %v", err)
	}
	defer forward.Close()

	buf := make([]byte, 3)
	if _, err := io.ReadFull(forward, buf); err != nil {
		t.Fatalf("read forwarded data: %v", err)
	}
	if string(buf) != "ok\n" {
		t.Fatalf("expected backend payload, got %q", string(buf))
	}
	<-backendDone

	if _, err := client.Dial("tcp", "myrepo.t3.example.com:2222"); err == nil {
		t.Fatal("expected requested non-22 port to be rejected")
	}
	if _, err := client.Dial("tcp", "unknown.t3.example.com:22"); err == nil {
		t.Fatal("expected unknown host to be rejected")
	}
}

func TestTailnetSSHGateway_RejectsNonVscodeUser(t *testing.T) {
	app := &RelayApp{
		SSHAllowedUser: "vscode",
		SSHBackendPort: 2222,
		SSHHostKeyFile: filepath.Join(t.TempDir(), "ssh_host_ed25519_key"),
		logger:         zap.NewNop(),
	}
	gateway, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen gateway: %v", err)
	}
	defer gateway.Close()
	go serveTailnetSSH(gateway, app)

	if _, err := dialRawTestSSHGateway(gateway.Addr().String(), "root"); err == nil {
		t.Fatal("expected root user to be rejected")
	}
}

func dialTestSSHGateway(t *testing.T, addr, user string) *ssh.Client {
	t.Helper()
	client, err := dialRawTestSSHGateway(addr, user)
	if err != nil {
		t.Fatalf("dial SSH gateway: %v", err)
	}
	return client
}

func dialRawTestSSHGateway(addr, user string) (*ssh.Client, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, err
	}
	return ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
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
		DomainSuffix:   "t3.example.com",
		ProbePort:      3773,
		tokenList:      tokens,
		store:          store,
		sharedSecret:   []byte("test-secret"),
		supportedZones: []string{"t3.example.com"},
		primaryZone:    "t3.example.com",
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

func TestAPIHandler_ListEnvironmentsUsesRelayEndpointForPublishedIngress(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running",
		ProbeJSON: `{
			"environmentId":"server-env-1",
			"label":"My Repo",
			"platform":{"os":"linux","arch":"x64"},
			"serverVersion":"0.0.27",
			"advertisedEndpoints":[{
				"id":"tailscale-https",
				"label":"Tailscale HTTPS",
				"provider":{"id":"tailscale","label":"Tailscale","kind":"private-network"},
				"httpBaseUrl":"https://myrepo.tailnet.ts.net/",
				"wsBaseUrl":"wss://myrepo.tailnet.ts.net/",
				"reachability":"private-network",
				"compatibility":{"hostedHttpsApp":"compatible","desktopApp":"compatible"},
				"source":"server",
				"status":"available"
			}]
		}`,
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
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	record := resp["environments"].([]any)[0].(map[string]any)
	endpoint := record["endpoint"].(map[string]any)
	if endpoint["httpBaseUrl"] != "https://myrepo.t3.example.com" {
		t.Errorf("httpBaseUrl = %v, want relay endpoint", endpoint["httpBaseUrl"])
	}
	if endpoint["wsBaseUrl"] != "wss://myrepo.t3.example.com" {
		t.Errorf("wsBaseUrl = %v, want relay endpoint", endpoint["wsBaseUrl"])
	}
	if endpoint["providerKind"] != "t3_relay" {
		t.Errorf("providerKind = %v, want t3_relay", endpoint["providerKind"])
	}
}

func TestAPIHandler_ListEnvironmentsUsesAdvertisedTailscaleEndpointForTailnetBridgeIngress(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running",
		ProbeJSON: `{
			"environmentId":"server-env-1",
			"label":"My Repo",
			"platform":{"os":"linux","arch":"x64"},
			"serverVersion":"0.0.27",
			"advertisedEndpoints":[{
				"id":"tailscale-https",
				"label":"Tailscale HTTPS",
				"provider":{"id":"tailscale","label":"Tailscale","kind":"private-network"},
				"httpBaseUrl":"https://myrepo.tailnet.ts.net/",
				"wsBaseUrl":"wss://myrepo.tailnet.ts.net/",
				"reachability":"private-network",
				"compatibility":{"hostedHttpsApp":"compatible","desktopApp":"compatible"},
				"source":"server",
				"status":"available"
			}]
		}`,
		FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/environments", nil)
	req.RemoteAddr = "127.0.0.1:54321"
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
	record := resp["environments"].([]any)[0].(map[string]any)
	endpoint := record["endpoint"].(map[string]any)
	if endpoint["httpBaseUrl"] != "https://myrepo.tailnet.ts.net/" {
		t.Errorf("httpBaseUrl = %v, want tailscale endpoint", endpoint["httpBaseUrl"])
	}
	if endpoint["wsBaseUrl"] != "wss://myrepo.tailnet.ts.net/" {
		t.Errorf("wsBaseUrl = %v, want tailscale endpoint", endpoint["wsBaseUrl"])
	}
	if endpoint["providerKind"] != "manual" {
		t.Errorf("providerKind = %v, want manual", endpoint["providerKind"])
	}
}

func TestAPIHandler_ListEnvironmentsIgnoresUnavailableAdvertisedTailscaleEndpoint(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running",
		ProbeJSON: `{
			"environmentId":"server-env-1",
			"label":"My Repo",
			"platform":{"os":"linux","arch":"x64"},
			"serverVersion":"0.0.27",
			"advertisedEndpoints":[{
				"provider":{"id":"tailscale","label":"Tailscale","kind":"private-network"},
				"httpBaseUrl":"https://myrepo.tailnet.ts.net/",
				"wsBaseUrl":"wss://myrepo.tailnet.ts.net/",
				"reachability":"private-network",
				"status":"unavailable"
			}]
		}`,
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
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	record := resp["environments"].([]any)[0].(map[string]any)
	endpoint := record["endpoint"].(map[string]any)
	if endpoint["httpBaseUrl"] != "https://myrepo.t3.example.com" {
		t.Errorf("httpBaseUrl = %v, want relay endpoint", endpoint["httpBaseUrl"])
	}
	if endpoint["providerKind"] != "t3_relay" {
		t.Errorf("providerKind = %v, want t3_relay", endpoint["providerKind"])
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

	pairing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/pairing-token" {
			t.Errorf("unexpected pairing path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected pairing method %q", r.Method)
		}
		if r.Header.Get("X-Relay-Secret") != "test-secret" {
			t.Errorf("missing relay secret header")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode pairing request body: %v", err)
		}
		if body["proofKeyThumbprint"] != "client-proof-key-thumbprint" {
			t.Errorf("expected proof key thumbprint to be forwarded, got %q", body["proofKeyThumbprint"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pairing-1","credential":"pairing-token","expiresAt":"2026-06-12T00:00:00Z"}`))
	}))
	defer pairing.Close()

	host, portString, err := net.SplitHostPort(pairing.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split pairing address: %v", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse pairing port: %v", err)
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

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/environments/server-env-1/connect",
		strings.NewReader(`{"clientProofKeyThumbprint":"client-proof-key-thumbprint"}`),
	)
	req.Header.Set("Authorization", "Bearer tok1")
	req.Header.Set("Content-Type", "application/json")
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
	if resp["credential"] != "pairing-token" {
		t.Errorf("expected environment-issued pairing token, got %v", resp["credential"])
	}
	if resp["expiresAt"] != "2026-06-12T00:00:00Z" {
		t.Errorf("expected environment-issued expiry, got %v", resp["expiresAt"])
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

func TestAPIHandler_UpsertExposureWithSharedSecret(t *testing.T) {
	ah, store, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	env := Environment{
		ID: "devcontainer-1", ContainerID: "c1", Name: "myrepo",
		Hostname: "myrepo.t3.example.com", IP: "10.0.0.1", Port: 3773,
		Status: "running", FirstSeen: 1000, LastSeen: 1000,
	}
	if err := store.Upsert(env); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/devcontainer-1/exposures", strings.NewReader(`{"name":"Vite Dev","port":5173,"ttlSeconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Secret", "test-secret")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp exposureResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Host != "myrepo--vite-dev.t3.example.com" {
		t.Fatalf("host = %q, want myrepo--vite-dev.t3.example.com", resp.Host)
	}
	if resp.URL != "https://myrepo--vite-dev.t3.example.com" {
		t.Fatalf("url = %q", resp.URL)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/environments/devcontainer-1/exposures/Vite%20Dev", nil)
	deleteReq.Header.Set("X-Relay-Secret", "test-secret")
	deleteW := httptest.NewRecorder()
	if err := ah.ServeHTTP(deleteW, deleteReq, noopHandler()); err != nil {
		t.Fatalf("delete ServeHTTP error: %v", err)
	}
	if deleteW.Code != http.StatusNoContent {
		t.Fatalf("expected delete 204, got %d: %s", deleteW.Code, deleteW.Body.String())
	}
}

func TestAPIHandler_SharedSecretCannotListEnvironments(t *testing.T) {
	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/v1/environments", nil)
	req.Header.Set("X-Relay-Secret", "test-secret")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// noopHandler returns a caddyhttp.Handler that does nothing.
func noopHandler() caddyhttp.Handler {
	return caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return nil
	})
}
