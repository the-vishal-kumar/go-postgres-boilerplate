package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// Produced by Node: crypto.scryptSync('password123', salt, 64) with the salt
// string used as-is, stored as "<hex key>.<salt>".
const (
	nodeHash        = "791f673e6bdb380d91f76ccc0ba3469bac2d04f5188bdb65b866482efe94cd71da2b19e8c28a04bdf2abe11f21df801e72f4cf5caf1038591aa21d68b0857238.00112233445566778899aabbccddeeff"
	nodeHashUnicode = "c1281f2567fedcecab871b6adbf48b850906134256f443405f8f701d846d8d0cf7e13f41e95641d43617c2f7a1edb022ac37dcdbcc48b09d4a6467481203dd8f.0123456789abcdef0123456789abcdef"
	// Node jsonwebtoken-style HS256 token, key below, expiring in 2096,
	// kept as its three parts: header, claims and signature.
	nodeJWTHeader    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
	nodeJWTClaims    = "eyJ1c2VySWQiOiJ1MSIsImlhdCI6MTAwMCwiZXhwIjo0MDAwMDAwMDAwLCJqdGkiOiJqIn0"
	nodeJWTSignature = "5xuy-ZhDo3-HIazrfOHnNtV7Fy6WwCbbAW8lNvrNrh8"
	nodeJWT          = nodeJWTHeader + "." + nodeJWTClaims + "." + nodeJWTSignature
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	hash, salt, ok := strings.Cut(h, ".")
	if !ok || len(hash) != 128 || len(salt) != 32 {
		t.Fatalf("unexpected format: %s", h)
	}
	if !VerifyPassword(h, "password123") || VerifyPassword(h, "password124") || VerifyPassword(h, "") {
		t.Fatal("verify mismatch")
	}
	h2, _ := HashPassword("password123")
	if h == h2 {
		t.Fatal("salt must be random")
	}
}

func TestVerifiesNodeHashes(t *testing.T) {
	if !VerifyPassword(nodeHash, "password123") {
		t.Fatal("node hash rejected")
	}
	if VerifyPassword(nodeHash, "wrong") {
		t.Fatal("wrong password accepted")
	}
	if !VerifyPassword(nodeHashUnicode, "päss wörd") {
		t.Fatal("unicode node hash rejected")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, s := range []string{"", "nodot", ".salt", "zz.salt", "abcd.salt", nodeHash[:100]} {
		if VerifyPassword(s, "password123") {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestJWTRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, err := SignJWT(secret, "user-1", now, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := VerifyJWT(secret, tok, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "user-1" || c.Exp-c.Iat != 20*60 || c.Jti == "" {
		t.Fatalf("%+v", c)
	}
	other, _ := SignJWT(secret, "user-1", now, 20*time.Minute)
	if other == tok {
		t.Fatal("jti must differ")
	}
}

func TestJWTExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := SignJWT(secret, "u", now, time.Minute)
	if _, err := VerifyJWT(secret, tok, now.Add(59*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyJWT(secret, tok, now.Add(time.Minute)); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestJWTTamper(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, _ := SignJWT(secret, "u", now, time.Hour)
	parts := strings.Split(tok, ".")
	evil := base64.RawURLEncoding.EncodeToString([]byte(`{"userId":"admin","iat":1,"exp":4000000000,"jti":"x"}`))
	cases := map[string]string{
		"payload swapped": parts[0] + "." + evil + "." + parts[2],
		"bad signature":   parts[0] + "." + parts[1] + "." + parts[2][:len(parts[2])-2] + "AA",
		"no signature":    parts[0] + "." + parts[1] + ".",
		"two parts":       parts[0] + "." + parts[1],
		"garbage":         "not-a-jwt",
		"empty":           "",
	}
	for name, tk := range cases {
		if _, err := VerifyJWT(secret, tk, now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := VerifyJWT([]byte("another-secret-another-secret-123"), tok, now); err == nil {
		t.Error("wrong key accepted")
	}
}

func TestJWTAlgorithmIsPinned(t *testing.T) {
	enc := base64.RawURLEncoding
	payload := enc.EncodeToString([]byte(`{"userId":"u","iat":1,"exp":4000000000,"jti":"j"}`))
	for _, alg := range []string{"none", "HS512", "hs256", ""} {
		head := enc.EncodeToString([]byte(`{"alg":"` + alg + `","typ":"JWT"}`))
		input := head + "." + payload
		forged := input + "." + enc.EncodeToString(sign(secret, input))
		if _, err := VerifyJWT(secret, forged, time.Unix(100, 0)); err == nil {
			t.Errorf("alg %q accepted", alg)
		}
	}
	if _, err := VerifyJWT(secret, enc.EncodeToString([]byte(`{"alg":"none"}`))+"."+payload+".", time.Unix(100, 0)); err == nil {
		t.Error("unsigned token accepted")
	}
}

func TestJWTFromNode(t *testing.T) {
	c, err := VerifyJWT(secret, nodeJWT, time.Unix(2000, 0))
	if err != nil || c.UserID != "u1" || c.Jti != "j" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestHashToken(t *testing.T) {
	// sha256("abc")
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(got)
	}
}
