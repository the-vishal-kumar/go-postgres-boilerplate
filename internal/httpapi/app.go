// Package httpapi wires the HTTP surface: routing, middleware, handlers and
// the response envelope.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/config"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/docs"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/store"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/uuid"
)

const maxBodyBytes = 10240

var requestIDPattern = regexp.MustCompile(`^[\w.-]{1,128}$`)

// Store is what the handlers need from the database layer.
type Store interface {
	Ping(ctx context.Context) error
	CreateUser(ctx context.Context, u store.User) error
	UserByEmail(ctx context.Context, email string) (store.User, error)
	PruneExpiredSessions(ctx context.Context, userID string) error
	CreateSession(ctx context.Context, s store.Session) error
	SessionUser(ctx context.Context, tokenHash string) (store.Session, store.User, error)
	DeleteSession(ctx context.Context, id string) error
	ChangePassword(ctx context.Context, userID, hash, keepSessionID string) error
	CreateLoan(ctx context.Context, l store.Loan) error
	LoansByUser(ctx context.Context, userID string) ([]store.Loan, error)
	LoanByID(ctx context.Context, id string) (store.Loan, error)
	Approve(ctx context.Context, loanID, adminID string) error
	Pay(ctx context.Context, loanID, userID string, amountCents int64) error
}

// App is the HTTP application.
type App struct {
	cfg     config.Config
	store   Store
	log     *slog.Logger
	secret  []byte
	spec    []byte
	started time.Time
	now     func() time.Time

	shuttingDown atomic.Bool
	general      *Limiter
	authLimiter  *Limiter
}

// New builds the application. The OpenAPI document is prepared here so a broken
// embedded contract fails at start-up, not on the first request.
func New(cfg config.Config, st Store, log *slog.Logger) (*App, error) {
	spec, err := docs.Spec()
	if err != nil {
		return nil, err
	}
	w := time.Duration(cfg.RateLimitWindowMs) * time.Millisecond
	return &App{
		cfg:         cfg,
		store:       st,
		log:         log,
		secret:      []byte(cfg.JWTSecretKey),
		spec:        spec,
		started:     time.Now(),
		now:         time.Now,
		general:     NewLimiter(cfg.RateLimitMax, w),
		authLimiter: NewLimiter(cfg.AuthRateLimitMax, w),
	}, nil
}

// BeginShutdown makes readiness fail so the load balancer stops sending traffic.
func (a *App) BeginShutdown() { a.shuttingDown.Store(true) }

// RunSweepers starts the rate-limit cleanup loops; they stop with ctx.
func (a *App) RunSweepers(ctx context.Context) {
	interval := time.Duration(a.cfg.RateLimitWindowMs) * time.Millisecond
	go a.general.Run(ctx, interval)
	go a.authLimiter.Run(ctx, interval)
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (a *App) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			a.fail(w, r, err)
		}
	}
}

// fail answers a handler error. Known errors keep their status and message;
// anything else is a 500 that exposes only the request id.
func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeAPIError(w, ae)
		return
	}
	id := stateOf(r.Context()).id
	a.log.Error("unhandled error", "requestId", id, "method", r.Method, "path", r.URL.Path, "error", err.Error())
	writeJSON(w, http.StatusInternalServerError, errorBody{Status: 500, Error: "An error occurred", RequestID: id})
}

// Handler returns the full application handler.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", a.live)
	mux.HandleFunc("GET /health/ready", a.ready)

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusFound)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusFound)
	})
	mux.HandleFunc("GET /docs/{$}", func(w http.ResponseWriter, r *http.Request) {
		serveBytes(w, r, "text/html; charset=utf-8", docs.Index())
	})
	mux.HandleFunc("GET /docs/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		serveBytes(w, r, docs.ContentType(), a.spec)
	})
	mux.HandleFunc("GET /docs/swagger-initializer.js", func(w http.ResponseWriter, r *http.Request) {
		serveBytes(w, r, "text/javascript; charset=utf-8", docs.Initializer())
	})
	mux.HandleFunc("GET /docs/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		b, ok := docs.Asset(name)
		if !ok {
			a.notFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		serveBytes(w, r, mime.TypeByExtension(path.Ext(name)), b)
	})

	mux.HandleFunc("POST /auth/signup", a.h(a.signup(false)))
	mux.HandleFunc("POST /auth/signup/admin", a.h(a.signup(true)))
	mux.HandleFunc("POST /auth/signin", a.h(a.signin(false)))
	mux.HandleFunc("POST /auth/signin/admin", a.h(a.signin(true)))
	mux.HandleFunc("POST /auth/signoff", a.h(a.signoff))
	mux.HandleFunc("PUT /auth/password/reset", a.h(a.resetPassword))

	mux.HandleFunc("POST /loan", a.h(a.requestLoan))
	mux.HandleFunc("GET /loan", a.h(a.listLoans))
	mux.HandleFunc("GET /loan/{id}", a.h(a.getLoan))
	mux.HandleFunc("PUT /loan/{id}/approve", a.h(a.approveLoan))
	mux.HandleFunc("PUT /loan/{id}/repayment", a.h(a.makePayment))

	// Catch-all: unknown paths and wrong methods both answer 404 JSON.
	mux.HandleFunc("/", a.notFound)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.serve(mux, w, r) })
}

func (a *App) notFound(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, httpErr(http.StatusNotFound, "Resource not found"))
}

func serveBytes(w http.ResponseWriter, r *http.Request, contentType string, b []byte) {
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
}

func isHealth(p string) bool { return strings.HasPrefix(p, "/health/") }

