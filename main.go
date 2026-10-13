package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tiny-tube/store"
)

var (
	videoDir string
	fs       store.Store
)

func init() {
	// HLS mime types missing on plain Windows Go installs
	_ = mime.AddExtensionType(".m3u8", "application/vnd.apple.mpegurl")
	_ = mime.AddExtensionType(".ts", "video/mp2t")
}

var listTmpl = template.Must(template.New("list").Parse(`<!doctype html><html lang="el"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Tiny Tube</title>
<style>
:root{--bg:#0d1117;--card:#161d27;--line:#26303f;--txt:#e8edf3;--mut:#9aa6b5;--acc:#4fc3f7;--acc2:#7c5cff;--ok:#3ddc84;--wait:#ffb020}
*{box-sizing:border-box}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;margin:0;color:var(--txt);min-height:100vh;background:radial-gradient(1100px 380px at 50% -90px,#1c2c49 0%,var(--bg) 62%) fixed,var(--bg)}
a{color:var(--acc);text-decoration:none}
.wrap{max-width:1080px;margin:auto;padding:0 16px}
header.top{position:sticky;top:0;z-index:10;background:rgba(13,17,23,.86);backdrop-filter:blur(10px);border-bottom:1px solid var(--line)}
.bar{display:flex;align-items:center;gap:10px;padding:12px 0}
.logo{display:flex;align-items:center;gap:10px;font-weight:800;font-size:20px;color:var(--txt)}
.mark{width:32px;height:32px;border-radius:9px;background:linear-gradient(135deg,var(--acc2),var(--acc));display:inline-grid;place-items:center;color:#fff;font-size:15px}
.sp{flex:1}
.btn{display:inline-block;background:linear-gradient(135deg,var(--acc2),var(--acc));color:#fff;font-weight:700;padding:9px 18px;border-radius:999px;border:0;cursor:pointer;font-size:14px}
a.btn{color:#fff}
.btn:hover{filter:brightness(1.12)}
.search{display:flex;gap:8px;margin:24px 0 18px}
.search input{flex:1;padding:13px 16px;border-radius:12px;border:1px solid var(--line);background:#0f1620;color:var(--txt);font-size:15px;outline:none}
.search input:focus{border-color:var(--acc)}
.search button{padding:0 24px;border-radius:12px;border:0;background:linear-gradient(135deg,var(--acc2),var(--acc));color:#fff;font-weight:700;font-size:15px;cursor:pointer}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:14px;padding-bottom:44px}
.card{background:linear-gradient(180deg,#1a2332,var(--card));border:1px solid var(--line);border-radius:14px;padding:0;overflow:hidden;transition:transform .12s ease,border-color .12s ease}
.card:hover{transform:translateY(-2px);border-color:var(--acc)}
.card .thumb{display:block;width:100%;aspect-ratio:16/9;object-fit:cover;background:#0a0f16}
.card .thumb.ph{display:grid;place-items:center;background:linear-gradient(135deg,#1c2c49,#101827);color:var(--acc);font-size:34px;aspect-ratio:16/9}
.card .cbody{padding:14px 16px 16px}
.card h3{margin:0;font-size:17px;line-height:1.35}
.card h3 a{color:var(--txt)}
.card h3 a:hover{color:var(--acc)}
.card p{margin:8px 0 0;color:var(--mut);font-size:13.5px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.tags{display:flex;gap:6px;flex-wrap:wrap;margin-top:12px}
.tag{display:inline-flex;align-items:center;font-size:11.5px;font-weight:700;padding:3px 10px;border-radius:999px;background:#2a3648;color:#cdd8e4;text-transform:uppercase;letter-spacing:.4px}
.dot{width:8px;height:8px;border-radius:50%;margin-right:6px}
.dot.ok{background:var(--ok);box-shadow:0 0 6px var(--ok)}
.dot.wait{background:var(--wait);box-shadow:0 0 6px var(--wait)}
.empty{text-align:center;padding:56px 20px;background:var(--card);border:1px dashed var(--line);border-radius:14px;color:var(--mut)}
.empty p{margin:0 0 18px;font-size:16px}
footer{color:var(--mut);font-size:12px;text-align:center;padding:20px}
</style>
</head><body><header class="top"><div class="wrap bar"><span class="logo"><span class="mark">▶</span>Tiny Tube</span><span class="sp"></span><a class="btn" href="/upload">+ Upload</a></div></header><div class="wrap">
<form class="search" method="GET" action="/"><input type="text" name="q" placeholder="αναζήτηση τίτλου, περιγραφής, κατηγορίας..." value="{{.Q}}"><button>OK</button></form>
{{if .Items}}<div class="grid">{{range .Items}}<div class="card"><a href="/watch?id={{.ID}}">{{if .Poster}}<img class="thumb" loading="lazy" src="/videos/{{.ID}}/{{.Poster}}" alt="{{.Title}}">{{else}}<div class="thumb ph"><span>▶</span></div>{{end}}</a><div class="cbody"><h3><a href="/watch?id={{.ID}}">{{.Title}}</a></h3>{{if .Desc}}<p>{{.Desc}}</p>{{end}}<div class="tags">{{if .Category}}<span class="tag">{{.Category}}</span>{{end}}<span class="tag"><span class="dot {{if eq .Status "ready"}}ok{{else}}wait{{end}}"></span>{{.Status}}</span></div></div></div>{{end}}</div>{{else}}<div class="empty"><p>Κανένα βίντεο ακόμα. Ανέβασε από /upload</p><a class="btn" href="/upload">+ Upload</a></div>{{end}}
</div><footer>Tiny Tube · Go + HLS</footer></body></html>`))

