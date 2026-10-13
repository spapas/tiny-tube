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
<style>body{font-family:system-ui;margin:0;background:#0f1419;color:#eee}header{background:#0a3d62;padding:12px 16px}main{padding:16px;max-width:1000px;margin:auto}.card{background:#1c2833;margin:10px 0;padding:12px;border-radius:8px}a{color:#4fc3f7}form{display:flex;gap:8px}input[type=text]{flex:1;padding:8px;border-radius:6px;border:0}.badge{font-size:12px;background:#444;padding:2px 8px;border-radius:10px}</style>
</head><body><header><b>Tiny Tube</b> &nbsp; <a href="/upload">upload</a></header><main>
<form method="GET" action="/"><input type="text" name="q" placeholder="αναζήτηση..." value="{{.Q}}"><button>OK</button></form>
{{range .Items}}<div class="card"><a href="/watch?id={{.ID}}"><b>{{.Title}}</b></a> <span class="badge">{{.Category}}</span> <span class="badge">{{.Status}}</span><br><small>{{.Desc}}</small></div>{{else}}<p>Κανένα βίντεο ακόμα. Ανέβασε από /upload</p>{{end}}
</main></body></html>`))

var watchTmpl = template.Must(template.New("watch").Parse(`<!doctype html><html lang="el"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} - Tiny Tube</title>
<script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
<style>body{font-family:system-ui;margin:0;background:#0f1419;color:#eee}main{max-width:900px;margin:auto;padding:16px}video{width:100%;background:#000;border-radius:8px}a{color:#4fc3f7}</style>
</head><body><main><a href="/">← πίσω</a><h2>{{.Title}}</h2><p>{{.Desc}}</p>
<video id="v" controls playsinline></video>
<script>
const id="{{.ID}}", hls="{{.HLS}}", orig="{{.Original}}";
const v=document.getElementById("v");
if(hls){ const url="/videos/"+id+"/"+hls;
  if(v.canPlayType("application/vnd.apple.mpegurl")){ v.src=url; }
  else if(window.Hls && Hls.isSupported()){ const h=new Hls(); h.loadSource(url); h.attachMedia(v); }
  else { v.src="/videos/"+id+"/"+orig; }
} else { v.src="/videos/"+id+"/"+orig; }
</script>
<p><small>Αν πας στο τέλος και κολλάει: λείπει faststart/HLS ακόμα (status={{.Status}}). Περίμενε το transcode.</small></p>
</main></body></html>`))

var uploadTmpl = template.Must(template.New("upload").Parse(`<!doctype html><html lang="el"><head><meta charset="utf-8"><title>upload - Tiny Tube</title></head><body style="font-family:system-ui;max-width:600px;margin:auto;padding:16px">
<h2>Upload (mp4 ~1GB ok)</h2><form method="POST" action="/api/upload" enctype="multipart/form-data">
<p>Τίτλος: <input name="title" required></p><p>Περιγραφή: <input name="desc"></p><p>Κατηγορία: <input name="category" placeholder="πχ ΔΙΕΥΘΥΝΣΗ"></p>
<p>Αρχείο: <input type="file" name="file" accept="video/mp4,video/*" required></p><p><button>Ανέβασμα</button></p></form>
<p><a href="/">← λίστα</a></p></body></html>`))

func main() {
	videoDir = getenv("VIDEO_DIR", "videos")
	port := getenv("PORT", "8080")
	_ = os.MkdirAll(videoDir, 0o755)
	fs = store.NewFS(videoDir)

	log.Printf("ffmpeg available: %v", ffmpegAvailable())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", handleList)
	mux.HandleFunc("GET /watch", handleWatch)
	// upload page + api gated by BasicAuth (viewers open, upload closed)
	mux.Handle("GET /upload", uploadAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadTmpl.Execute(w, nil)
	})))
	mux.Handle("POST /api/upload", uploadAuth(http.HandlerFunc(handleUpload)))

	// static videos: Go-only mode. Behind Nginx, Nginx serves /videos/ directly.
	fsHandler := http.StripPrefix("/videos/", http.FileServer(http.Dir(videoDir)))
	mux.Handle("GET /videos/", fsHandler)

	log.Printf("Tiny Tube on :%s VIDEO_DIR=%s", port, videoDir)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	items, _ := fs.List(q)
	listTmpl.Execute(w, map[string]any{"Items": items, "Q": q})
}

func handleWatch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	m, err := fs.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
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
