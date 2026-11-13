package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/uuid"
)

// Claims is the JWT payload.
type Claims struct {
	UserID string `json:"userId"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
	Jti    string `json:"jti"`
}

// ErrInvalidToken covers every reason a token is refused; callers do not need
// to tell them apart and must not tell the client.
var ErrInvalidToken = errors.New("invalid token")

var (
	enc        = base64.RawURLEncoding
	headerPart = enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
)

func sign(secret []byte, signingInput string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signingInput))
	return m.Sum(nil)
}

// SignJWT issues an HS256 token for userID that expires ttl after now.
func SignJWT(secret []byte, userID string, now time.Time, ttl time.Duration) (string, error) {
	payload, err := json.Marshal(Claims{
		UserID: userID,
		Iat:    now.Unix(),
		Exp:    now.Add(ttl).Unix(),
		Jti:    uuid.New(),
	})
	if err != nil {
		return "", err
	}
	input := headerPart + "." + enc.EncodeToString(payload)
	return input + "." + enc.EncodeToString(sign(secret, input)), nil
}

// VerifyJWT checks the algorithm, signature and expiry of token.
func VerifyJWT(secret []byte, token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	headerJSON, err := enc.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
	}
	// Pinning the algorithm stops "alg: none" and key-confusion downgrades.
	if json.Unmarshal(headerJSON, &header) != nil || header.Alg != "HS256" {
		return Claims{}, ErrInvalidToken
	}
	got, err := enc.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, sign(secret, parts[0]+"."+parts[1])) {
		return Claims{}, ErrInvalidToken
	}
	payloadJSON, err := enc.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var c Claims
	if json.Unmarshal(payloadJSON, &c) != nil || c.UserID == "" || c.Exp <= now.Unix() {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

// HashToken returns the SHA-256 hex digest stored in place of a raw token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
