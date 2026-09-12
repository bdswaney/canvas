package mcp

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bdswaney/canvas/internal/auth"
)

// HTTPAdmissionConfig bounds the work accepted by the stateless MCP HTTP
// endpoint. The limits are process-local: deployments with more than one
// server process need an external admission layer if they need a shared quota.
type HTTPAdmissionConfig struct {
	MaxRequestBodyBytes  int64
	GlobalConcurrency    int
	AccountConcurrency   int
	GlobalRatePerMinute  int
	GlobalRateBurst      int
	AccountRatePerMinute int
	AccountRateBurst     int
	MaxAccountEntries    int
}

const (
	DefaultMaxRequestBodyBytes  int64 = 4 << 20
	DefaultGlobalConcurrency          = 64
	DefaultAccountConcurrency         = 4
	DefaultGlobalRatePerMinute        = 600
	DefaultGlobalRateBurst            = 100
	DefaultAccountRatePerMinute       = 60
	DefaultAccountRateBurst           = 20
	DefaultMaxAccountEntries          = 1024
)

// DefaultHTTPAdmissionConfig returns the safe defaults for the MCP endpoint.
func DefaultHTTPAdmissionConfig() HTTPAdmissionConfig {
	return HTTPAdmissionConfig{
		MaxRequestBodyBytes:  DefaultMaxRequestBodyBytes,
		GlobalConcurrency:    DefaultGlobalConcurrency,
		AccountConcurrency:   DefaultAccountConcurrency,
		GlobalRatePerMinute:  DefaultGlobalRatePerMinute,
		GlobalRateBurst:      DefaultGlobalRateBurst,
		AccountRatePerMinute: DefaultAccountRatePerMinute,
		AccountRateBurst:     DefaultAccountRateBurst,
		MaxAccountEntries:    DefaultMaxAccountEntries,
	}
}

// normalize applies defaults to a config assembled by a caller. It is kept
// private so runtime configuration cannot accidentally enable an unbounded
// body or metadata map by omitting one field.
func (c HTTPAdmissionConfig) normalize() HTTPAdmissionConfig {
	defaults := DefaultHTTPAdmissionConfig()
	if c.MaxRequestBodyBytes <= 0 {
		c.MaxRequestBodyBytes = defaults.MaxRequestBodyBytes
	}
	if c.GlobalConcurrency <= 0 {
		c.GlobalConcurrency = defaults.GlobalConcurrency
	}
	if c.AccountConcurrency <= 0 {
		c.AccountConcurrency = defaults.AccountConcurrency
	}
	if c.GlobalRatePerMinute <= 0 {
		c.GlobalRatePerMinute = defaults.GlobalRatePerMinute
	}
	if c.GlobalRateBurst <= 0 {
		c.GlobalRateBurst = defaults.GlobalRateBurst
	}
	if c.AccountRatePerMinute <= 0 {
		c.AccountRatePerMinute = defaults.AccountRatePerMinute
	}
	if c.AccountRateBurst <= 0 {
		c.AccountRateBurst = defaults.AccountRateBurst
	}
	if c.MaxAccountEntries <= 0 {
		c.MaxAccountEntries = defaults.MaxAccountEntries
	}
	return c
}

type tokenBucket struct {
	tokens        float64
	last          time.Time
	ratePerSecond float64
	burst         float64
}

func newTokenBucket(ratePerMinute, burst int, now time.Time) tokenBucket {
	return tokenBucket{
		tokens:        float64(burst),
		last:          now,
		ratePerSecond: float64(ratePerMinute) / 60,
		burst:         float64(burst),
	}
}

