package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

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

// commandRuntime contains only the services needed by a command. In
// particular, constructing the command tree does not connect to Postgres or
// compile the Wasm engine, so help and argument errors remain available when
// the application is not configured yet.
type commandRuntime struct {
	db     *store.PostgresStore
	authn  *auth.PasswordAuth
	engine *ydoc.Engine
}

func (r *commandRuntime) close() {
	if r.engine != nil {
		_ = r.engine.Close(context.Background())
	}
	if r.db != nil {
		r.db.Close()
	}
}

type runtimeOptions struct {
	needEngine bool
	cookieKey  string
}

type runtimeFactory func(context.Context, runtimeOptions) (*commandRuntime, error)

type protocolOutput struct{ io.Writer }

func (protocolOutput) Close() error { return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return executeCLI(ctx, os.Args[1:], os.Stdout, newRuntime)
}

// executeCLI retains the caller's stdout for help, completion, tokens, and MCP.
// The session dependency prints generated cookie keys directly to os.Stdout;
// route those diagnostics to stderr for every command and restore the stream
// on return. Like main, this process-wide boundary must execute serially.
func executeCLI(ctx context.Context, args []string, protocol io.Writer, factory runtimeFactory) error {
	originalStdout := os.Stdout
	defer func() { os.Stdout = originalStdout }()
	os.Stdout = os.Stderr

	root := newRootCommand(protocol, factory)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// newRuntime performs startup only for a command that actually needs it. The
// engine is optional because migrate, account administration, and token
// administration do not interpret documents. Browser startup resolves its key
// before calling this function; other commands get a disposable key because
// the auth package still needs one even though they do not serve cookies.
func newRuntime(ctx context.Context, options runtimeOptions) (*commandRuntime, error) {
	cookieKey := options.cookieKey
	if cookieKey == "" {
		var err error
		cookieKey, err = resolveCookieKey(false)
		if err != nil {
			return nil, err
		}
	}

	databaseURL, err := requiredDatabaseURL()
	if err != nil {
		return nil, err
	}
	if err := migrate.Run(databaseURL); err != nil {
		return nil, err
	}

	connect, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	db, err := store.NewPostgresStore(connect, databaseURL)
	cancelConnect()
	if err != nil {
		return nil, err
	}

	authn, err := auth.NewPasswordAuth(db.Pool(), cookieKey)
	if err != nil {
		db.Close()
		return nil, err
	}

	runtime := &commandRuntime{db: db, authn: authn}
	if options.needEngine {
		runtime.engine, err = ydoc.New(ctx)
		if err != nil {
			runtime.close()
			return nil, err
		}
	}
	return runtime, nil
}

func requiredDatabaseURL() (string, error) {
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		return databaseURL, nil
	}
	return "", errors.New("DATABASE_URL is required: accounts and document history live in Postgres")
}

func newRootCommand(protocol io.Writer, factory runtimeFactory) *cobra.Command {
	if factory == nil {
		factory = newRuntime
	}

	root := &cobra.Command{
		Use:           "canvas",
		Short:         "nPly collaborative Markdown workspace",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cookieKey, err := resolveCookieKey(true)
			if err != nil {
				return err
			}
			runtime, err := factory(cmd.Context(), runtimeOptions{
				needEngine: true,
				cookieKey:  cookieKey,
			})
			if err != nil {
				return err
			}
			defer runtime.close()
			return runServer(cmd.Context(), runtime)
		},
	}
	// Cobra's normal output includes help and completion scripts. Errors are
	// printed once by main, without dumping usage for runtime failures.
	root.SetOut(protocol)
	root.SetErr(os.Stderr)

	root.AddCommand(
		newMigrateCommand(),
		newCreateUserCommand(factory),
		newDeleteUserCommand(factory),
		newTokenCommand(factory, protocol),
		newMCPCommand(factory, protocol),
	)
	return root
}

func newMigrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			databaseURL, err := requiredDatabaseURL()
			if err != nil {
				return err
			}
			if err := migrate.Run(databaseURL); err != nil {
				return err
			}
			log.Print("Migrations are up to date")
			return nil
		},
	}
}

func newCreateUserCommand(factory runtimeFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "createuser <username> <password>",
		Short: "Create a user account",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := factory(cmd.Context(), runtimeOptions{})
			if err != nil {
				return err
			}
			defer runtime.close()
			if err := runtime.authn.CreateUser(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			log.Printf("Created user %s", args[0])
			return nil
		},
	}
}

