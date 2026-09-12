package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bdswaney/canvas/internal/auth/authtest"
	"github.com/bdswaney/canvas/internal/relay"
	"github.com/bdswaney/canvas/internal/server"
	"github.com/bdswaney/canvas/internal/store"
)

func TestCLIValidationDoesNotStartRuntime(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "root help", args: []string{"--help"}, want: "Usage:"},
		{name: "token help", args: []string{"token", "--help"}, want: "Usage:"},
		{name: "empty token group", args: []string{"token"}, want: "Manage access tokens"},
		{name: "help command", args: []string{"help", "token", "create"}, want: "Create an access token"},
		{name: "migrate help", args: []string{"migrate", "--help"}, want: "Apply pending"},
		{name: "createuser help", args: []string{"createuser", "--help"}, want: "Create a user account"},
		{name: "deleteuser help", args: []string{"deleteuser", "--help"}, want: "Delete a user account"},
		{name: "mcp help", args: []string{"mcp", "--help"}, want: "Serve nPly"},
		{name: "token create help", args: []string{"token", "create", "--help"}, want: "Create an access token"},
		{name: "token list help", args: []string{"token", "list", "--help"}, want: "List a user's"},
		{name: "token revoke help", args: []string{"token", "revoke", "--help"}, want: "Revoke an access token"},
		{name: "missing argument", args: []string{"createuser", "alice"}, want: "accepts 2 arg(s)", wantErr: true},
		{name: "nested missing argument", args: []string{"token", "create", "alice"}, want: "accepts 2 arg(s)", wantErr: true},
		{name: "extra argument", args: []string{"deleteuser", "alice", "extra"}, want: "accepts 1 arg(s)", wantErr: true},
		{name: "token list missing username", args: []string{"token", "list"}, want: "accepts 1 arg(s)", wantErr: true},
		{name: "token revoke extra argument", args: []string{"token", "revoke", "id", "extra"}, want: "accepts 1 arg(s)", wantErr: true},
		{name: "mcp missing username", args: []string{"mcp"}, want: "accepts 1 arg(s)", wantErr: true},
		{name: "migrate extra argument", args: []string{"migrate", "extra"}, want: "unknown command", wantErr: true},
		{name: "unknown command", args: []string{"not-a-command"}, want: "unknown command", wantErr: true},
		{name: "unknown token command", args: []string{"token", "unknown"}, want: "unknown command", wantErr: true},
		{name: "unknown flag", args: []string{"--unknown"}, want: "unknown flag", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			started := false
			factory := func(context.Context, runtimeOptions) (*commandRuntime, error) {
				started = true
				return nil, errors.New("runtime should not start")
			}
			originalStdout := os.Stdout
			err := executeCLI(context.Background(), tt.args, &output, factory)
			if started {
				t.Fatal("runtime factory was called before command could complete")
			}
			if os.Stdout != originalStdout {
				t.Fatal("executeCLI did not restore stdout")
			}
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error %v does not contain %q", err, tt.want)
				}
				if output.Len() != 0 {
					t.Fatalf("validation error wrote to stdout: %q", output.String())
				}
			} else if err != nil || !strings.Contains(output.String(), tt.want) {
				t.Fatalf("help output=%q, error=%v; want %q", output.String(), err, tt.want)
			}
		})
	}
}

// These tests change process-wide streams and must not run in parallel.
func captureCLIStderr(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = original
		file.Close()
	})
	return file
}

func TestCLIStartupKeepsDiagnosticsOffStdout(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("COOKIE_KEY", "")
	t.Setenv("CANVAS_ENV", "development")
	tests := []struct {
		name       string
		args       []string
		needEngine bool
	}{
		{name: "server", needEngine: true},
		{name: "createuser", args: []string{"createuser", "alice", "password"}},
		{name: "deleteuser", args: []string{"deleteuser", "alice"}},
		{name: "token create", args: []string{"token", "create", "alice", "laptop"}},
		{name: "token list", args: []string{"token", "list", "alice"}},
		{name: "token revoke", args: []string{"token", "revoke", "id"}},
		{name: "mcp", args: []string{"mcp", "alice"}, needEngine: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stderr := captureCLIStderr(t)
			originalStdout := os.Stdout
			var protocol bytes.Buffer
			stop := errors.New("stop before database operations")
			calls := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := executeCLI(ctx, tt.args, &protocol, func(gotCtx context.Context, options runtimeOptions) (*commandRuntime, error) {
				calls++
				if gotCtx != ctx || options.needEngine != tt.needEngine {
					t.Errorf("incorrect runtime context or engine requirement: needEngine=%v", options.needEngine)
				}
				if tt.name == "server" && options.cookieKey == "" {
					t.Error("browser runtime received an empty cookie key")
				}
				if tt.name != "server" && options.cookieKey != "" {
					t.Error("non-browser runtime received a browser cookie key")
				}
				// Simulate the session dependency's direct stdout diagnostic.
				fmt.Fprintln(os.Stdout, "startup diagnostic")
				return nil, stop
			})
			if !errors.Is(err, stop) || calls != 1 {
				t.Fatalf("startup calls=%d, error=%v", calls, err)
			}
			if os.Stdout != originalStdout {
				t.Fatal("executeCLI did not restore stdout after startup failure")
			}
			if protocol.Len() != 0 {
				t.Fatalf("startup contaminated protocol output: %q", protocol.String())
			}
			contents, err := os.ReadFile(stderr.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contents), "startup diagnostic") {
				t.Fatalf("startup diagnostic missing from stderr: %q", contents)
			}
		})
	}
}

