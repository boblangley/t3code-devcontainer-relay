package t3relay

import (
	"encoding/base64"
	"fmt"
	"io/fs"
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
	Name     string           `json:"name"`
	Path     string           `json:"path"`
	Type     string           `json:"type"`
	Size     int64            `json:"size,omitempty"`
	Children []mountTreeEntry `json:"children,omitempty"`
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
	tree, err := mountTree(root, "")
	if err != nil {
		return writeJSON(w, http.StatusInternalServerError, map[string]string{
			"_tag": "MountsError", "code": "mounts_error", "reason": "tree_failed",
		})
	}
	tree.Name = filepath.Base(root)
	return writeJSON(w, http.StatusOK, map[string]any{"root": tree})
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

func mountTree(root, relativePath string) (mountTreeEntry, error) {
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
		Name: filepath.Base(fullPath),
		Path: cleanPath,
		Type: entryType,
		Size: info.Size(),
	}
	if !info.IsDir() {
		return entry, nil
	}

	dirEntries, err := os.ReadDir(fullPath)
	if err != nil {
		return entry, nil
	}
	sort.Slice(dirEntries, func(i, j int) bool {
		leftDir := dirEntries[i].IsDir()
		rightDir := dirEntries[j].IsDir()
		if leftDir != rightDir {
			return leftDir
		}
		return strings.ToLower(dirEntries[i].Name()) < strings.ToLower(dirEntries[j].Name())
	})

	entry.Children = make([]mountTreeEntry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		childRelativePath := pathJoin(cleanPath, dirEntry.Name())
		childInfo, err := dirEntry.Info()
		if err != nil || childInfo.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		child, err := mountTree(root, childRelativePath)
		if err != nil {
			continue
		}
		entry.Children = append(entry.Children, child)
	}
	return entry, nil
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
<style>
:root{color-scheme:light dark;--bg:#f7f8f5;--panel:#fff;--ink:#1d2420;--muted:#66716a;--line:#d9dfd8;--accent:#176b87;--accent2:#7a3f20;--code:#101418}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}button,input{font:inherit}
.shell{display:grid;grid-template-columns:minmax(220px,28vw) 6px 1fr;height:100vh;width:100vw;overflow:hidden}.shell.collapsed{grid-template-columns:0 0 1fr}.sidebar{background:var(--panel);border-right:1px solid var(--line);overflow:auto}.resizer{cursor:col-resize;background:var(--line)}.main{min-width:0;display:grid;grid-template-rows:auto 1fr;overflow:hidden}
.side-top,.topbar{height:48px;display:flex;align-items:center;gap:8px;padding:8px 12px;border-bottom:1px solid var(--line);background:var(--panel)}.side-title{font-weight:650;white-space:nowrap}.grow{flex:1}.icon{width:32px;height:32px;border:1px solid var(--line);background:var(--panel);color:var(--ink);display:grid;place-items:center;cursor:pointer}.icon:hover{border-color:var(--accent);color:var(--accent)}
.tree{padding:8px}.node{display:flex;align-items:center;gap:7px;width:100%;border:0;background:transparent;color:var(--ink);text-align:left;padding:5px 7px;min-height:30px;cursor:pointer}.node:hover,.node.active{background:color-mix(in srgb,var(--accent) 12%,transparent)}.twisty{width:14px;color:var(--muted)}.name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.fileicon{color:var(--accent2)}.diricon{color:var(--accent)}
.breadcrumb{display:flex;align-items:center;gap:6px;min-width:0;color:var(--muted)}.crumb{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.seg{display:flex;border:1px solid var(--line);height:32px}.seg button{border:0;background:var(--panel);color:var(--muted);padding:0 12px;cursor:pointer}.seg button.active{background:var(--accent);color:white}.token{width:min(340px,28vw);height:32px;border:1px solid var(--line);background:var(--panel);color:var(--ink);padding:0 10px}.unlock{height:32px;border:1px solid var(--accent);background:var(--accent);color:white;padding:0 12px;cursor:pointer}.unlock:disabled{border-color:var(--line);background:var(--line);color:var(--muted);cursor:default}.authstate{color:var(--muted);font-size:12px;white-space:nowrap}
.content{min-width:0;overflow:auto;background:#fff}.empty,.error{padding:24px;color:var(--muted)}.render{padding:22px;max-width:960px}.render iframe{width:100%;height:calc(100vh - 88px);border:1px solid var(--line);background:white}.render img{display:block;max-width:100%;height:auto}.codewrap{display:grid;grid-template-columns:auto 1fr;align-items:start;background:var(--code);color:#e8edf0;min-height:100%;font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}.ln{color:#7f8b94;text-align:right;padding:14px 10px 14px 16px;border-right:1px solid #2d3439;user-select:none;white-space:pre}.src{padding:14px;overflow:auto;white-space:pre}.tok-tag{color:#6ee7d8}.tok-attr{color:#f7c873}.tok-str{color:#a7d982}.tok-comment{color:#8b98a3}.tok-key{color:#8ab4ff}
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
const state={tree:null,path:null,mode:"render",open:new Set(),token:localStorage.getItem("t3relay.mounts.token")||""};
const el=id=>document.getElementById(id);el("token").value=state.token;
function authHeaders(){return state.token?{Authorization:"Bearer "+state.token}:{}}
function escapeHtml(s){return s.replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]))}
function highlight(source,path){let out=escapeHtml(source);const ext=(path.split(".").pop()||"").toLowerCase();if(["html","xml","svg","markdown","md"].includes(ext)){out=out.replace(/(&lt;!--[\s\S]*?--&gt;)/g,'<span class="tok-comment">$1</span>').replace(/(&lt;\/?[\w:-]+)/g,'<span class="tok-tag">$1</span>').replace(/([\w:-]+)=(&quot;.*?&quot;|'.*?')/g,'<span class="tok-attr">$1</span>=<span class="tok-str">$2</span>')}else{out=out.replace(/\b(function|const|let|var|return|if|else|for|while|package|import|type|struct|func|case|switch|true|false|null)\b/g,'<span class="tok-key">$1</span>').replace(/(".*?"|'.*?')/g,'<span class="tok-str">$1</span>')}return out}
async function api(path){const r=await fetch(path,{headers:authHeaders()});if(r.status===401){setAuthState("Locked");throw new Error("Invalid token.")}if(!r.ok){let msg=r.statusText;try{const e=await r.json();msg=e.reason||e.code||msg}catch{}throw new Error(msg)}setAuthState("Unlocked");return r.json()}
function setAuthState(value){el("authstate").textContent=value;el("unlock").disabled=value==="Loading"}
async function applyToken(){state.token=el("token").value.trim();if(!state.token){setAuthState("Locked");el("tree").innerHTML="";return}localStorage.setItem("t3relay.mounts.token",state.token);await loadTree();if(state.path)await loadFile()}
async function loadTree(){if(!state.token){setAuthState("Locked");return}setAuthState("Loading");try{state.tree=(await api("/v1/mounts/tree")).root;renderTree();}catch(e){el("tree").innerHTML='<div class="error">'+escapeHtml(e.message)+'</div>'}}
function renderTree(){const root=state.tree;if(!root){return}el("tree").innerHTML=nodeHtml(root,0);document.querySelectorAll("[data-path]").forEach(b=>b.onclick=()=>{const p=b.dataset.path,t=b.dataset.type;if(t==="directory"){state.open.has(p)?state.open.delete(p):state.open.add(p);renderTree()}else{selectFile(p)}})}
function nodeHtml(n,depth){const active=n.path===state.path?" active":"";const dir=n.type==="directory";const open=n.path===""||state.open.has(n.path);let h='<button class="node'+active+'" data-type="'+n.type+'" data-path="'+escapeHtml(n.path)+'" style="padding-left:'+(7+depth*16)+'px"><span class="twisty">'+(dir?(open?"v":">"):"")+'</span><span class="'+(dir?"diricon":"fileicon")+'">'+(dir?"[]":"-")+'</span><span class="name">'+escapeHtml(n.name||"mounts")+'</span></button>';if(dir&&open&&n.children){h+=n.children.map(c=>nodeHtml(c,depth+1)).join("")}return h}
function selectFile(path){state.path=path;renderTree();loadFile()}
async function loadFile(){if(!state.path)return;el("breadcrumb").innerHTML=state.path.split("/").map(p=>'<span class="crumb">'+escapeHtml(p)+'</span>').join("<span>/</span>");el("content").innerHTML='<div class="empty">Loading...</div>';try{const f=await api("/v1/mounts/file/"+encodeURIComponent(state.path).replaceAll("%2F","/")+"?mode="+state.mode);renderFile(f)}catch(e){el("content").innerHTML='<div class="error">'+escapeHtml(e.message)+'</div>'}}
function renderFile(f){if(state.mode==="render"&&f.dataUrl){el("content").innerHTML='<div class="render"><img src="'+f.dataUrl+'" alt="'+escapeHtml(f.name)+'"></div>';return}if(state.mode==="render"&&f.html){el("content").innerHTML='<div class="render"><iframe sandbox srcdoc="'+escapeHtml(f.html)+'"></iframe></div>';return}const src=f.source||"";const lines=src.split("\n").map((_,i)=>i+1).join("\n");el("content").innerHTML='<div class="codewrap"><pre class="ln">'+lines+'</pre><pre class="src">'+highlight(src,f.path)+'</pre></div>'}
el("unlock").onclick=applyToken;el("token").onkeydown=e=>{if(e.key==="Enter")applyToken()};el("token").oninput=()=>{if(!el("token").value.trim())setAuthState("Locked")};el("refresh").onclick=loadTree;el("collapse").onclick=()=>el("shell").classList.add("collapsed");el("expand").onclick=()=>el("shell").classList.remove("collapsed");
el("renderMode").onclick=()=>{state.mode="render";el("renderMode").classList.add("active");el("sourceMode").classList.remove("active");loadFile()};el("sourceMode").onclick=()=>{state.mode="source";el("sourceMode").classList.add("active");el("renderMode").classList.remove("active");loadFile()};
let dragging=false;el("resizer").onpointerdown=e=>{dragging=true;el("resizer").setPointerCapture(e.pointerId)};el("resizer").onpointermove=e=>{if(dragging&&!el("shell").classList.contains("collapsed"))el("shell").style.gridTemplateColumns=Math.max(120,e.clientX)+"px 6px 1fr"};el("resizer").onpointerup=()=>dragging=false;
if(state.token)loadTree();
</script>
</body>
</html>`
