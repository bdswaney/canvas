// Package server assembles the HTTP surface: the API routes, the
// collaboration socket, and the single-page app that everything else is
// served alongside. It owns the route table and nothing else — handlers live
// in internal/api, the socket in internal/relay.
package server

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/bdswaney/canvas/internal/api"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/relay"
)

// New builds the whole HTTP handler: the API behind session middleware, the
// collaboration socket, and the SPA fallback for everything else. assets is
// the built frontend; it must contain index.html.
func New(assets fs.FS, h *relay.Hub, originPatterns []string, authn auth.Authenticator) (http.Handler, error) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read frontend entry point: %w", err)
	}

	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Route("/api", func(r chi.Router) {
		// Every API route needs the session cookie read and an XSRF token
		// issued; SetXSRFToken depends on StartSession having run.
		r.Use(authn.StartSession)
		r.Use(authn.SetXSRFToken)

		r.Route("/session", func(r chi.Router) {
			// Login cannot sit behind ValidateSession: there is no session
			// yet. GET reports who you are and doubles as the call that
			// primes the XSRF cookie and keeps a session alive while someone
			// is editing over a WebSocket and making no other requests.
			r.Get("/", authn.Authenticated())
			r.Post("/", authn.Login())
			// Signing out changes state, so it carries the XSRF check that
			// every future write endpoint will use.
			r.With(authn.ValidateSession, authn.ValidateXSRFToken).Delete("/", authn.Logout())
		})

		r.Group(func(r chi.Router) {
			r.Use(authn.ValidateSession)
			r.Use(authn.ValidateXSRFToken)
			api.Mount(r, h.Store(), authn)
		})

		// Collaboration sockets: authenticated, but no XSRF check. A browser
		// cannot set headers on a WebSocket handshake. What protects this is
		// the session cookie being SameSite=Strict, so a cross-site handshake
		// carries no cookie at all, with the Origin check behind it.
		r.Get("/sync/doc/{docID}", h.Handler(originPatterns, authn))
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
