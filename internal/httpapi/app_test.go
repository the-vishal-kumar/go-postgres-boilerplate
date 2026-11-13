package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/auth"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/store"
)

// stub implements only what a test overrides; touching anything else panics,
// which proves the path under test never reached the database.
type stub struct {
	Store
	ping    error
	session func() (store.Session, store.User, error)
}

func (s stub) Ping(context.Context) error { return s.ping }
func (s stub) SessionUser(context.Context, string) (store.Session, store.User, error) {
	return s.session()
}

func TestUnknownRoutesAnd405AreJSON404(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	for _, c := range [][2]string{{"GET", "/nope"}, {"DELETE", "/loan"}, {"GET", "/auth/signup"}, {"POST", "/health/live"}, {"PATCH", "/loan/abc"}, {"GET", "/docs/secret.txt"}} {
		r := call(t, h, c[0], c[1], "", nil)
		if r.Status != 404 || r.Raw != `{"status":404,"error":"Resource not found"}` {
			t.Errorf("%s %s: %d %s", c[0], c[1], r.Status, r.Raw)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s %s content type %q", c[0], c[1], ct)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	want := map[string]string{
		"Content-Security-Policy":           "default-src 'self';base-uri 'self';font-src 'self' https: data:;form-action 'self';frame-ancestors 'self';img-src 'self' data:;object-src 'none';script-src 'self';script-src-attr 'none';style-src 'self' https: 'unsafe-inline';upgrade-insecure-requests",
		"Cross-Origin-Opener-Policy":        "same-origin",
		"Cross-Origin-Resource-Policy":      "same-origin",
		"Origin-Agent-Cluster":              "?1",
		"Referrer-Policy":                   "no-referrer",
		"Strict-Transport-Security":         "max-age=31536000; includeSubDomains",
		"X-Content-Type-Options":            "nosniff",
		"X-Dns-Prefetch-Control":            "off",
		"X-Download-Options":                "noopen",
		"X-Frame-Options":                   "SAMEORIGIN",
		"X-Permitted-Cross-Domain-Policies": "none",
		"X-Xss-Protection":                  "0",
	}
	for _, path := range []string{"/nope", "/docs/", "/health/live", "/loan"} {
		r := call(t, h, "GET", path, "", nil)
		for k, v := range want {
			if got := r.Header.Get(k); got != v {
				t.Errorf("%s %s = %q; want %q", path, k, got, v)
			}
		}
		if r.Header.Get("X-Powered-By") != "" || r.Header.Get("Server") != "" {
			t.Errorf("%s leaks a fingerprint header", path)
		}
	}
}

func TestRequestID(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	req := httptest.NewRequest("GET", "/nope", nil)
	req.Header.Set("X-Request-Id", "abc-123.x_Y")
	if got := send(h, req).Header.Get("X-Request-Id"); got != "abc-123.x_Y" {
		t.Errorf("kept id = %q", got)
	}
	for _, bad := range []string{"bad id with spaces", strings.Repeat("a", 129), "semi;colon"} {
		req := httptest.NewRequest("GET", "/nope", nil)
		req.Header.Set("X-Request-Id", bad)
		got := send(h, req).Header.Get("X-Request-Id")
		if got == bad || len(got) != 36 {
			t.Errorf("malformed id %q was echoed as %q", bad, got)
		}
	}
	a, b := call(t, h, "GET", "/nope", "", nil), call(t, h, "GET", "/nope", "", nil)
	if a.Header.Get("X-Request-Id") == "" || a.Header.Get("X-Request-Id") == b.Header.Get("X-Request-Id") {
		t.Error("generated ids must be present and unique")
	}
}

func TestBodyErrors(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	cases := []struct {
		name, body string
		status     int
		raw        string
	}{
		{"malformed", `{"email": `, 400, `{"status":400,"error":"Request body is not valid JSON"}`},
		{"trailing data", `{} {}`, 400, `{"status":400,"error":"Request body is not valid JSON"}`},
		{"too large", `{"password":"` + strings.Repeat("x", 20000) + `"}`, 413, `{"status":413,"error":"Request body is too large"}`},
		{"exactly at limit is parsed", `{"a":"` + strings.Repeat("x", 10240-8) + `"}`, 422, ""},
	}
	for _, c := range cases {
		r := call(t, h, "POST", "/auth/signin", "", c.body)
		if r.Status != c.status || (c.raw != "" && r.Raw != c.raw) {
			t.Errorf("%s: %d %s", c.name, r.Status, r.Raw)
		}
	}
	for _, body := range []string{`[1, 2]`, `"text"`, `5`, `null`} {
		r := call(t, h, "POST", "/auth/signin", "", body)
		if r.Status != 422 || !reflect.DeepEqual(errorsOf(r), []string{"request body must be a JSON object"}) {
			t.Errorf("body %s: %d %s", body, r.Status, r.Raw)
		}
	}
	// A body is parsed before auth, so bad JSON beats a missing token.
	if r := call(t, h, "POST", "/loan", "", `{`); r.Status != 400 {
		t.Errorf("bad json on protected route: %d", r.Status)
	}
	// An empty body counts as {}.
	r := call(t, h, "POST", "/auth/signin", "", nil)
	if r.Status != 422 || !reflect.DeepEqual(errorsOf(r), []string{`"email" is required`, `"password" is required`}) {
		t.Errorf("empty body: %d %s", r.Status, r.Raw)
	}
	r = call(t, h, "POST", "/auth/signin", "", "")
	if r.Status != 422 {
		t.Errorf("empty string body: %d %s", r.Status, r.Raw)
	}
}

func TestSignupValidationMessages(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	long := strings.Repeat("x", 101)
	cases := []struct {
		body any
		want []string
	}{
		{map[string]any{}, []string{`"email" is required`, `"password" is required`, `"firstName" is required`}},
		{map[string]any{"email": "not-an-email", "password": "password123", "firstName": "A"}, []string{`"email" must be a valid email`}},
		{map[string]any{"email": "a@b", "password": "password123", "firstName": "A"}, []string{`"email" must be a valid email`}},
		{map[string]any{"email": "a b@example.com", "password": "password123", "firstName": "A"}, []string{`"email" must be a valid email`}},
		{map[string]any{"email": "a@@example.com", "password": "password123", "firstName": "A"}, []string{`"email" must be a valid email`}},
		{map[string]any{"email": "a@example.c", "password": "password123", "firstName": "A"}, []string{`"email" must be a valid email`}},
		{map[string]any{"email": "  ", "password": "password123", "firstName": "A"}, []string{`"email" is not allowed to be empty`}},
		{map[string]any{"email": 5, "password": true, "firstName": []any{}}, []string{`"email" must be a string`, `"password" must be a string`, `"firstName" must be a string`}},
		{map[string]any{"email": strings.Repeat("a", 250) + "@example.com", "password": "password123", "firstName": "A"},
			[]string{`"email" length must be less than or equal to 254 characters long`}},
		{map[string]any{"email": "a@example.com", "password": "short", "firstName": "A"}, []string{`"password" length must be at least 8 characters long`}},
		{map[string]any{"email": "a@example.com", "password": strings.Repeat("p", 129), "firstName": "A"}, []string{`"password" length must be less than or equal to 128 characters long`}},
		{map[string]any{"email": "a@example.com", "password": "password123", "firstName": "   "}, []string{`"firstName" is not allowed to be empty`}},
		{map[string]any{"email": "a@example.com", "password": "password123", "firstName": long}, []string{`"firstName" length must be less than or equal to 100 characters long`}},
		{map[string]any{"email": "a@example.com", "password": "password123", "firstName": "A", "lastName": long}, []string{`"lastName" length must be less than or equal to 100 characters long`}},
		{map[string]any{"email": "a@example.com", "password": "password123", "firstName": "A", "isAdmin": true, "zeta": 1}, []string{`"isAdmin" is not allowed`, `"zeta" is not allowed`}},
	}
	for _, c := range cases {
		r := call(t, h, "POST", "/auth/signup", "", c.body)
		if r.Status != 422 || r.Body["error"] != "Validation error" || !reflect.DeepEqual(errorsOf(r), c.want) {
			t.Errorf("%v\n got %d %s\nwant %v", c.body, r.Status, r.Raw, c.want)
		}
	}
}

func TestPasswordIsNotTrimmed(t *testing.T) {
	// 7 characters plus a space is 8: the space counts.
	r := call(t, newTestApp(t, nil, stub{}).Handler(), "POST", "/auth/signup", "", map[string]any{"email": "a@example.com", "password": "1234567 ", "firstName": ""})
	if !reflect.DeepEqual(errorsOf(r), []string{`"firstName" is not allowed to be empty`}) {
		t.Errorf("%s", r.Raw)
	}
}

func TestAdminSignupDisabledIsCheckedBeforeValidation(t *testing.T) {
	h := newTestApp(t, map[string]string{"ADMIN_SIGNUP_ENABLED": "false"}, stub{}).Handler()
	r := call(t, h, "POST", "/auth/signup/admin", "", map[string]any{})
	if r.Status != 403 || r.Raw != `{"status":403,"error":"Admin sign-up is disabled"}` {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
}

func TestMoneyAndTermMessages(t *testing.T) {
	cases := []struct {
		body map[string]any
		want []string
	}{
		{map[string]any{}, []string{`"amount" is required`, `"term" is required`}},
		{map[string]any{"amount": "lots", "term": "x"}, []string{`"amount" must be a number`, `"term" must be a number`}},
		{map[string]any{"amount": nil, "term": true}, []string{`"amount" must be a number`, `"term" must be a number`}},
		{map[string]any{"amount": 0, "term": 0}, []string{`"amount" must be greater than or equal to 0.01`, `"term" must be greater than or equal to 1`}},
		{map[string]any{"amount": -5, "term": 521}, []string{`"amount" must be greater than or equal to 0.01`, `"term" must be less than or equal to 520`}},
		{map[string]any{"amount": 1000000000.01, "term": 2.5}, []string{`"amount" must be less than or equal to 1000000000`, `"term" must be an integer`}},
		{map[string]any{"amount": 10.005, "term": 3}, []string{`"amount" must have no more than 2 decimal places`}},
		{map[string]any{"amount": 30.001, "term": 3}, []string{`"amount" must have no more than 2 decimal places`}},
		{map[string]any{"amount": 100, "term": 3, "status": "APPROVED"}, []string{`"status" is not allowed`}},
	}
	for _, c := range cases {
		v := newValidator(&reqState{body: jsonValue(t, c.body), hasBody: true})
		v.money("amount")
		v.term("term")
		if got := v.finish(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v\n got %v\nwant %v", c.body, got, c.want)
		}
	}
	v := newValidator(&reqState{body: jsonValue(t, map[string]any{"amount": "22.5", "term": "7"}), hasBody: true})
	cents, ok1 := v.money("amount")
	term, ok2 := v.term("term")
	if len(v.finish()) != 0 || !ok1 || !ok2 || cents != 2250 || term != 7 {
		t.Errorf("numeric strings: %d %d %v", cents, term, v.errs)
	}
}

func TestIDValidation(t *testing.T) {
	if _, msg := validID("not-a-uuid"); msg != `"id" must be a valid GUID` {
		t.Error(msg)
	}
	if id, msg := validID(" 00000000-0000-4000-8000-00000000ABCD "); msg != "" || id != "00000000-0000-4000-8000-00000000abcd" {
		t.Error(id, msg)
	}
}

func TestEmailCheck(t *testing.T) {
	good := []string{"a@example.com", "first.last+tag@sub.example.co.uk", "x@y.io"}
	bad := []string{"", "a", "a@", "@example.com", "a@example", "a@example.", "a@.com", "a@exa mple.com", "a@@b.com", "a@b@c.com", "a@b.c", "a@b.c1", "a\t@b.com"}
	for _, e := range good {
		if !validEmail(e) {
			t.Errorf("%q should pass", e)
		}
	}
	for _, e := range bad {
		if validEmail(e) {
			t.Errorf("%q should fail", e)
		}
	}
}

func TestAuthTokenErrors(t *testing.T) {
	notFound := stub{session: func() (store.Session, store.User, error) { return store.Session{}, store.User{}, store.ErrNotFound }}
	h := newTestApp(t, nil, notFound).Handler()
	valid, _ := auth.SignJWT([]byte(testSecret), "u", time.Now(), time.Hour)
	expired, _ := auth.SignJWT([]byte(testSecret), "u", time.Now().Add(-2*time.Hour), time.Hour)
	forged, _ := auth.SignJWT([]byte("a-different-secret-a-different-secret"), "u", time.Now(), time.Hour)
	cases := []struct {
		header, msg string
	}{
		{"", "Missing access token"},
		{"Basic abc", "Missing access token"},
		{"Bearer", "Missing access token"},
		{"Bearer ", "Missing access token"},
		{"Bearer  x", "Missing access token"},
		{"bearer " + valid, "Missing access token"},
		{"Bearer not-a-jwt", "Invalid access token"},
		{"Bearer " + forged, "Invalid access token"},
		{"Bearer " + expired, "Invalid access token"},
		{"Bearer " + valid, "Invalid access token"}, // signature fine, but no live session
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/loan", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		r := send(h, req)
		if r.Status != 401 || r.Body["error"] != c.msg {
			t.Errorf("%q: %d %s", c.header, r.Status, r.Raw)
		}
	}
	// Authentication comes before validation.
	if r := call(t, h, "POST", "/loan", "", map[string]any{"amount": "nonsense"}); r.Status != 401 || r.Body["error"] != "Missing access token" {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
}

func TestDatabaseFailureIs500WithRequestIDOnly(t *testing.T) {
	broken := stub{session: func() (store.Session, store.User, error) {
		return store.Session{}, store.User{}, errors.New("dial tcp 10.0.0.5:5432: connection refused (password=hunter2)")
	}}
	h := newTestApp(t, nil, broken).Handler()
	tok, _ := auth.SignJWT([]byte(testSecret), "u", time.Now(), time.Hour)
	r := call(t, h, "GET", "/loan", tok, nil)
	if r.Status != 500 || r.Body["error"] != "An error occurred" || r.Body["requestId"] != r.Header.Get("X-Request-Id") || r.Body["requestId"] == "" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if strings.Contains(r.Raw, "hunter2") || strings.Contains(r.Raw, "dial") {
		t.Fatalf("internals leaked: %s", r.Raw)
	}
}

func TestReadiness(t *testing.T) {
	app := newTestApp(t, nil, stub{})
	h := app.Handler()
	r := call(t, h, "GET", "/health/ready", "", nil)
	if r.Status != 200 || r.Raw != `{"status":"ready","checks":{"postgres":"ok","shutdown":"ok"}}` {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
	app.BeginShutdown()
	r = call(t, h, "GET", "/health/ready", "", nil)
	if r.Status != 503 || r.Raw != `{"status":"not ready","checks":{"postgres":"ok","shutdown":"fail"}}` {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
	down := newTestApp(t, nil, stub{ping: errors.New("down")}).Handler()
	r = call(t, down, "GET", "/health/ready", "", nil)
	if r.Status != 503 || r.Raw != `{"status":"not ready","checks":{"postgres":"fail","shutdown":"ok"}}` {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
	live := call(t, down, "GET", "/health/live", "", nil)
	if live.Status != 200 || live.Body["status"] != "ok" || !live.Decoded {
		t.Errorf("%d %s", live.Status, live.Raw)
	}
}

func TestDocsRoutes(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	root := call(t, h, "GET", "/", "", nil)
	if root.Status != 302 || root.Header.Get("Location") != "/docs" {
		t.Errorf("root: %d %q", root.Status, root.Header.Get("Location"))
	}
	if r := call(t, h, "GET", "/docs", "", nil); r.Status/100 != 3 || r.Header.Get("Location") != "/docs/" {
		t.Errorf("docs: %d %q", r.Status, r.Header.Get("Location"))
	}
	page := call(t, h, "GET", "/docs/", "", nil)
	if page.Status != 200 || !strings.Contains(page.Raw, "swagger-ui") || strings.Contains(page.Raw, "<script>") ||
		!strings.HasPrefix(page.Header.Get("Content-Type"), "text/html") {
		t.Errorf("index: %d %q", page.Status, page.Header.Get("Content-Type"))
	}
	for path, ct := range map[string]string{
		"/docs/swagger-ui-bundle.js": "javascript", "/docs/swagger-ui.css": "text/css",
		"/docs/swagger-initializer.js": "javascript", "/docs/favicon-32x32.png": "image/png",
		"/docs/swagger-ui-standalone-preset.js": "javascript",
	} {
		r := call(t, h, "GET", path, "", nil)
		if r.Status != 200 || !strings.Contains(r.Header.Get("Content-Type"), ct) || len(r.Raw) == 0 {
			t.Errorf("%s: %d %q", path, r.Status, r.Header.Get("Content-Type"))
		}
	}
	spec := call(t, h, "GET", "/docs/openapi.json", "", nil)
	info, _ := spec.Body["info"].(map[string]any)
	if spec.Status != 200 || info["title"] != "Loan API (Go, PostgreSQL)" {
		t.Errorf("spec: %d %v", spec.Status, info["title"])
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	h := newTestApp(t, map[string]string{"AUTH_RATE_LIMIT_MAX": "2", "RATE_LIMIT_MAX": "5", "TRUST_PROXY": "1"}, stub{}).Handler()
	post := func(ip string) result {
		req := httptest.NewRequest("POST", "/auth/signin", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		return send(h, req)
	}
	if post("1.1.1.1").Status != 422 || post("1.1.1.1").Status != 422 {
		t.Fatal("first two requests should pass the limiter")
	}
	r := post("1.1.1.1")
	if r.Status != 429 || r.Raw != `{"status":429,"error":"Too many authentication attempts, try again later"}` {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if p := r.Header.Get("RateLimit-Policy"); p != `"auth";q=2;w=60` {
		t.Errorf("policy %q", p)
	}
	if v := r.Header.Get("RateLimit"); !strings.HasPrefix(v, `"auth";r=0;t=`) {
		t.Errorf("ratelimit %q", v)
	}
	if post("2.2.2.2").Status != 422 {
		t.Error("another client must not be limited")
	}
	// General limiter: 5 per window; health, docs and "/" are exempt.
	get := func(path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Forwarded-For", "3.3.3.3")
		return send(h, req).Status
	}
	for i := 0; i < 5; i++ {
		if get("/nope") != 404 {
			t.Fatal("within limit")
		}
	}
	if get("/nope") != 429 {
		t.Error("general limiter did not trigger")
	}
	for _, p := range []string{"/health/live", "/docs/", "/"} {
		if get(p) == 429 {
			t.Errorf("%s must not be rate limited", p)
		}
	}
}

func TestClientIP(t *testing.T) {
	mk := func(extra map[string]string, xff ...string) string {
		a := newTestApp(t, extra, stub{})
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "9.9.9.9:1234"
		for _, v := range xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		return a.clientIP(req)
	}
	if got := mk(nil, "1.1.1.1"); got != "9.9.9.9" {
		t.Errorf("untrusted: %s", got)
	}
	if got := mk(map[string]string{"TRUST_PROXY": "1"}, "1.1.1.1"); got != "1.1.1.1" {
		t.Errorf("one entry: %s", got)
	}
	if got := mk(map[string]string{"TRUST_PROXY": "1"}, "6.6.6.6, 1.1.1.1"); got != "1.1.1.1" {
		t.Errorf("spoofed left entry must be ignored: %s", got)
	}
	if got := mk(map[string]string{"TRUST_PROXY": "2"}, "6.6.6.6", "2.2.2.2, 1.1.1.1"); got != "2.2.2.2" {
		t.Errorf("two hops: %s", got)
	}
	if got := mk(map[string]string{"TRUST_PROXY": "5"}, "2.2.2.2, 1.1.1.1"); got != "2.2.2.2" {
		t.Errorf("more hops than entries: %s", got)
	}
	if got := mk(map[string]string{"TRUST_PROXY": "true"}, "2.2.2.2, 1.1.1.1"); got != "2.2.2.2" {
		t.Errorf("trust all: %s", got)
	}
	if got := mk(map[string]string{"TRUST_PROXY": "1"}); got != "9.9.9.9" {
		t.Errorf("no header: %s", got)
	}
}

func TestLimiterWindows(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	t0 := time.Unix(1000, 0)
	for i, want := range []bool{true, true, false, false} {
		ok, remaining, _ := l.Hit("a", t0.Add(time.Duration(i)*time.Second))
		if ok != want {
			t.Errorf("hit %d allowed=%v", i, ok)
		}
		if i == 0 && remaining != 1 {
			t.Errorf("remaining %d", remaining)
		}
	}
	if ok, _, _ := l.Hit("b", t0); !ok {
		t.Error("keys are independent")
	}
	// A new window starts exactly when the old one ends.
	if ok, remaining, reset := l.Hit("a", t0.Add(time.Minute)); !ok || remaining != 1 || !reset.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("new window: %v %d %v", ok, remaining, reset)
	}
	if n := l.Sweep(t0.Add(30 * time.Second)); n != 0 {
		t.Errorf("swept %d live windows", n)
	}
	if n := l.Sweep(t0.Add(time.Minute)); n != 1 { // only "b" has ended
		t.Errorf("swept %d", n)
	}
	if n := l.Sweep(t0.Add(3 * time.Minute)); n != 1 || len(l.m) != 0 {
		t.Errorf("swept %d, left %d", n, len(l.m))
	}
}

func TestCORS(t *testing.T) {
	h := newTestApp(t, nil, stub{}).Handler()
	req := httptest.NewRequest("OPTIONS", "/loan", nil)
	req.Header.Set("Origin", "https://x.test")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	r := send(h, req)
	if r.Status != 204 || r.Header.Get("Access-Control-Allow-Origin") != "*" ||
		r.Header.Get("Access-Control-Allow-Headers") != "authorization,content-type" {
		t.Errorf("%d %v", r.Status, r.Header)
	}
	h = newTestApp(t, map[string]string{"CORS_ORIGINS": "https://a.test,https://b.test"}, stub{}).Handler()
	for origin, want := range map[string]string{"https://b.test": "https://b.test", "https://evil.test": ""} {
		req := httptest.NewRequest("GET", "/nope", nil)
		req.Header.Set("Origin", origin)
		if got := send(h, req).Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %s -> %q", origin, got)
		}
	}
}

func TestPanicBecomes500(t *testing.T) {
	// A stub with no session func panics when called; the handler must survive.
	h := newTestApp(t, nil, stub{}).Handler()
	tok, _ := auth.SignJWT([]byte(testSecret), "u", time.Now(), time.Hour)
	r := call(t, h, "GET", "/loan", tok, nil)
	if r.Status != http.StatusInternalServerError || r.Body["requestId"] == nil {
		t.Errorf("%d %s", r.Status, r.Raw)
	}
}