var watchTmpl = template.Must(template.New("watch").Parse(`<!doctype html><html lang="el"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} - Tiny Tube</title>
<script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
<style>
:root{--bg:#0d1117;--card:#161d27;--line:#26303f;--txt:#e8edf3;--mut:#9aa6b5;--acc:#4fc3f7;--acc2:#7c5cff;--ok:#3ddc84;--wait:#ffb020}
*{box-sizing:border-box}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;margin:0;color:var(--txt);min-height:100vh;background:radial-gradient(1100px 380px at 50% -90px,#1c2c49 0%,var(--bg) 62%) fixed,var(--bg)}
a{color:var(--acc);text-decoration:none}
.wrap{max-width:1080px;margin:auto;padding:0 16px}
.wrap.narrow{max-width:860px}
header.top{position:sticky;top:0;z-index:10;background:rgba(13,17,23,.86);backdrop-filter:blur(10px);border-bottom:1px solid var(--line)}
.bar{display:flex;align-items:center;gap:10px;padding:12px 0}
.logo{display:flex;align-items:center;gap:10px;font-weight:800;font-size:20px;color:var(--txt)}
.mark{width:32px;height:32px;border-radius:9px;background:linear-gradient(135deg,var(--acc2),var(--acc));display:inline-grid;place-items:center;color:#fff;font-size:15px}
.back{display:inline-block;margin:18px 0 12px;color:var(--mut);font-size:14px}
.back:hover{color:var(--acc)}
.player{background:#000;border:1px solid var(--line);border-radius:14px;overflow:hidden;box-shadow:0 14px 44px rgba(0,0,0,.5)}
.player video{width:100%;aspect-ratio:16/9;display:block;background:#000}
.info{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:18px 20px;margin:14px 0 40px}
.info h2{margin:0 0 4px;font-size:22px}
.tags{display:flex;gap:6px;flex-wrap:wrap;margin-top:10px}
.tag{display:inline-flex;align-items:center;font-size:11.5px;font-weight:700;padding:3px 10px;border-radius:999px;background:#2a3648;color:#cdd8e4;text-transform:uppercase;letter-spacing:.4px}
.dot{width:8px;height:8px;border-radius:50%;margin-right:6px}
.dot.ok{background:var(--ok);box-shadow:0 0 6px var(--ok)}
.dot.wait{background:var(--wait);box-shadow:0 0 6px var(--wait)}
.desc{color:#c6d0dc;line-height:1.55}
.notice{margin-top:12px;padding:10px 14px;border-radius:10px;background:rgba(255,176,32,.1);border:1px solid rgba(255,176,32,.4);font-size:14px}
.hint{color:var(--mut)}
.admin{margin-top:14px;display:flex;gap:8px;align-items:center}
.admin form{margin:0}
.abtn{display:inline-block;font-size:13px;font-weight:700;padding:7px 14px;border-radius:9px;border:1px solid var(--line);background:#0f1620;color:var(--txt);cursor:pointer;font-family:inherit}
a.abtn{color:var(--txt)}
.abtn:hover{border-color:var(--acc)}
.abtn.danger{color:#ff8a8a;border-color:#5a2b2b}
.abtn.danger:hover{border-color:#ff8a8a}
.speedbar{display:flex;gap:6px;align-items:center;flex-wrap:wrap;margin:12px 0 0}
.speedbar span{color:var(--mut);font-size:13px;margin-right:2px}
.speedbar button{background:#0f1620;border:1px solid var(--line);color:var(--txt);border-radius:8px;padding:6px 10px;font-size:13px;cursor:pointer}
.speedbar button:hover{border-color:var(--acc)}
.speedbar button.on{border-color:var(--acc);color:var(--acc)}
footer{color:var(--mut);font-size:12px;text-align:center;padding:20px}
</style>
</head><body><header class="top"><div class="wrap bar"><a class="logo" href="/"><span class="mark">▶</span>Tiny Tube</a></div></header><div class="wrap narrow"><a class="back" href="/">← πίσω</a><div class="player">
<video id="v" controls autoplay muted playsinline></video>
<script>
const id="{{.ID}}", hls="{{.HLS}}", orig="{{.Original}}";
const v=document.getElementById("v");
// Autoplay: browsers only allow it muted. Start muted, then try sound;
// if blocked, the first click anywhere unmutes.
function autoplay(){ const p=v.play(); if(p){ p.then(()=>{ v.muted=false; const q=v.play(); if(q) q.catch(()=>{}); }).catch(()=>{}); } }
document.addEventListener("click",()=>{ v.muted=false; },{once:true});
if(hls){ const url="/videos/"+id+"/"+hls;
  if(v.canPlayType("application/vnd.apple.mpegurl")){ v.src=url; autoplay(); }
  else if(window.Hls && Hls.isSupported()){ const h=new Hls(); h.loadSource(url); h.attachMedia(v); h.on(Hls.Events.MANIFEST_PARSED,autoplay); }
  else { v.src="/videos/"+id+"/"+orig; autoplay(); }
} else { v.src="/videos/"+id+"/"+orig; autoplay(); }
window.addEventListener("DOMContentLoaded",()=>{
  const vv=document.getElementById("v");
  document.querySelectorAll(".speedbar button").forEach(b=>b.addEventListener("click",()=>{
    vv.playbackRate=parseFloat(b.dataset.s);
    document.querySelectorAll(".speedbar button").forEach(x=>x.classList.toggle("on",x===b));
  }));
});
</script>
</div><div class="speedbar"><span>Ταχύτητα:</span><button data-s="0.5">0.5x</button><button data-s="0.75">0.75x</button><button data-s="1" class="on">1x</button><button data-s="1.25">1.25x</button><button data-s="1.5">1.5x</button><button data-s="1.75">1.75x</button><button data-s="2">2x</button></div><div class="info"><h2>{{.Title}}</h2><div class="tags">{{if .Category}}<span class="tag">{{.Category}}</span>{{end}}<span class="tag"><span class="dot {{if eq .Status "ready"}}ok{{else}}wait{{end}}"></span>{{.Status}}</span></div>{{if .Desc}}<p class="desc">{{.Desc}}</p>{{end}}{{if eq .Status "processing"}}<div class="notice">Μετατροπή σε εξέλιξη — το HLS δεν είναι έτοιμο ακόμα. Δοκίμασε σε λίγο.</div>{{end}}{{if and (eq .Status "ready") .HLS}}{{else}}<p class="hint"><small>Αν πας στο τέλος και κολλάει: λείπει faststart/HLS ακόμα (status={{.Status}}). Περίμενε το transcode.</small></p>{{end}}<div class="admin"><a class="abtn" href="/edit?id={{.ID}}">Επεξεργασία</a><form method="POST" action="/api/delete" onsubmit="return confirm('Σίγουρα διαγραφή;')"><input type="hidden" name="id" value="{{.ID}}"><button class="abtn danger" type="submit">Διαγραφή</button></form></div></div>
</div><footer>Tiny Tube · Go + HLS</footer></body></html>`))

