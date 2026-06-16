package t3relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIHandler_MountsUI_NoAuth(t *testing.T) {
	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/mounts", nil)
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "T3 Relay Mounts") {
		t.Fatal("expected mounts UI HTML")
	}
	if !strings.Contains(w.Body.String(), `id="unlock"`) {
		t.Fatal("expected explicit unlock control")
	}
	if !strings.Contains(w.Body.String(), "prism-autoloader") {
		t.Fatal("expected Prism autoloader plugin")
	}
	if !strings.Contains(w.Body.String(), "linkable-line-numbers") {
		t.Fatal("expected Prism linkable line numbers")
	}
	if !strings.Contains(w.Body.String(), ".sourceview .token{background:transparent!important}") {
		t.Fatal("expected Prism token background override")
	}
}

func TestAPIHandler_MountsTree_RequiresAuth(t *testing.T) {
	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/v1/mounts/tree", nil)
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAPIHandler_MountsTree_WithAuth(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.markdown"), "# Hello\n")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "nested", "b.txt"), "text\n")

	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()
	ah.app.MountsRoot = root

	req := httptest.NewRequest(http.MethodGet, "/v1/mounts/tree", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Root mountTreeEntry `json:"root"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Root.Children) != 0 {
		t.Fatalf("tree should not eagerly load children, got %d", len(resp.Root.Children))
	}
	if !resp.Root.HasChildren {
		t.Fatal("expected root to advertise lazy children")
	}
}

func TestAPIHandler_MountsChildren_WithAuth(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.markdown"), "# Hello\n")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "nested", "b.txt"), "text\n")

	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()
	ah.app.MountsRoot = root

	req := httptest.NewRequest(http.MethodGet, "/v1/mounts/children?path=", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Children []mountTreeEntry `json:"children"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(resp.Children))
	}
	if resp.Children[0].Name != "nested" || resp.Children[0].Type != "directory" {
		t.Fatalf("first child = %#v, want nested directory", resp.Children[0])
	}
}

func TestAPIHandler_MountFile_RenderMarkdown(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "doc.markdown"), "# Hello\n\nBody\n")
	mustWriteFile(t, filepath.Join(root, "short.md"), "# Short\n")

	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()
	ah.app.MountsRoot = root

	resp := requestMountFile(t, ah, "/v1/mounts/file/doc.markdown?mode=render")
	if !resp.Renderable || resp.HTML == "" {
		t.Fatalf("response = %#v, want rendered HTML", resp)
	}
	if !strings.Contains(resp.HTML, "<h1>Hello</h1>") {
		t.Fatalf("html = %q, want heading", resp.HTML)
	}

	resp = requestMountFile(t, ah, "/v1/mounts/file/short.md?mode=render")
	if !resp.Renderable || !strings.Contains(resp.HTML, "<h1>Short</h1>") {
		t.Fatalf("response = %#v, want rendered .md HTML", resp)
	}
}

func TestAPIHandler_MountFile_SourceText(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")

	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()
	ah.app.MountsRoot = root

	resp := requestMountFile(t, ah, "/v1/mounts/file/notes.txt?mode=source")
	if resp.Source != "one\ntwo\n" || resp.Binary {
		t.Fatalf("response = %#v, want text source", resp)
	}
}

func TestAPIHandler_MountFile_RenderImage(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pixel.png"), []byte{0x89, 0x50, 0x4e, 0x47})

	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()
	ah.app.MountsRoot = root

	resp := requestMountFile(t, ah, "/v1/mounts/file/pixel.png?mode=render")
	if !resp.Binary || !strings.HasPrefix(resp.DataURL, "data:image/png;base64,") {
		t.Fatalf("response = %#v, want png data URL", resp)
	}
}

func TestAPIHandler_MountFile_SourceBinaryRejected(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pixel.png"), []byte{0x89, 0x50, 0x4e, 0x47})

	ah, _, cleanup := testAPIHandler(t, []string{"tok1"})
	defer cleanup()
	ah.app.MountsRoot = root

	req := httptest.NewRequest(http.MethodGet, "/v1/mounts/file/pixel.png?mode=source", nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", w.Code)
	}
}

func TestSafeMountPath_CleansTraversalAndRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	_, _, err := safeMountPath(root, "../outside.txt")
	if err != nil {
		t.Fatalf("safeMountPath should clean lexical traversal inside root: %v", err)
	}

	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err = safeMountPath(root, "escape")
	if err == nil {
		t.Fatal("expected symlink escaping root to be rejected")
	}
}

func requestMountFile(t *testing.T, ah *APIHandler, target string) mountFileResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer tok1")
	w := httptest.NewRecorder()

	if err := ah.ServeHTTP(w, req, noopHandler()); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp mountFileResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func mustWriteFile(t *testing.T, path string, content any) {
	t.Helper()
	var data []byte
	switch v := content.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		t.Fatalf("unsupported content type %T", content)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
