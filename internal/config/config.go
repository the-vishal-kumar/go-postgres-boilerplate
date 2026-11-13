// Package config reads and validates the process environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Config holds every setting the service reads from the environment.
type Config struct {
	NodeEnv              string
	Port                 int
	DatabaseURL          string
	JWTSecretKey         string
	UserTokenTTLMinutes  int
	AdminTokenTTLMinutes int
	AdminSignupEnabled   bool
	CORSOrigins          []string // empty slice with CORSAny set means "*"
	CORSAny              bool
	TrustProxyAll        bool // TRUST_PROXY=true: trust the whole X-Forwarded-For chain
	TrustProxyHops       int  // TRUST_PROXY=N: number of trusted proxies in front
	RateLimitWindowMs    int
	RateLimitMax         int
	AuthRateLimitMax     int
	ShutdownTimeoutMs    int
	LogLevel             string
}

// Load validates the environment returned by getenv. Every problem is reported
// at once so a misconfigured deployment is fixed in one pass.
func Load(getenv func(string) string) (Config, error) {
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	// An empty value is treated as unset: compose files often pass "KEY=".
	get := func(key string) (string, bool) {
		v := strings.TrimSpace(getenv(key))
		return v, v != ""
	}
	integer := func(key string, def, min int) int {
		v, ok := get(key)
		if !ok {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			fail("%s must be an integer", key)
			return def
		}
		if n < min {
			fail("%s must be greater than or equal to %d", key, min)
			return def
		}
		return n
	}
	oneOf := func(key, def string, allowed ...string) string {
		v, ok := get(key)
		if !ok {
			return def
		}
		for _, a := range allowed {
			if v == a {
				return v
			}
		}
		fail("%s must be one of [%s]", key, strings.Join(allowed, ", "))
		return def
	}

	c := Config{}
	c.NodeEnv = oneOf("NODE_ENV", "development", "development", "test", "staging", "production")
	c.Port = integer("PORT", 3000, 1)
	if c.Port > 65535 {
		fail("PORT must be a valid port")
	}

	if v, ok := get("DATABASE_URL"); !ok {
		fail("DATABASE_URL is required")
	} else if !strings.HasPrefix(v, "postgres://") && !strings.HasPrefix(v, "postgresql://") {
		fail("DATABASE_URL must be a valid uri with a scheme matching the postgres|postgresql pattern")
	} else {
		c.DatabaseURL = v
	}

	if v, ok := get("JWT_SECRET_KEY"); !ok {
		fail("JWT_SECRET_KEY is required")
	} else if utf8.RuneCountInString(v) < 32 {
		fail("JWT_SECRET_KEY must be at least 32 characters long")
	} else {
		c.JWTSecretKey = v
	}

	c.UserTokenTTLMinutes = integer("USER_TOKEN_TTL_MINUTES", 24*60, 1)
	c.AdminTokenTTLMinutes = integer("ADMIN_TOKEN_TTL_MINUTES", 20, 1)

	c.AdminSignupEnabled = false
	if v, ok := get("ADMIN_SIGNUP_ENABLED"); ok {
		switch strings.ToLower(v) {
		case "true":
			c.AdminSignupEnabled = true
		case "false":
		default:
			fail("ADMIN_SIGNUP_ENABLED must be a boolean")
		}
	}

	origins, ok := get("CORS_ORIGINS")
	if !ok || origins == "*" {
		c.CORSAny = true
	} else {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.CORSOrigins = append(c.CORSOrigins, o)
			}
		}
	}

	if v, ok := get("TRUST_PROXY"); ok {
		switch strings.ToLower(v) {
		case "true":
			c.TrustProxyAll = true
		case "false":
		default:
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				fail("TRUST_PROXY must be a boolean or a non-negative integer")
			} else {
				c.TrustProxyHops = n
			}
		}
	}

	c.RateLimitWindowMs = integer("RATE_LIMIT_WINDOW_MS", 60000, 1000)
	c.RateLimitMax = integer("RATE_LIMIT_MAX", 300, 1)
	c.AuthRateLimitMax = integer("AUTH_RATE_LIMIT_MAX", 20, 1)
	c.ShutdownTimeoutMs = integer("SHUTDOWN_TIMEOUT_MS", 10000, 0)
	c.LogLevel = oneOf("LOG_LEVEL", "info", "error", "warn", "info", "debug")

	if len(problems) > 0 {
		return Config{}, errors.New("Invalid environment configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return c, nil
}
