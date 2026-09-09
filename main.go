package main

import (
	"bytes"
	"context"
	"embed"
	"errors"
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
// be allowed explicitly. These wildcards accept any tunnel anybody can create
// on those services, which is fine while developing behind one and is not
// something to deploy: set ORIGINS, which replaces them entirely.
var defaultOrigins = []string{"*.ngrok-free.dev", "*.ngrok-free.app", "*.ngrok.app", "*.ngrok.io"}

// Session rows are timestamp-without-time-zone: the session store writes
// wall-clock local time and reads it back as UTC. Anywhere but UTC that skew
// makes every session look hours old and instantly expired. This is an init so
// the test binary is pinned too, not only the server.
func init() { time.Local = time.UTC }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required: accounts and document history live in Postgres")
	}
	if err := migrateDatabase(databaseURL); err != nil {
		log.Fatal(err)
	}

	connect, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	store, err := NewPostgresStore(connect, databaseURL)
	cancelConnect()
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	auth, err := newPasswordAuth(store.Pool(), os.Getenv("COOKIE_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	// canvas createuser <username> <password> makes the first account, which
	// cannot come through the API because that route requires a session.
	if len(os.Args) > 1 {
		if err := runCommand(ctx, auth, os.Args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		log.Fatal(err)
	}
	originPatterns, err := origins()
	if err != nil {
		log.Fatal(err)
	}
	handler, err := newHandler(assets, newHub(store), originPatterns, auth)
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

// runCommand handles the few one-shot administrative commands. Anything that
// needs a running server does not belong here.
func runCommand(ctx context.Context, auth *passwordAuth, args []string) error {
	switch args[0] {
	case "migrate":
		// Migrations already ran before this switch; nothing left to do.
		log.Print("Migrations are up to date")
		return nil
	case "createuser":
		if len(args) != 3 {
			return errors.New("usage: canvas createuser <username> <password>")
		}
		if err := auth.createUser(ctx, args[1], args[2]); err != nil {
			return err
		}
		log.Printf("Created user %s", args[1])
		return nil
	case "deleteuser":
		if len(args) != 2 {
			return errors.New("usage: canvas deleteuser <username>")
		}
		if err := auth.deleteUser(ctx, args[1]); err != nil {
			return err
		}
		log.Printf("Deleted user %s", args[1])
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// origins returns the allowed WebSocket origins. ORIGINS is required unless
// CANVAS_ENV names a development environment: the wildcard fallback would
// otherwise let any tunnel on those hosts open a socket, which is a
// development convenience that must not reach a deployment.
func origins() ([]string, error) {
	value := os.Getenv("ORIGINS")
	if value == "" {
		if env := os.Getenv("CANVAS_ENV"); env != "" && env != "development" {
			return nil, fmt.Errorf("ORIGINS is required when CANVAS_ENV is %q: "+
				"the development fallback allows any tunnel host", env)
		}
		log.Printf("ORIGINS is unset; allowing tunnel hosts %v. Set ORIGINS before deploying.",
			defaultOrigins)
		return defaultOrigins, nil
	}
	var patterns []string
	for _, pattern := range strings.Split(value, ",") {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			patterns = append(patterns, pattern)
		}
	}
	if len(patterns) == 0 {
		return nil, errors.New("ORIGINS is set but lists no origins")
	}
	return patterns, nil
}

func docID(r *http.Request) string { return chi.URLParam(r, "docID") }

func newHandler(assets fs.FS, h *hub, originPatterns []string, auth authenticator) (http.Handler, error) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read frontend entry point: %w", err)
	}

	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Route("/api", func(api chi.Router) {
		// Every API route needs the session cookie read and an XSRF token
		// issued; SetXSRFToken depends on StartSession having run.
		api.Use(auth.StartSession)
		api.Use(auth.SetXSRFToken)

		api.Route("/session", func(r chi.Router) {
			// Login cannot sit behind ValidateSession: there is no session
			// yet. GET reports who you are and doubles as the call that
			// primes the XSRF cookie and keeps a session alive while someone
			// is editing over a WebSocket and making no other requests.
			r.Get("/", auth.Authenticated())
			r.Post("/", auth.Login())
			// Signing out changes state, so it carries the XSRF check that
			// every future write endpoint will use.
			r.With(auth.ValidateSession, auth.ValidateXSRFToken).Delete("/", auth.Logout())
		})

		api.Group(func(r chi.Router) {
			r.Use(auth.ValidateSession)
			r.Use(auth.ValidateXSRFToken)
			projects := &projectAPI{store: h.store, auth: auth}
			r.Route("/docs", (&docAPI{store: h.store, auth: auth}).routes)
			r.Route("/projects", projects.projectRoutes)
			r.Get("/users", projects.listUsers)
		})

		// Collaboration sockets: authenticated, but no XSRF check. A browser
		// cannot set headers on a WebSocket handshake. What protects this is
		// the session cookie being SameSite=Strict, so a cross-site handshake
		// carries no cookie at all, with the Origin check behind it.
		api.Get("/sync/doc/{docID}", h.syncHandler(originPatterns, auth))
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