var uploadTmpl = template.Must(template.New("upload").Parse(`<!doctype html><html lang="el"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Upload - Tiny Tube</title>
<style>
:root{--bg:#0d1117;--card:#161d27;--line:#26303f;--txt:#e8edf3;--mut:#9aa6b5;--acc:#4fc3f7;--acc2:#7c5cff}
*{box-sizing:border-box}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;margin:0;color:var(--txt);min-height:100vh;background:radial-gradient(1100px 380px at 50% -90px,#1c2c49 0%,var(--bg) 62%) fixed,var(--bg)}
a{color:var(--acc);text-decoration:none}
.wrap{max-width:1080px;margin:auto;padding:0 16px}
.wrap.narrow{max-width:640px}
header.top{position:sticky;top:0;z-index:10;background:rgba(13,17,23,.86);backdrop-filter:blur(10px);border-bottom:1px solid var(--line)}
.bar{display:flex;align-items:center;gap:10px;padding:12px 0}
.logo{display:flex;align-items:center;gap:10px;font-weight:800;font-size:20px;color:var(--txt)}
.mark{width:32px;height:32px;border-radius:9px;background:linear-gradient(135deg,var(--acc2),var(--acc));display:inline-grid;place-items:center;color:#fff;font-size:15px}
.back{display:inline-block;margin:18px 0 12px;color:var(--mut);font-size:14px}
.back:hover{color:var(--acc)}
.panel{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:26px;margin:0 0 40px}
.panel h2{margin:0;font-size:22px}
.mut{color:var(--mut);font-size:14px;margin:6px 0 8px}
.panel label{display:block;font-size:14px;font-weight:600;margin-top:16px}
.panel input{display:block;width:100%;padding:12px 14px;margin-top:6px;border-radius:10px;border:1px solid var(--line);background:#0f1620;color:var(--txt);font-size:15px;outline:none}
.panel input:focus{border-color:var(--acc)}
.panel input[type=file]{border-style:dashed;padding:14px;cursor:pointer}
.panel .btn{width:100%;margin-top:22px;padding:13px;display:inline-block;background:linear-gradient(135deg,var(--acc2),var(--acc));color:#fff;font-weight:700;border-radius:12px;border:0;cursor:pointer;font-size:16px}
.panel .btn:hover{filter:brightness(1.12)}
</style>
</head><body><header class="top"><div class="wrap bar"><a class="logo" href="/"><span class="mark">▶</span>Tiny Tube</a></div></header><div class="wrap narrow"><a class="back" href="/">← λίστα</a><div class="panel"><h2>Ανέβασμα βίντεο</h2><p class="mut">MP4 ~1GB ok — το HLS φτιάχνεται στο background.</p><form method="POST" action="/api/upload" enctype="multipart/form-data">
<label>Τίτλος<input name="title" required></label><label>Περιγραφή<input name="desc"></label><label>Κατηγορία<input name="category" placeholder="πχ ΔΙΕΥΘΥΝΣΗ"></label>
<label>Αρχείο<input type="file" name="file" accept="video/mp4,video/*" required></label><p><button class="btn">Ανέβασμα</button></p></form>
</div></div></body></html>`))

