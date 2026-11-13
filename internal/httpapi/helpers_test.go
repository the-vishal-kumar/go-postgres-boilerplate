package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/config"
)

const testSecret = "unit-test-secret-unit-test-secret-1234"

func testConfig(t testing.TB, extra map[string]string) config.Config {
	t.Helper()
	env := map[string]string{
		"DATABASE_URL":        "postgres://u:p@localhost:5432/db",
		"JWT_SECRET_KEY":      testSecret,
		"RATE_LIMIT_MAX":      "1000000",
		"AUTH_RATE_LIMIT_MAX": "1000000",
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newTestApp(t testing.TB, extra map[string]string, st Store) *App {
	t.Helper()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	app, err := New(testConfig(t, extra), st, log)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

type result struct {
	Status  int
	Header  http.Header
	Raw     string
	Body    map[string]any
	Decoded bool
}

// call sends one request through the handler. body may be a string (sent
// as-is), nil, or any JSON-encodable value.
func call(t testing.TB, h http.Handler, method, path, token string, body any) result {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return send(h, req)
}

func send(h http.Handler, req *http.Request) result {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := result{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.String()}
	dec := json.NewDecoder(strings.NewReader(res.Raw))
	dec.UseNumber()
	if err := dec.Decode(&res.Body); err == nil {
		res.Decoded = true
	}
	return res
}

func errorsOf(r result) []string {
	raw, _ := r.Body["errors"].([]any)
	out := make([]string, len(raw))
	for i, e := range raw {
		out[i], _ = e.(string)
	}
	return out
}

// jsonValue round-trips v through JSON the way the server decodes bodies.
func jsonValue(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
