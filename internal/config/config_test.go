package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var valid = map[string]string{
	"DATABASE_URL":   "postgres://u:p@localhost:5432/db",
	"JWT_SECRET_KEY": strings.Repeat("k", 32),
}

func with(extra map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range valid {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 3000 || c.UserTokenTTLMinutes != 1440 || c.AdminTokenTTLMinutes != 20 || c.AdminSignupEnabled ||
		!c.CORSAny || c.TrustProxyAll || c.TrustProxyHops != 0 || c.RateLimitWindowMs != 60000 ||
		c.RateLimitMax != 300 || c.AuthRateLimitMax != 20 || c.ShutdownTimeoutMs != 10000 ||
		c.LogLevel != "info" || c.NodeEnv != "development" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"NODE_ENV":               "prod",
		"PORT":                   "70000",
		"JWT_SECRET_KEY":         "short",
		"USER_TOKEN_TTL_MINUTES": "0",
		"ADMIN_SIGNUP_ENABLED":   "maybe",
		"TRUST_PROXY":            "-1",
		"RATE_LIMIT_WINDOW_MS":   "10",
		"RATE_LIMIT_MAX":         "x",
		"LOG_LEVEL":              "verbose",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"NODE_ENV must be one of", "PORT must be a valid port", "DATABASE_URL is required",
		"JWT_SECRET_KEY must be at least 32 characters long", "USER_TOKEN_TTL_MINUTES must be greater than or equal to 1",
		"ADMIN_SIGNUP_ENABLED must be a boolean", "TRUST_PROXY must be", "RATE_LIMIT_WINDOW_MS must be greater than or equal to 1000",
		"RATE_LIMIT_MAX must be an integer", "LOG_LEVEL must be one of",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%s", want, err)
		}
	}
}

func TestDatabaseURLScheme(t *testing.T) {
	if _, err := Load(env(with(map[string]string{"DATABASE_URL": "mysql://x"}))); err == nil {
		t.Fatal("expected scheme error")
	}
	if _, err := Load(env(with(map[string]string{"DATABASE_URL": "postgresql://x/y"}))); err != nil {
		t.Fatal(err)
	}
}

func TestParsing(t *testing.T) {
	c, err := Load(env(with(map[string]string{
		"ADMIN_SIGNUP_ENABLED": "TRUE", "TRUST_PROXY": "2", "CORS_ORIGINS": " https://a.test , https://b.test ,",
		"SHUTDOWN_TIMEOUT_MS": "0", "PORT": "8080",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AdminSignupEnabled || c.TrustProxyHops != 2 || c.CORSAny || len(c.CORSOrigins) != 2 || c.ShutdownTimeoutMs != 0 || c.Port != 8080 {
		t.Fatalf("unexpected: %+v", c)
	}
	c, err = Load(env(with(map[string]string{"TRUST_PROXY": "true"})))
	if err != nil || !c.TrustProxyAll {
		t.Fatalf("trust proxy true: %+v %v", c, err)
	}
}

func TestEmptyValuesCountAsUnset(t *testing.T) {
	c, err := Load(env(with(map[string]string{"PORT": "", "LOG_LEVEL": "", "TRUST_PROXY": ""})))
	if err != nil || c.Port != 3000 {
		t.Fatalf("%+v %v", c, err)
	}
}