var editTmpl = template.Must(template.New("edit").Parse(`<!doctype html><html lang="el"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Επεξεργασία - Tiny Tube</title>
<style>
:root{--bg:#0d1117;--card:#161d27;--line:#26303f;--txt:#e8edf3;--mut:#9aa6b5;--acc:#4fc3f7;--acc2:#7c5cff}
*{box-sizing:border-box}
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;margin:0;color:var(--txt);min-height:100vh;background:radial-gradient(1100px 380px at 50% -90px,#1c2c49 0%,var(--bg) 62%) fixed,var(--bg)}
a{color:var(--acc);text-decoration:none}
.wrap{max-width:1080px;margin:auto;padding:0 16px}
.wrap.narrow{max-width:640px}
header.top{position:sticky;top:0;z-index:10;background:rgba(13,17,23,.86);backdrop-filter:blur(10px);border-bottom:1px solid var(--line)}
.bar{display:flex;align-items:center;gap:10px;padding:12px 0}
.logo{display:flex;align-items:center;gap:10px;font-weight:800;font-size:20px;color:var(--txt)}
.mark{width:32px;height:32px;border-radius:9px;background:linear-gradient(135deg,var(--acc2),var(--acc));display:inline-grid;place-items:center;color:#fff;font-size:15px}
.back{display:inline-block;margin:18px 0 12px;color:var(--mut);font-size:14px}
.back:hover{color:var(--acc)}
.panel{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:26px;margin:0 0 40px}
.panel h2{margin:0;font-size:22px}
.panel label{display:block;font-size:14px;font-weight:600;margin-top:16px}
.panel input{display:block;width:100%;padding:12px 14px;margin-top:6px;border-radius:10px;border:1px solid var(--line);background:#0f1620;color:var(--txt);font-size:15px;outline:none}
.panel input:focus{border-color:var(--acc)}
.panel .btn{width:100%;margin-top:22px;padding:13px;display:inline-block;background:linear-gradient(135deg,var(--acc2),var(--acc));color:#fff;font-weight:700;border-radius:12px;border:0;cursor:pointer;font-size:16px}
.panel .btn:hover{filter:brightness(1.12)}
</style>
</head><body><header class="top"><div class="wrap bar"><a class="logo" href="/"><span class="mark">▶</span>Tiny Tube</a></div></header><div class="wrap narrow"><a class="back" href="/watch?id={{.ID}}">← πίσω στο βίντεο</a><div class="panel"><h2>Επεξεργασία</h2><form method="POST" action="/api/edit"><input type="hidden" name="id" value="{{.ID}}"><label>Τίτλος<input name="title" value="{{.Title}}" required></label><label>Περιγραφή<input name="desc" value="{{.Desc}}"></label><label>Κατηγορία<input name="category" value="{{.Category}}"></label><p><button class="btn">Αποθήκευση</button></p></form></div></div></body></html>`))