func captureCLIStreams(t *testing.T) (stdout, stderr *os.File) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err = os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	t.Cleanup(func() {
		os.Stdout, os.Stderr = originalStdout, originalStderr
		stdout.Close()
		stderr.Close()
	})
	return stdout, stderr
}

func TestCookieKeyPolicy(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, cookieKeyBytes))
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, cookieKeyBytes-1))
	tests := []struct {
		name       string
		browser    bool
		env        string
		configured string
		want       string
		wantErr    bool
		wantBytes  int
	}{
		{name: "browser missing unset", browser: true, wantErr: true},
		{name: "browser missing production", browser: true, env: "production", wantErr: true},
		{name: "browser missing unknown", browser: true, env: "staging", wantErr: true},
		{name: "browser development generates", browser: true, env: "development", wantBytes: cookieKeyBytes},
		{name: "browser malformed", browser: true, env: "development", configured: "not-base64", wantErr: true},
		{name: "browser short", browser: true, env: "development", configured: short, wantErr: true},
		{name: "browser configured outside development", browser: true, env: "production", configured: valid, want: valid},
		{name: "non-browser missing", env: "production", wantBytes: cookieKeyBytes},
		{name: "non-browser malformed", env: "production", configured: "not-base64", wantBytes: cookieKeyBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CANVAS_ENV", tt.env)
			t.Setenv("COOKIE_KEY", tt.configured)
			got, err := resolveCookieKey(tt.browser)
			if tt.wantErr {
				if err == nil {
					t.Fatal("resolveCookieKey succeeded; want error")
				}
				if tt.configured != "" && strings.Contains(err.Error(), tt.configured) {
					t.Fatalf("error leaked configured key: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && got != tt.want {
				t.Fatalf("key=%q, want configured key", got)
			}
			decoded, err := base64.StdEncoding.DecodeString(got)
			if err != nil || len(decoded) < tt.wantBytes {
				t.Fatalf("generated key is not at least %d decoded bytes: %q (%v)", tt.wantBytes, got, err)
			}
		})
	}
}

func TestConfiguredCookieKeyRemainsStable(t *testing.T) {
	configured := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, cookieKeyBytes))
	t.Setenv("CANVAS_ENV", "production")
	t.Setenv("COOKIE_KEY", configured)
	first, err := resolveCookieKey(true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveCookieKey(true)
	if err != nil {
		t.Fatal(err)
	}
	if first != configured || second != configured || first != second {
		t.Fatalf("resolved keys changed: first=%q second=%q", first, second)
	}
}

func TestGeneratedCookieKeyIsNeverLogged(t *testing.T) {
	t.Setenv("CANVAS_ENV", "development")
	t.Setenv("COOKIE_KEY", "")
	stdout, stderr := captureCLIStreams(t)
	key, err := resolveCookieKey(true)
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]*os.File{"stdout": stdout, "stderr": stderr} {
		contents, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), key) || strings.Contains(string(contents), "Using random Key") {
			t.Fatalf("generated key leaked to %s: %q", name, contents)
		}
	}
}

func TestBrowserServerRejectsMissingCookieKeyBeforeRuntime(t *testing.T) {
	for _, env := range []string{"", "production", "staging"} {
		t.Run("env="+env, func(t *testing.T) {
			t.Setenv("CANVAS_ENV", env)
			t.Setenv("COOKIE_KEY", "")
			called := false
			err := executeCLI(t.Context(), nil, new(bytes.Buffer), func(context.Context, runtimeOptions) (*commandRuntime, error) {
				called = true
				return nil, errors.New("runtime should not start")
			})
			if called {
				t.Fatal("browser runtime started without a cookie key")
			}
			if err == nil || !strings.Contains(err.Error(), "COOKIE_KEY is required") {
				t.Fatalf("error=%v, want missing-key error", err)
			}
		})
	}
}

func TestNonBrowserCommandIgnoresMalformedCookieKey(t *testing.T) {
	t.Setenv("CANVAS_ENV", "production")
	t.Setenv("COOKIE_KEY", "not-base64")
	stop := errors.New("stop before database operations")
	called := false
	err := executeCLI(t.Context(), []string{"createuser", "alice", "password"}, new(bytes.Buffer), func(context.Context, runtimeOptions) (*commandRuntime, error) {
		called = true
		return nil, stop
	})
	if !called || !errors.Is(err, stop) {
		t.Fatalf("runtime called=%v, error=%v; want non-browser startup", called, err)
	}
}

func TestCLICompletionWritesToStdoutWithoutRuntime(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			stderr := captureCLIStderr(t)
			originalStdout := os.Stdout
			var output bytes.Buffer
			err := executeCLI(t.Context(), []string{"completion", shell}, &output, func(context.Context, runtimeOptions) (*commandRuntime, error) {
				t.Error("completion started the runtime")
				return nil, errors.New("unexpected startup")
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "canvas") {
				t.Fatalf("completion script missing from stdout: %q", output.String())
			}
			contents, err := os.ReadFile(stderr.Name())
			if err != nil {
				t.Fatal(err)
			}
			if len(contents) != 0 {
				t.Fatalf("unexpected completion stderr: %q", contents)
			}
			if os.Stdout != originalStdout {
				t.Fatal("executeCLI did not restore stdout after success")
			}
		})
	}
}

func TestEmbeddedFrontend(t *testing.T) {
	assets, err := fs.Sub(frontend, "dist")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.New(assets, relay.NewHub(store.NewMemoryStore(), nil), nil, authtest.Stub{Valid: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/artifacts/example", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("embedded app unavailable: status %d", w.Code)
	}
}
