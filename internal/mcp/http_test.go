package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bdswaney/canvas/internal/auth"
)

type admissionIdentity struct{}

func admissionUser(ctx context.Context) (auth.User, bool) {
	id, _ := ctx.Value(admissionIdentity{}).(string)
	return auth.User{ID: id}, id != ""
}

func admissionRequest(id string) *http.Request {
	ctx := context.WithValue(context.Background(), admissionIdentity{}, id)
	return httptest.NewRequest(http.MethodPost, "/api/mcp", nil).WithContext(ctx)
}

func TestHTTPAdmissionBoundsConcurrentRequestsAndReleasesSlots(t *testing.T) {
	config := DefaultHTTPAdmissionConfig()
	config.GlobalConcurrency = 2
	config.AccountConcurrency = 1
	gate := NewHTTPAdmission(config)

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-release
	})
	handler := gate.ServeHTTP(admissionUser, next)

	doneA := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), admissionRequest("account-a"))
		close(doneA)
	}()
	<-started

	doneB := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), admissionRequest("account-b"))
		close(doneB)
	}()
	<-started

	blockedAccount := httptest.NewRecorder()
	handler.ServeHTTP(blockedAccount, admissionRequest("account-a"))
	if blockedAccount.Code != http.StatusTooManyRequests {
		t.Fatalf("same-account admission = %d, want 429", blockedAccount.Code)
	}
	if blockedAccount.Header().Get("Retry-After") == "" {
		t.Fatal("same-account refusal has no Retry-After")
	}

	blockedGlobal := httptest.NewRecorder()
	handler.ServeHTTP(blockedGlobal, admissionRequest("account-c"))
	if blockedGlobal.Code != http.StatusTooManyRequests {
		t.Fatalf("global admission = %d, want 429", blockedGlobal.Code)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("downstream calls after rejected requests = %d, want 2", got)
	}

	close(release)
	<-doneA
	<-doneB

	// Both global and account slots must be released on every normal return.
	after := httptest.NewRecorder()
	handler.ServeHTTP(after, admissionRequest("account-a"))
	if after.Code != http.StatusOK {
		t.Fatalf("admission after cleanup = %d, want 200", after.Code)
	}
}

func TestHTTPAdmissionReleasesSlotsAfterPanic(t *testing.T) {
	config := DefaultHTTPAdmissionConfig()
	config.GlobalConcurrency = 1
	config.AccountConcurrency = 1
	gate := NewHTTPAdmission(config)

	var calls atomic.Int32
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
		panic("test panic")
	})
	handler := gate.ServeHTTP(admissionUser, next)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected downstream panic")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), admissionRequest("account-a"))
	}()

	// A panic must not strand either slot. The handler is normally behind the
	// server Recoverer, but release is still the admission gate's responsibility.
	next = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	gate.ServeHTTP(admissionUser, next).ServeHTTP(httptest.NewRecorder(), admissionRequest("account-a"))
	if calls.Load() != 1 {
		t.Fatalf("panic handler calls = %d, want 1", calls.Load())
	}
}

func TestHTTPAdmissionRateLimitAndEviction(t *testing.T) {
	config := DefaultHTTPAdmissionConfig()
	config.AccountRatePerMinute = 1
	config.AccountRateBurst = 1
	config.MaxAccountEntries = 1
	gate := NewHTTPAdmission(config)

	var calls atomic.Int32
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	handler := gate.ServeHTTP(admissionUser, next)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, admissionRequest("account-a"))
	if first.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", first.Code)
	}
	limited := httptest.NewRecorder()
	handler.ServeHTTP(limited, admissionRequest("account-a"))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request = %d, want 429", limited.Code)
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited request has no Retry-After")
	}

	// A bounded metadata map evicts idle accounts rather than growing with the
	// number of authenticated account IDs.
	secondAccount := httptest.NewRecorder()
	handler.ServeHTTP(secondAccount, admissionRequest("account-b"))
	if secondAccount.Code != http.StatusOK {
		t.Fatalf("evicted account request = %d, want 200", secondAccount.Code)
	}
	if got := gate.accountCount(); got != 1 {
		t.Fatalf("account metadata entries = %d, want 1", got)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("downstream calls = %d, want 2", got)
	}
}

func TestHTTPAdmissionRejectsMissingAccountBeforeDownstream(t *testing.T) {
	gate := NewHTTPAdmission(DefaultHTTPAdmissionConfig())
	var calls atomic.Int32
	handler := gate.ServeHTTP(func(context.Context) (auth.User, bool) { return auth.User{}, false },
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/mcp", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing account = %d, want 401", response.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("downstream ran for missing account")
	}
}

func TestHTTPAdmissionLetsAuthenticatedNonPOSTReachStatelessSDK(t *testing.T) {
	config := DefaultHTTPAdmissionConfig()
	config.GlobalConcurrency = 1
	config.AccountConcurrency = 1
	config.GlobalRateBurst = 1
	config.AccountRateBurst = 1

	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	streamable := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return mcpServer
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true})
	verifier := func(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		if token != "valid-token" {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: "account-a"}, nil
	}
	userFromContext := func(ctx context.Context) (auth.User, bool) {
		info := mcpauth.TokenInfoFromContext(ctx)
		if info == nil || info.UserID == "" {
			return auth.User{}, false
		}
		return auth.User{ID: info.UserID}, true
	}
	admitted := NewHTTPAdmissionHandler(config, userFromContext, streamable)
	handler := mcpauth.RequireBearerToken(verifier, &mcpauth.RequireBearerTokenOptions{
		AllowMissingExpiration: true,
	})(admitted)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request := func(method, token, body string) *http.Response {
		t.Helper()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL, reader)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	unauthorized := request(http.MethodGet, "", "")
	if unauthorized.StatusCode != http.StatusUnauthorized {
		unauthorized.Body.Close()
		t.Fatalf("unauthenticated GET = %d, want 401", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()

	for i := 0; i < 3; i++ {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			response := request(method, "valid-token", "")
			if response.StatusCode != http.StatusMethodNotAllowed {
				response.Body.Close()
				t.Fatalf("authenticated %s = %d, want 405", method, response.StatusCode)
			}
			if got := response.Header.Get("Allow"); got != http.MethodPost {
				response.Body.Close()
				t.Fatalf("authenticated %s Allow = %q, want %q", method, got, http.MethodPost)
			}
			response.Body.Close()
		}
	}

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	validPost := request(http.MethodPost, "valid-token", initialize)
	if validPost.StatusCode != http.StatusOK {
		validPost.Body.Close()
		t.Fatalf("valid POST after non-POST requests = %d, want 200", validPost.StatusCode)
	}
	validPost.Body.Close()
}

func TestHTTPAdmissionRetryAfterIsAtLeastOneSecond(t *testing.T) {
	config := DefaultHTTPAdmissionConfig()
	config.AccountRatePerMinute = 600
	config.AccountRateBurst = 1
	gate := NewHTTPAdmission(config)
	handler := gate.ServeHTTP(admissionUser, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(), admissionRequest("account-a"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, admissionRequest("account-a"))
	if response.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", response.Header().Get("Retry-After"))
	}

	// Keep the test explicit about the unit used by the header.
	if retryAfterSeconds(1500*time.Millisecond) != 2 {
		t.Fatal("Retry-After should round a fractional second up")
	}
}