func main() {
	loadDotEnv(".env") // optional local config; explicit env vars win
	videoDir = getenv("VIDEO_DIR", "videos")
	// Default bind is loopback-only. Override with ADDR, or with PORT (kept for compat).
	addr := getenv("ADDR", "")
	if addr == "" {
		addr = "127.0.0.1:" + getenv("PORT", "8081")
	}
	_ = os.MkdirAll(videoDir, 0o755)
	// Metadata backend: sqlite (default) or legacy fs (meta.json per video).
	// Video blobs always stay on disk under VIDEO_DIR/{id}/ either way.
	switch getenv("STORE", "sqlite") {
	case "fs":
		fs = store.NewFS(videoDir)
	case "sqlite", "":
		dbPath := getenv("DB_PATH", filepath.Join(videoDir, "tiny-tube.db"))
		sq, err := store.NewSQLite(videoDir, dbPath)
		if err != nil {
			log.Fatalf("sqlite %s: %v", dbPath, err)
		}
		if n, err := store.ImportFS(sq, videoDir); err != nil {
			log.Printf("import meta.json: %v", err)
		} else if n > 0 {
			log.Printf("imported %d video(s) from meta.json", n)
		}
		log.Printf("sqlite: %s", dbPath)
		fs = sq
	default:
		log.Fatalf("unknown STORE=%q (want sqlite|fs)", getenv("STORE", ""))
	}

	log.Printf("ffmpeg available: %v", ffmpegAvailable())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", handleList)
	mux.HandleFunc("GET /watch", handleWatch)
	// upload/edit/delete pages + apis gated by BasicAuth (viewers open, management closed)
	mux.Handle("GET /upload", uploadAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		uploadTmpl.Execute(w, nil)
	})))
	mux.Handle("POST /api/upload", uploadAuth(http.HandlerFunc(handleUpload)))
	mux.Handle("GET /edit", uploadAuth(http.HandlerFunc(handleEditPage)))
	mux.Handle("POST /api/edit", uploadAuth(http.HandlerFunc(handleEdit)))
	mux.Handle("POST /api/delete", uploadAuth(http.HandlerFunc(handleDelete)))

	// static videos: Go-only mode. Behind Nginx, Nginx serves /videos/ directly.
	fsHandler := http.StripPrefix("/videos/", http.FileServer(http.Dir(videoDir)))
	mux.Handle("GET /videos/", fsHandler)

	log.Printf("Tiny Tube on %s VIDEO_DIR=%s", addr, videoDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	items, _ := fs.List(q)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	listTmpl.Execute(w, map[string]any{"Items": items, "Q": q})
}

