package t3relay

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/russross/blackfriday/v2"
)

const maxMountFileBytes = 5 * 1024 * 1024

type mountTreeEntry struct {
	Name        string           `json:"name"`
	Path        string           `json:"path"`
	Type        string           `json:"type"`
	Size        int64            `json:"size,omitempty"`
	HasChildren bool             `json:"hasChildren,omitempty"`
	Children    []mountTreeEntry `json:"children,omitempty"`
}

type mountFileResponse struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Extension  string `json:"extension"`
	Mode       string `json:"mode"`
	MimeType   string `json:"mimeType"`
	Binary     bool   `json:"binary"`
	Renderable bool   `json:"renderable"`
	Source     string `json:"source,omitempty"`
	HTML       string `json:"html,omitempty"`
	DataURL    string `json:"dataUrl,omitempty"`
}

type mountRenderer struct {
	render func([]byte) string
}

var mountRenderers = map[string]mountRenderer{
	".html": {
		render: func(data []byte) string { return string(data) },
	},
	".markdown": {
		render: func(data []byte) string {
			return string(blackfriday.Run(data))
		},
	},
	".md": {
		render: func(data []byte) string {
			return string(blackfriday.Run(data))
		},
	},
}

var mountImageExtensions = map[string]string{
	".apng": "image/apng",
	".avif": "image/avif",
	".gif":  "image/gif",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
}

func (a *APIHandler) serveMountsUI(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err := w.Write([]byte(mountsHTML))
	return err
}

func (a *APIHandler) handleMountsTree(w http.ResponseWriter, _ *http.Request) error {
	root := strings.TrimSpace(a.app.MountsRoot)
	if root == "" {
		return writeJSON(w, http.StatusOK, map[string]any{"root": mountTreeEntry{Name: "mounts", Path: "", Type: "directory"}})
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return writeJSON(w, http.StatusOK, map[string]any{"root": mountTreeEntry{Name: filepath.Base(root), Path: "", Type: "directory"}})
		}
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "MountsError", "code": "mounts_error", "reason": "root_unreadable",
		})
	}
	tree, err := mountEntry(root, "")
	if err != nil {
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "MountsError", "code": "mounts_error", "reason": "tree_failed",
		})
	}
	tree.Name = filepath.Base(root)
	return writeJSON(w, http.StatusOK, map[string]any{"root": tree})
}

func (a *APIHandler) handleMountsChildren(w http.ResponseWriter, r *http.Request) error {
	children, err := mountChildren(a.app.MountsRoot, r.URL.Query().Get("path"))
	if err != nil {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "invalid_path",
		})
	}
	return writeJSON(w, http.StatusOK, map[string]any{"children": children})
}

func (a *APIHandler) handleMountFile(w http.ResponseWriter, r *http.Request) error {
	relativePath := strings.TrimPrefix(r.URL.Path, "/v1/mounts/file/")
	relativePath, err := url.PathUnescape(relativePath)
	if err != nil {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "invalid_path",
		})
	}

	fullPath, cleanPath, err := safeMountPath(a.app.MountsRoot, relativePath)
	if err != nil {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "invalid_path",
		})
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return writeJSON(w, http.StatusNotFound, map[string]string{
				"_tag": "NotFoundError", "code": "not_found", "reason": "unknown_file",
			})
		}
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "MountsError", "code": "mounts_error", "reason": "stat_failed",
		})
	}
	if info.IsDir() {
		return writeJSON(w, http.StatusBadRequest, map[string]string{
			"_tag": "BadRequestError", "code": "bad_request", "reason": "path_is_directory",
		})
	}
	if info.Size() > maxMountFileBytes {
		return writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"_tag": "FileTooLargeError", "code": "file_too_large", "reason": "file_too_large",
		})
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "MountsError", "code": "mounts_error", "reason": "read_failed",
		})
	}

	ext := strings.ToLower(filepath.Ext(cleanPath))
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "render"
	}
	mimeType := mountMimeType(ext, data)
	resp := mountFileResponse{
		Path:       cleanPath,
		Name:       filepath.Base(cleanPath),
		Extension:  ext,
		Mode:       mode,
		MimeType:   mimeType,
		Binary:     isBinaryMountFile(ext, data),
		Renderable: isRenderableMountFile(ext),
	}

	if mode == "render" {
		if mimeType, ok := mountImageExtensions[ext]; ok {
			resp.MimeType = mimeType
			resp.DataURL = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
			return writeJSON(w, http.StatusOK, resp)
		}
		if renderer, ok := mountRenderers[ext]; ok {
			resp.HTML = renderer.render(data)
			return writeJSON(w, http.StatusOK, resp)
		}
	}

	if resp.Binary {
		return writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
			"_tag": "UnsupportedMediaTypeError", "code": "unsupported_media_type", "reason": "binary_source_unavailable",
		})
	}
	resp.Mode = "source"
	resp.Source = string(data)
	return writeJSON(w, http.StatusOK, resp)
}