// take consumes one token and returns the delay before another token should
// be available. Calls happen while admission.mu is held.
func (b *tokenBucket) take(now time.Time) (bool, time.Duration) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsed.Seconds()*b.ratePerSecond)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration(math.Ceil((1-b.tokens)/b.ratePerSecond) * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

type accountAdmission struct {
	active   int
	lastSeen time.Time
	bucket   tokenBucket
}

// HTTPAdmission is a process-local, non-waiting admission gate. A request
// either gets both global and account slots immediately or receives 429; it
// never holds one slot while waiting for the other.
type HTTPAdmission struct {
	mu sync.Mutex

	config       HTTPAdmissionConfig
	globalActive int
	globalBucket tokenBucket
	accounts     map[string]*accountAdmission
	now          func() time.Time
}

// NewHTTPAdmissionHandler wraps a stateless MCP transport with authenticated
// POST admission. The caller must put authentication around this handler;
// userFromContext is evaluated for every request, not once per connection.
func NewHTTPAdmissionHandler(config HTTPAdmissionConfig, userFromContext func(context.Context) (auth.User, bool), next http.Handler) http.Handler {
	return NewHTTPAdmission(config).ServeHTTP(userFromContext, next)
}

// NewHTTPAdmission creates an admission gate with the supplied limits.
func NewHTTPAdmission(config HTTPAdmissionConfig) *HTTPAdmission {
	config = config.normalize()
	now := time.Now
	at := now()
	return &HTTPAdmission{
		config:       config,
		globalBucket: newTokenBucket(config.GlobalRatePerMinute, config.GlobalRateBurst, at),
		accounts:     make(map[string]*accountAdmission),
		now:          now,
	}
}

// ServeHTTP admits an authenticated POST and releases every acquired slot when
// the downstream handler returns, including when it panics. Authentication must
// wrap this handler so rejected credentials cannot consume quota.
func (a *HTTPAdmission) ServeHTTP(userFromContext func(context.Context) (auth.User, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := userFromContext(r.Context())
		if !ok || user.ID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="canvas"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// The stateless SDK transport owns method validation and its 405/Allow
		// response. Only POST can execute MCP work, so non-POST requests must
		// not consume admission slots or rate-limit tokens.
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		release, retry, ok := a.acquire(user.ID)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retry)))
			http.Error(w, "too many MCP requests", http.StatusTooManyRequests)
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

func retryAfterSeconds(wait time.Duration) int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

func (a *HTTPAdmission) acquire(accountID string) (func(), time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()

	if a.globalActive >= a.config.GlobalConcurrency {
		return nil, time.Second, false
	}

	account := a.accounts[accountID]
	if account == nil {
		if !a.makeAccountLocked() {
			return nil, time.Second, false
		}
		account = &accountAdmission{
			lastSeen: now,
			bucket:   newTokenBucket(a.config.AccountRatePerMinute, a.config.AccountRateBurst, now),
		}
		a.accounts[accountID] = account
	}
	account.lastSeen = now
	if account.active >= a.config.AccountConcurrency {
		return nil, time.Second, false
	}
	if ok, wait := account.bucket.take(now); !ok {
		return nil, wait, false
	}
	if ok, wait := a.globalBucket.take(now); !ok {
		return nil, wait, false
	}

	account.active++
	a.globalActive++
	released := false
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if released {
			return
		}
		released = true
		account.active--
		a.globalActive--
		account.lastSeen = a.now()
	}, 0, true
}

// makeAccountLocked evicts the least recently used idle entry when the
// metadata bound is reached. Active entries are never evicted, because they
// own an in-flight slot whose release closure still references the entry.
func (a *HTTPAdmission) makeAccountLocked() bool {
	if len(a.accounts) < a.config.MaxAccountEntries {
		return true
	}
	var oldestID string
	var oldest *accountAdmission
	for id, account := range a.accounts {
		if account.active != 0 || (oldest != nil && !account.lastSeen.Before(oldest.lastSeen)) {
			continue
		}
		oldestID, oldest = id, account
	}
	if oldest == nil {
		return false
	}
	delete(a.accounts, oldestID)
	return true
}

// accountCount is intentionally test-only through the package's tests; the
// map itself is not exposed as a cross-process quota or operational API.
func (a *HTTPAdmission) accountCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.accounts)
}
