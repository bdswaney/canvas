package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Build the frontend with mise run build before compiling Go.
//
//go:embed dist
var frontend embed.FS

// Tunnels terminate TLS and present their own Host, so their origins have to
// be allowed explicitly. Override with ORIGINS (comma separated).
var defaultOrigins = []string{"*.ngrok-free.dev", "*.ngrok-free.app", "*.ngrok.app", "*.ngrok.io"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openStore(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		log.Fatal(err)
	}
	handler, err := newHandler(assets, newHub(store), origins())
	if err != nil {
		log.Fatal(err)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Collaboration sockets are long lived; do not wait them out.
		server.Shutdown(shutdown)
	}()

	log.Printf("Canvas listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// openStore uses Postgres when DATABASE_URL is set and otherwise keeps the
// update log in memory, which is fine for a throwaway development run.
func openStore(ctx context.Context) (Store, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Print("DATABASE_URL is not set; keeping room history in memory only")
		return NewMemoryStore(), nil
	}
	connect, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	store, err := NewPostgresStore(connect, url)
	if err != nil {
		return nil, err
	}
	log.Print("Room history persisted to Postgres")
	return store, nil
}

func origins() []string {
	value := os.Getenv("ORIGINS")
	if value == "" {
		return defaultOrigins
	}
	var patterns []string
	for _, pattern := range strings.Split(value, ",") {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func roomName(r *http.Request) string { return chi.URLParam(r, "room") }

func newHandler(assets fs.FS, h *hub, originPatterns []string) (http.Handler, error) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read frontend entry point: %w", err)
	}

	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Route("/api", func(api chi.Router) {
		api.Get("/sync/{room}", h.syncHandler(originPatterns))
	})
	// Unrouted paths are client-side routes, static files, or genuine 404s.
	spa := serveApp(assets, index)
	router.NotFound(spa)
	router.MethodNotAllowed(spa)
	return router, nil
}

func serveApp(assets fs.FS, index []byte) http.HandlerFunc {
	files := http.FileServer(http.FS(assets))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// Reserve API paths for backend handlers; never return the SPA here.
		if name == "api" || strings.HasPrefix(name, "api/") {
			http.NotFound(w, r)
			return
		}

		// Revalidate the entry point so deployments do not leave stale asset URLs.
		w.Header().Set("Cache-Control", "no-cache")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			// Missing files and asset directories are not client-side routes.
			if name == "assets" || strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	}
}