func (a *App) serve(mux *http.ServeMux, rw http.ResponseWriter, r *http.Request) {
	st := &reqState{id: r.Header.Get("X-Request-Id")}
	if !requestIDPattern.MatchString(st.id) {
		st.id = uuid.New()
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, st))
	w := &statusWriter{ResponseWriter: rw}
	setSecurityHeaders(w.Header())
	w.Header().Set("X-Request-Id", st.id)

	started := time.Now()
	defer func() {
		if rec := recover(); rec != nil {
			if w.status == 0 {
				a.fail(w, r, fmt.Errorf("panic: %v", rec))
			} else {
				a.log.Error("panic after response started", "requestId", st.id, "panic", fmt.Sprint(rec))
			}
		}
		if isHealth(r.URL.Path) {
			return
		}
		level := slog.LevelInfo
		if w.status >= 500 {
			level = slog.LevelError
		}
		a.log.Log(r.Context(), level, "request completed",
			"requestId", st.id,
			"method", r.Method,
			"path", r.URL.Path,
			"statusCode", w.status,
			"durationMs", float64(time.Since(started).Microseconds())/1000,
		)
	}()

	if isHealth(r.URL.Path) {
		mux.ServeHTTP(w, r)
		return
	}
	if a.cors(w, r) {
		return
	}
	if !a.readBody(w, r, st) {
		return
	}
	if !a.rateLimit(w, r) {
		return
	}
	mux.ServeHTTP(w, r)
}

// setSecurityHeaders sets the same header set as helmet 8 defaults.
func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'self';base-uri 'self';font-src 'self' https: data:;form-action 'self';frame-ancestors 'self';img-src 'self' data:;object-src 'none';script-src 'self';script-src-attr 'none';style-src 'self' https: 'unsafe-inline';upgrade-insecure-requests")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Origin-Agent-Cluster", "?1")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-DNS-Prefetch-Control", "off")
	h.Set("X-Download-Options", "noopen")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Permitted-Cross-Domain-Policies", "none")
	h.Set("X-XSS-Protection", "0")
}

// cors reports true when it fully answered the request (a preflight).
func (a *App) cors(w http.ResponseWriter, r *http.Request) bool {
	h := w.Header()
	origin := r.Header.Get("Origin")
	if a.cfg.CORSAny {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Add("Vary", "Origin")
		for _, o := range a.cfg.CORSOrigins {
			if o == origin {
				h.Set("Access-Control-Allow-Origin", origin)
				break
			}
		}
	}
	if r.Method != http.MethodOptions {
		return false
	}
	h.Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE")
	if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
		h.Set("Access-Control-Allow-Headers", reqHeaders)
		h.Add("Vary", "Access-Control-Request-Headers")
	}
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusNoContent)
	return true
}

// readBody parses a JSON body once, before routing and auth, so malformed or
// oversized bodies get 400 or 413 whatever the route. No body counts as {}.
func (a *App) readBody(w http.ResponseWriter, r *http.Request, st *reqState) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" && !strings.HasSuffix(media, "+json") {
		return true
	}
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeAPIError(w, httpErr(http.StatusRequestEntityTooLarge, "Request body is too large"))
		} else {
			writeAPIError(w, httpErr(http.StatusBadRequest, "Request body is not valid JSON"))
		}
		return false
	}
	if len(data) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		writeAPIError(w, httpErr(http.StatusBadRequest, "Request body is not valid JSON"))
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeAPIError(w, httpErr(http.StatusBadRequest, "Request body is not valid JSON"))
		return false
	}
	st.body, st.hasBody = v, true
	return true
}

func isAuthPath(p string) bool {
	for _, prefix := range []string{"/auth/signup", "/auth/signin", "/auth/password"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// rateLimit applies the auth limiter to credential endpoints and the general
// limiter to everything except probes, docs and the root redirect.
func (a *App) rateLimit(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if p == "/" || p == "/docs" || strings.HasPrefix(p, "/docs/") {
		return true
	}
	ip := a.clientIP(r)
	if isAuthPath(p) && !a.hit(w, a.authLimiter, "auth", ip, "Too many authentication attempts, try again later") {
		return false
	}
	return a.hit(w, a.general, "api", ip, "Too many requests, try again later")
}

func (a *App) hit(w http.ResponseWriter, l *Limiter, name, ip, message string) bool {
	now := a.now()
	allowed, remaining, reset := l.Hit(ip, now)
	secs := int((reset.Sub(now) + time.Second - 1) / time.Second)
	secs = max(secs, 0)
	h := w.Header()
	h.Set("RateLimit-Policy", fmt.Sprintf(`"%s";q=%d;w=%d`, name, l.limit, int((l.window+time.Second-1)/time.Second)))
	h.Set("RateLimit", fmt.Sprintf(`"%s";r=%d;t=%d`, name, remaining, secs))
	if allowed {
		return true
	}
	h.Set("Retry-After", strconv.Itoa(secs))
	writeAPIError(w, httpErr(http.StatusTooManyRequests, message))
	return false
}

// clientIP is the socket peer unless TRUST_PROXY says to read X-Forwarded-For:
// N trusted proxies means the entry N hops from the right; "true" means the
// leftmost entry.
func (a *App) clientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if !a.cfg.TrustProxyAll && a.cfg.TrustProxyHops < 1 {
		return peer
	}
	var entries []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(v, ",") {
			if e = strings.TrimSpace(e); e != "" {
				entries = append(entries, e)
			}
		}
	}
	if len(entries) == 0 {
		return peer
	}
	if a.cfg.TrustProxyAll {
		return entries[0]
	}
	return entries[max(len(entries)-a.cfg.TrustProxyHops, 0)]
}