func newDeleteUserCommand(factory runtimeFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "deleteuser <username>",
		Short: "Delete a user account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := factory(cmd.Context(), runtimeOptions{})
			if err != nil {
				return err
			}
			defer runtime.close()
			if err := runtime.authn.DeleteUser(cmd.Context(), args[0]); err != nil {
				return err
			}
			log.Printf("Deleted user %s", args[0])
			return nil
		},
	}
}

func newTokenCommand(factory runtimeFactory, protocol io.Writer) *cobra.Command {
	token := &cobra.Command{
		Use:   "token",
		Short: "Manage access tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	token.AddCommand(
		&cobra.Command{
			Use:   "create <username> <name>",
			Short: "Create an access token",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				runtime, err := factory(cmd.Context(), runtimeOptions{})
				if err != nil {
					return err
				}
				defer runtime.close()
				secret, token, err := runtime.authn.CreateToken(cmd.Context(), args[0], args[1], 0)
				if err != nil {
					return err
				}
				log.Printf("Created token %s (%s) for %s", token.Name, token.ID, args[0])
				log.Print("This is the only time it is shown; store it now:")
				_, err = fmt.Fprintln(protocol, secret)
				return err
			},
		},
		&cobra.Command{
			Use:   "list <username>",
			Short: "List a user's access tokens",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				runtime, err := factory(cmd.Context(), runtimeOptions{})
				if err != nil {
					return err
				}
				defer runtime.close()
				tokens, err := runtime.authn.Tokens(cmd.Context(), args[0])
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
					if _, err := fmt.Fprintf(protocol, "%s\t%s…\t%s\t%s\t%s\n",
						token.ID, token.Prefix, token.Name, state, used); err != nil {
						return err
					}
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "revoke <token id>",
			Short: "Revoke an access token",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				runtime, err := factory(cmd.Context(), runtimeOptions{})
				if err != nil {
					return err
				}
				defer runtime.close()
				if err := runtime.authn.RevokeToken(cmd.Context(), args[0]); err != nil {
					return err
				}
				log.Printf("Revoked token %s", args[0])
				return nil
			},
		},
	)
	return token
}

func newMCPCommand(factory runtimeFactory, protocol io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp <username>",
		Short: "Serve nPly over MCP on stdio",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime, err := factory(cmd.Context(), runtimeOptions{needEngine: true})
			if err != nil {
				return err
			}
			defer runtime.close()

			// The account is named on the command line rather than authenticated,
			// because anyone who can run this already holds DATABASE_URL and could
			// read the tables directly. What the name buys is that every request
			// goes through the same membership checks as the web API rather than
			// around them. A network transport would need a real credential.
			user, err := runtime.authn.UserByUsername(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return mcpserver.New(runtime.db, runtime.engine,
				relay.NewHub(runtime.db, runtime.engine), user).
				Run(cmd.Context(), &mcp.IOTransport{Reader: os.Stdin, Writer: protocolOutput{protocol}})
		},
	}
}

// runServer contains the long-lived web-server lifecycle. One hub for the
// whole process lets the relay and authenticated HTTP MCP handler broadcast
// through the same collaboration state.
func runServer(ctx context.Context, runtime *commandRuntime) error {
	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		return err
	}
	originPatterns, err := origins()
	if err != nil {
		return err
	}
	hub := relay.NewHub(runtime.db, runtime.engine)
	mcpHandler := runtime.authn.RequireToken(mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			user, _ := runtime.authn.UserFromCtx(r.Context())
			return mcpserver.New(runtime.db, runtime.engine, hub, user)
		}, nil))

	handler, err := server.New(assets, hub, originPatterns, runtime.authn, mcpHandler)
	if err != nil {
		return err
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	httpServer := &http.Server{
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
		_ = httpServer.Shutdown(shutdown)
	}()

	log.Printf("Canvas listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

const cookieKeyBytes = 32

// resolveCookieKey applies the browser-server cookie policy when browser is
// true. Non-browser commands intentionally tolerate an absent or malformed
// COOKIE_KEY: they need an auth object for database operations, not a stable
// browser session, so they receive a disposable key instead.
func resolveCookieKey(browser bool) (string, error) {
	configured := os.Getenv("COOKIE_KEY")
	if configured != "" {
		if validCookieKey(configured) {
			return configured, nil
		}
		if browser {
			return "", errors.New("COOKIE_KEY must be standard base64 containing at least 32 decoded bytes")
		}
	}

	if browser && os.Getenv("CANVAS_ENV") != "development" {
		return "", errors.New("COOKIE_KEY is required unless CANVAS_ENV=development")
	}

	key := make([]byte, cookieKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate ephemeral cookie key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func validCookieKey(value string) bool {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) >= cookieKeyBytes &&
		base64.StdEncoding.EncodeToString(decoded) == value
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