func mountEntry(root, relativePath string) (mountTreeEntry, error) {
	fullPath, cleanPath, err := safeMountPath(root, relativePath)
	if err != nil {
		return mountTreeEntry{}, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return mountTreeEntry{}, err
	}
	entryType := "file"
	if info.IsDir() {
		entryType = "directory"
	}
	entry := mountTreeEntry{
		Name:        filepath.Base(fullPath),
		Path:        cleanPath,
		Type:        entryType,
		Size:        info.Size(),
		HasChildren: info.IsDir() && directoryHasVisibleEntries(fullPath),
	}
	return entry, nil
}

func mountChildren(root, relativePath string) ([]mountTreeEntry, error) {
	fullPath, _, err := safeMountPath(root, relativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(fullPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory")
	}

	dirEntries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	sort.Slice(dirEntries, func(i, j int) bool {
		leftDir := dirEntries[i].IsDir()
		rightDir := dirEntries[j].IsDir()
		if leftDir != rightDir {
			return leftDir
		}
		return strings.ToLower(dirEntries[i].Name()) < strings.ToLower(dirEntries[j].Name())
	})

	children := make([]mountTreeEntry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		childInfo, err := dirEntry.Info()
		if err != nil || childInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		childRelativePath := pathJoin(relativePath, dirEntry.Name())
		child, err := mountEntry(root, childRelativePath)
		if err != nil {
			continue
		}
		children = append(children, child)
	}
	return children, nil
}

func directoryHasVisibleEntries(path string) bool {
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, dirEntry := range dirEntries {
		childInfo, err := dirEntry.Info()
		if err != nil || childInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		return true
	}
	return false
}

func safeMountPath(root, relativePath string) (string, string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", fmt.Errorf("mount root is empty")
	}
	cleanPath := filepath.ToSlash(filepath.Clean("/" + relativePath))
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	if cleanPath == "." {
		cleanPath = ""
	}
	fullPath := filepath.Join(root, filepath.FromSlash(cleanPath))
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		if os.IsNotExist(err) && cleanPath == "" {
			return root, cleanPath, nil
		}
		return "", "", err
	}
	targetEval, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return fullPath, cleanPath, nil
	}
	rel, err := filepath.Rel(rootEval, targetEval)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path escapes mount root")
	}
	return fullPath, cleanPath, nil
}

func pathJoin(left, right string) string {
	if left == "" {
		return right
	}
	return left + "/" + right
}

func mountMimeType(ext string, data []byte) string {
	if mimeType := mime.TypeByExtension(ext); mimeType != "" {
		return mimeType
	}
	return http.DetectContentType(data)
}

func isRenderableMountFile(ext string) bool {
	if _, ok := mountRenderers[ext]; ok {
		return true
	}
	_, ok := mountImageExtensions[ext]
	return ok
}

