package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bdswaney/canvas/internal/auth"
	mcpserver "github.com/bdswaney/canvas/internal/mcp"
	"github.com/bdswaney/canvas/internal/migrate"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/server"
	"github.com/bdswaney/canvas/internal/store"
	"github.com/bdswaney/canvas/internal/ydoc"
)

// Tunnels terminate TLS and present their own Host, so their origins have to
// be allowed explicitly. These wildcards accept any tunnel anybody can create
// on those services, which is fine while developing behind one and is not
// something to deploy: set ORIGINS, which replaces them entirely.
var defaultOrigins = []string{"*.ngrok-free.dev", "*.ngrok-free.app", "*.ngrok.app", "*.ngrok.io"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Some subcommands write output meant to be read by something other than
	// a person: mcp speaks a protocol on stdout, and token prints a secret to
	// be captured. Nothing else may write there. Take the real handle now and
	// point os.Stdout at stderr for the rest of startup — the session package
	// prints a generated cookie key straight to stdout, which would corrupt
	// the first protocol message or land in a file meant to hold a token, and
	// any library that does the same in future is covered too.
	protocol := os.Stdout
	if len(os.Args) > 1 && (os.Args[1] == "mcp" || os.Args[1] == "token") {
		os.Stdout = os.Stderr
	}

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

	// The CRDT engine lets the server read and edit documents rather than only
	// relay them: it folds a journal into one update once nobody is editing,
	// and backs the MCP subcommand below.
	engine, err := ydoc.New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close(context.Background())

	// canvas createuser <username> <password> makes the first account, which
	// cannot come through the API because that route requires a session.
	if len(os.Args) > 1 {
		if err := runCommand(ctx, authn, db, engine, protocol, os.Args[1:]); err != nil {
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
	// One hub for the whole process: the relay broadcasts through it and the
	// MCP handler injects through it, which is what makes an assistant's edit
	// visible to somebody already editing.
	hub := relay.NewHub(db, engine)

	// A per-request server, because MCP here acts as whoever presented the
	// token. RequireToken has already put that account in the context.
	mcpHandler := authn.RequireToken(mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			user, _ := authn.UserFromCtx(r.Context())
			return mcpserver.New(db, engine, hub, user)
		}, nil))

	handler, err := server.New(assets, hub, originPatterns, authn, mcpHandler)
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
func runCommand(ctx context.Context, authn *auth.PasswordAuth, db *store.PostgresStore, engine *ydoc.Engine, protocol *os.File, args []string) error {
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
	case "token":
		// Tokens are what a client that is not a browser can hold. The secret
		// is shown once, here, and only its hash is stored.
		if len(args) < 2 {
			return errors.New("usage: canvas token <create|list|revoke> ...")
		}
		switch args[1] {
		case "create":
			if len(args) != 4 {
				return errors.New("usage: canvas token create <username> <name>")
			}
			secret, token, err := authn.CreateToken(ctx, args[2], args[3], 0)
			if err != nil {
				return err
			}
			log.Printf("Created token %s (%s) for %s", token.Name, token.ID, args[2])
			log.Print("This is the only time it is shown; store it now:")
			fmt.Fprintln(protocol, secret)
			return nil
		case "list":
			if len(args) != 3 {
				return errors.New("usage: canvas token list <username>")
			}
			tokens, err := authn.Tokens(ctx, args[2])
			if err != nil {
				return err
			}
			for _, token := range tokens {
				state := "active"
				switch {
				case token.RevokedAt != nil:
					state = "revoked"
				case !token.Active():
					state = "expired"
				}
				used := "never used"
				if token.LastUsedAt != nil {
					used = "last used " + token.LastUsedAt.Format(time.RFC3339)
				}
				fmt.Fprintf(protocol, "%s\t%s…\t%s\t%s\t%s\n",
					token.ID, token.Prefix, token.Name, state, used)
			}
			return nil
		case "revoke":
			if len(args) != 3 {
				return errors.New("usage: canvas token revoke <token id>")
			}
			if err := authn.RevokeToken(ctx, args[2]); err != nil {
				return err
			}
			log.Printf("Revoked token %s", args[2])
			return nil
		default:
			return fmt.Errorf("unknown token command %q", args[1])
		}
	case "mcp":
		// Speaks the Model Context Protocol over stdin and stdout, so an
		// assistant can work with this person's documents.
		//
		// The account is named on the command line rather than authenticated,
		// because anyone who can run this already holds DATABASE_URL and could
		// read the tables directly. What the name buys is that every request
		// goes through the same membership checks as the web API rather than
		// around them. A network transport would need a real credential; see
		// the token discussion on the issue.
		//
		// This hub is this process's own, and the web server has another. An
		// edit made here is journalled and durable, but nobody already
		// connected to the web server will see it: Inject can only broadcast
		// to sessions in its own process, and a client that still holds the
		// older document may write over it. Live propagation needs the MCP
		// server inside the web server, which needs the token work.
		if len(args) != 2 {
			return errors.New("usage: canvas mcp <username>")
		}
		user, err := authn.UserByUsername(ctx, args[1])
		if err != nil {
			return err
		}
		return mcpserver.New(db, engine, relay.NewHub(db, engine), user).
			Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: protocol})
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