func handleWatch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	m, err := fs.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	watchTmpl.Execute(w, m)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(2 << 30); err != nil { // 2GB max
		http.Error(w, "parse: "+err.Error(), 400)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title required", 400)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file: "+err.Error(), 400)
		return
	}
	defer f.Close()

	id := fmt.Sprintf("%d", time.Now().Unix())
	// sanitize ext
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".mp4" && ext != ".mov" && ext != ".mkv" && ext != ".webm" {
		ext = ".mp4"
	}
	orig := "original" + ext
	_ = os.MkdirAll(filepath.Join(videoDir, id), 0o755)
	dst, err := os.Create(filepath.Join(videoDir, id, orig))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := io.Copy(dst, f); err != nil {
		dst.Close()
		http.Error(w, err.Error(), 500)
		return
	}
	dst.Close()

	m := store.Meta{ID: id, Title: title, Desc: r.FormValue("desc"), Category: r.FormValue("category"), Original: orig, Status: "ready"}
	if ffmpegAvailable() {
		m.Status = "processing" // HLS async
	}
	_ = fs.Put(m)
	if m.Status == "processing" {
		go transcodeToHLS(videoDir, id, orig)
	}
	http.Redirect(w, r, "/watch?id="+id, http.StatusSeeOther)
}

func handleEditPage(w http.ResponseWriter, r *http.Request) {
	m, err := fs.Get(r.URL.Query().Get("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	editTmpl.Execute(w, m)
}

func handleEdit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse: "+err.Error(), 400)
		return
	}
	id := r.FormValue("id")
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title required", 400)
		return
	}
	m, err := fs.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m.Title = title
	m.Desc = r.FormValue("desc")
	m.Category = r.FormValue("category")
	if err := fs.Put(m); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/watch?id="+id, http.StatusSeeOther)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse: "+err.Error(), 400)
		return
	}
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "id required", 400)
		return
	}
	if _, err := fs.Get(id); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := fs.Delete(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