func isBinaryMountFile(ext string, data []byte) bool {
	if _, ok := mountImageExtensions[ext]; ok {
		return true
	}
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return true
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

const mountsHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>T3 Relay Mounts</title>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/themes/prism-okaidia.min.css">
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/plugins/line-numbers/prism-line-numbers.min.css">
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/plugins/line-highlight/prism-line-highlight.min.css">
<script>window.Prism={manual:true};</script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/prism.min.js"></script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/plugins/autoloader/prism-autoloader.min.js"></script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/plugins/autolinker/prism-autolinker.min.js"></script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/plugins/line-numbers/prism-line-numbers.min.js"></script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/plugins/line-highlight/prism-line-highlight.min.js"></script>
<style>
:root{color-scheme:light dark;--bg:#f7f8f5;--panel:#fff;--ink:#1d2420;--muted:#66716a;--line:#d9dfd8;--accent:#176b87;--accent2:#7a3f20;--code:#101418}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}button,input{font:inherit}
.shell{display:grid;grid-template-columns:600px 6px 1fr;height:100vh;width:100vw;overflow:hidden}.shell.collapsed{grid-template-columns:0 0 1fr}.sidebar{background:var(--panel);border-right:1px solid var(--line);overflow:auto}.resizer{cursor:col-resize;background:var(--line)}.main{min-width:0;display:grid;grid-template-rows:auto 1fr;overflow:hidden}
.side-top,.topbar{height:48px;display:flex;align-items:center;gap:8px;padding:8px 12px;border-bottom:1px solid var(--line);background:var(--panel)}.side-title{font-weight:650;white-space:nowrap}.grow{flex:1}.icon{width:32px;height:32px;border:1px solid var(--line);background:var(--panel);color:var(--ink);display:grid;place-items:center;cursor:pointer}.icon:hover{border-color:var(--accent);color:var(--accent)}
.tree{padding:8px}.node{display:flex;align-items:center;gap:7px;width:100%;border:0;background:transparent;color:var(--ink);text-align:left;padding:5px 7px;min-height:30px;cursor:pointer}.node:hover,.node.active{background:color-mix(in srgb,var(--accent) 12%,transparent)}.twisty{width:14px;color:var(--muted)}.name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.fileicon{color:var(--accent2)}.diricon{color:var(--accent)}
.breadcrumb{display:flex;align-items:center;gap:6px;min-width:0;color:var(--muted);cursor:text}.crumb{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.pathinput{width:min(760px,42vw);height:32px;border:1px solid var(--accent);background:var(--panel);color:var(--ink);padding:0 10px}.seg{display:flex;border:1px solid var(--line);height:32px}.seg button{border:0;background:var(--panel);color:var(--muted);padding:0 12px;cursor:pointer}.seg button.active{background:var(--accent);color:white}.token{width:min(340px,28vw);height:32px;border:1px solid var(--line);background:var(--panel);color:var(--ink);padding:0 10px}.unlock{height:32px;border:1px solid var(--accent);background:var(--accent);color:white;padding:0 12px;cursor:pointer}.unlock:disabled{border-color:var(--line);background:var(--line);color:var(--muted);cursor:default}.authstate{color:var(--muted);font-size:12px;white-space:nowrap}
.content{min-width:0;overflow:auto;background:#fff}.empty,.error{padding:24px;color:var(--muted)}.render{padding:22px;max-width:960px}.render iframe{width:100%;height:calc(100vh - 88px);border:1px solid var(--line);background:white}.render img{display:block;max-width:100%;height:auto}.sourceview{min-height:100%;background:var(--code)}pre[class*="language-"].sourcepre{min-height:100vh;margin:0;border-radius:0;font-size:13px;line-height:1.55}.line-highlight{background:rgba(255,255,255,.14)}
@media (max-width:720px){.shell{grid-template-columns:minmax(180px,76vw) 6px 1fr}.token{width:150px}.side-title{display:none}}
</style>
</head>
<body>
<div id="shell" class="shell">
  <aside class="sidebar">
    <div class="side-top"><button class="icon" id="collapse" title="Collapse sidebar">&#9776;</button><div class="side-title">Mounts</div><div class="grow"></div><button class="icon" id="refresh" title="Refresh">&#8635;</button></div>
    <div id="tree" class="tree"></div>
  </aside>
  <div id="resizer" class="resizer"></div>
  <main class="main">
    <div class="topbar"><button class="icon" id="expand" title="Show sidebar">&#9776;</button><div id="breadcrumb" class="breadcrumb"><span class="crumb">No file selected</span></div><div class="grow"></div><span id="authstate" class="authstate">Locked</span><input id="token" class="token" type="password" autocomplete="off" placeholder="Bearer token"><button id="unlock" class="unlock">Unlock</button><div class="seg"><button id="renderMode" class="active">Render</button><button id="sourceMode">Source</button></div></div>
    <section id="content" class="content"><div class="empty">Select a file from the mounted filesystem.</div></section>
  </main>
</div>
<script>
if(window.Prism&&Prism.plugins&&Prism.plugins.autoloader){Prism.plugins.autoloader.languages_path="https://cdnjs.cloudflare.com/ajax/libs/prism/1.30.0/components/"}
const state={tree:null,nodes:new Map(),path:null,mode:"render",open:new Set(),token:localStorage.getItem("t3relay.mounts.token")||""};
const el=id=>document.getElementById(id);el("token").value=state.token;
function authHeaders(){return state.token?{Authorization:"Bearer "+state.token}:{}}
function escapeHtml(s){return s.replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]))}
async function api(path){const r=await fetch(path,{headers:authHeaders()});if(r.status===401){setAuthState("Locked");throw new Error("Invalid token.")}if(!r.ok){let msg=r.statusText;try{const e=await r.json();msg=e.reason||e.code||msg}catch{}throw new Error(msg)}setAuthState("Unlocked");return r.json()}
function setAuthState(value){el("authstate").textContent=value;el("unlock").disabled=value==="Loading"}
async function applyToken(){state.token=el("token").value.trim();if(!state.token){setAuthState("Locked");el("tree").innerHTML="";return}localStorage.setItem("t3relay.mounts.token",state.token);await loadTree();if(state.path)await loadFile()}
async function loadTree(){if(!state.token){setAuthState("Locked");return}setAuthState("Loading");try{state.tree=(await api("/v1/mounts/tree")).root;state.nodes=new Map();rememberNode(state.tree);state.open=new Set([""]);await loadChildren(state.tree);renderTree();}catch(e){el("tree").innerHTML='<div class="error">'+escapeHtml(e.message)+'</div>'}}
function rememberNode(n){state.nodes.set(n.path,n);(n.children||[]).forEach(rememberNode)}
async function loadChildren(n){if(!n||n.type!=="directory"||n.loaded)return;n.loading=true;renderTree();const data=await api("/v1/mounts/children?path="+encodeURIComponent(n.path));n.children=data.children||[];n.loaded=true;n.loading=false;n.children.forEach(rememberNode)}
async function toggleDirectory(path){const n=state.nodes.get(path);if(!n)return;if(state.open.has(path)){state.open.delete(path);renderTree();return}try{await loadChildren(n);state.open.add(path);renderTree()}catch(e){el("tree").innerHTML='<div class="error">'+escapeHtml(e.message)+'</div>'}}
function renderTree(){const root=state.tree;if(!root){return}el("tree").innerHTML=nodeHtml(root,0);document.querySelectorAll("[data-path]").forEach(b=>b.onclick=()=>{const p=b.dataset.path,t=b.dataset.type;if(t==="directory")toggleDirectory(p);else selectFile(p)})}
function nodeHtml(n,depth){const active=n.path===state.path?" active":"";const dir=n.type==="directory";const open=n.path===""||state.open.has(n.path);const twisty=dir?(n.loading?"...":open?"v":n.hasChildren?">":""):"";let h='<button class="node'+active+'" data-type="'+n.type+'" data-path="'+escapeHtml(n.path)+'" style="padding-left:'+(7+depth*16)+'px"><span class="twisty">'+twisty+'</span><span class="'+(dir?"diricon":"fileicon")+'">'+(dir?"[]":"-")+'</span><span class="name">'+escapeHtml(n.name||"mounts")+'</span></button>';if(dir&&open&&n.children){h+=n.children.map(c=>nodeHtml(c,depth+1)).join("")}return h}
function selectFile(path){state.path=path;renderTree();loadFile()}
function renderBreadcrumb(){const value=state.path||"";el("breadcrumb").innerHTML=value?value.split("/").map(p=>'<span class="crumb">'+escapeHtml(p)+'</span>').join("<span>/</span>"):'<span class="crumb">No file selected</span>'}
function editBreadcrumb(){if(!state.path)return;el("breadcrumb").innerHTML='<input id="pathinput" class="pathinput" value="'+escapeHtml(state.path)+'">';const input=el("pathinput");input.focus();input.select();input.onblur=renderBreadcrumb;input.onkeydown=e=>{if(e.key==="Escape"||e.key==="Enter")input.blur()}}
async function loadFile(){if(!state.path)return;renderBreadcrumb();el("content").innerHTML='<div class="empty">Loading...</div>';try{const f=await api("/v1/mounts/file/"+encodeURIComponent(state.path).replaceAll("%2F","/")+"?mode="+state.mode);renderFile(f)}catch(e){el("content").innerHTML='<div class="error">'+escapeHtml(e.message)+'</div>'}}
function prismLanguage(path){const ext=(path.split(".").pop()||"").toLowerCase();return {cjs:"javascript",cts:"typescript",go:"go",html:"markup",htm:"markup",js:"javascript",json:"json",jsx:"jsx",markdown:"markdown",md:"markdown",mjs:"javascript",mts:"typescript",py:"python",rb:"ruby",rs:"rust",sh:"bash",svg:"markup",ts:"typescript",tsx:"tsx",txt:"none",xml:"markup",yaml:"yaml",yml:"yaml"}[ext]||ext||"none"}
function lineHash(){const prefix="#source.";return location.hash.startsWith(prefix)?location.hash.slice(prefix.length):""}
function renderFile(f){if(state.mode==="render"&&f.dataUrl){el("content").innerHTML='<div class="render"><img src="'+f.dataUrl+'" alt="'+escapeHtml(f.name)+'"></div>';return}if(state.mode==="render"&&f.html){el("content").innerHTML='<div class="render"><iframe sandbox srcdoc="'+escapeHtml(f.html)+'"></iframe></div>';return}const src=f.source||"";const lang=prismLanguage(f.path);const line=lineHash();el("content").innerHTML='<div class="sourceview"><pre id="source" class="sourcepre line-numbers linkable-line-numbers language-'+escapeHtml(lang)+'"'+(line?' data-line="'+escapeHtml(line)+'"':'')+'><code class="language-'+escapeHtml(lang)+'">'+escapeHtml(src)+'</code></pre></div>';const code=document.querySelector("#source code");if(window.Prism&&code)Prism.highlightElement(code)}
el("breadcrumb").onclick=editBreadcrumb;el("unlock").onclick=applyToken;el("token").onkeydown=e=>{if(e.key==="Enter")applyToken()};el("token").oninput=()=>{if(!el("token").value.trim())setAuthState("Locked")};el("refresh").onclick=loadTree;el("collapse").onclick=()=>el("shell").classList.add("collapsed");el("expand").onclick=()=>el("shell").classList.remove("collapsed");
el("renderMode").onclick=()=>{state.mode="render";el("renderMode").classList.add("active");el("sourceMode").classList.remove("active");loadFile()};el("sourceMode").onclick=()=>{state.mode="source";el("sourceMode").classList.add("active");el("renderMode").classList.remove("active");loadFile()};
let dragging=false;el("resizer").onpointerdown=e=>{dragging=true;el("resizer").setPointerCapture(e.pointerId)};el("resizer").onpointermove=e=>{if(dragging&&!el("shell").classList.contains("collapsed"))el("shell").style.gridTemplateColumns=Math.max(120,e.clientX)+"px 6px 1fr"};el("resizer").onpointerup=()=>dragging=false;
if(state.token)loadTree();
</script>
</body>
</html>`
