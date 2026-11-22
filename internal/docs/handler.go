package docs

import (
	"embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
)

//go:embed assets/*
var embedded embed.FS

var (
	staticFiles fs.FS
	specOnce    sync.Once
	specJSON    []byte
	specErr     error
)

func init() {
	var err error
	staticFiles, err = fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
}

// RegisterRoutes adds the documentation-only routes to the existing API mux.
// It does not change the /v1/call action gateway.
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /docs", serveIndex)
	mux.HandleFunc("GET /docs/assets/{name}", serveAsset)
	mux.HandleFunc("GET /openapi.json", serveOpenAPI)
}

func serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	body, err := fs.ReadFile(staticFiles, "index.html")
	if err != nil {
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.PathValue("name"))
	if name == "." || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	body, err := fs.ReadFile(staticFiles, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if strings.HasPrefix(name, "scalar-") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	_, _ = w.Write(body)
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	body, err := OpenAPIJSON()
	if err != nil {
		http.Error(w, "OpenAPI specification unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// OpenAPIJSON returns the same generated description served at /openapi.json.
func OpenAPIJSON() ([]byte, error) {
	specOnce.Do(func() {
		specJSON, specErr = json.MarshalIndent(Specification(), "", "  ")
	})
	return append([]byte(nil), specJSON...), specErr
}
