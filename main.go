package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/bdswaney/canvas/internal/auth"
	"github.com/bdswaney/canvas/internal/migrate"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/server"
	"github.com/bdswaney/canvas/internal/store"
	"github.com/bdswaney/canvas/internal/ydoc"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Tunnels terminate TLS and present their own Host, so their origins have to
// be allowed explicitly. These wildcards accept any tunnel anybody can create
// on those services, which is fine while developing behind one and is not
// something to deploy: set ORIGINS, which replaces them entirely.
var defaultOrigins = []string{"*.ngrok-free.dev", "*.ngrok-free.app", "*.ngrok.app", "*.ngrok.io"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required: accounts and document history live in Postgres")
	}
	if err := migrate.Run(databaseURL); err != nil {
		log.Fatal(err)
	}

	connect, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	db, err := store.NewPostgresStore(connect, databaseURL)
	cancelConnect()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	authn, err := auth.NewPasswordAuth(db.Pool(), os.Getenv("COOKIE_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	// canvas createuser <username> <password> makes the first account, which
	// cannot come through the API because that route requires a session.
	if len(os.Args) > 1 {
		if err := runCommand(ctx, authn, os.Args[1:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	// The CRDT engine lets the server fold a document's journal into a single
	// update once nobody is editing it. Without it the relay still works;
	// journals just grow without bound, as they always have.
	engine, err := ydoc.New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close(context.Background())

	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		log.Fatal(err)
	}
	originPatterns, err := origins()
	if err != nil {
		log.Fatal(err)
	}
	handler, err := server.New(assets, relay.NewHub(db, engine), originPatterns, authn)
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
func runCommand(ctx context.Context, authn *auth.PasswordAuth, args []string) error {
	switch args[0] {
	case "migrate":
		// Migrations already ran before this switch; nothing left to do.
		log.Print("Migrations are up to date")
		return nil
	case "createuser":
		if len(args) != 3 {
			return errors.New("usage: canvas createuser <username> <password>")
		}
		if err := authn.CreateUser(ctx, args[1], args[2]); err != nil {
			return err
		}
		log.Printf("Created user %s", args[1])
		return nil
	case "deleteuser":
		if len(args) != 2 {
			return errors.New("usage: canvas deleteuser <username>")
		}
		if err := authn.DeleteUser(ctx, args[1]); err != nil {
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
