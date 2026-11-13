package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/auth"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/store"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/uuid"
)

const invalidCredentials = "Invalid email or password"

type principal struct {
	user      store.User
	sessionID string
}

// authenticate accepts "Authorization: Bearer <jwt>" only when the signature and
// expiry are valid AND a live session for the token still exists (sign-off
// deletes it).
func (a *App) authenticate(r *http.Request) (principal, error) {
	parts := strings.Split(r.Header.Get("Authorization"), " ")
	token := ""
	if len(parts) > 1 {
		token = parts[1]
	}
	if parts[0] != "Bearer" || token == "" {
		return principal{}, httpErr(http.StatusUnauthorized, "Missing access token")
	}
	invalid := httpErr(http.StatusUnauthorized, "Invalid access token")
	if _, err := auth.VerifyJWT(a.secret, token, a.now()); err != nil {
		return principal{}, invalid
	}
	sess, user, err := a.store.SessionUser(r.Context(), auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return principal{}, invalid
	}
	if err != nil {
		return principal{}, err
	}
	return principal{user: user, sessionID: sess.ID}, nil
}

func (a *App) signup(admin bool) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if admin && !a.cfg.AdminSignupEnabled {
			return httpErr(http.StatusForbidden, "Admin sign-up is disabled")
		}
		v := newValidator(stateOf(r.Context()))
		email, _ := v.str("email", emailRule)
		password, _ := v.str("password", newPasswordRule)
		first, _ := v.str("firstName", firstNameRule)
		last, _ := v.str("lastName", lastNameRule)
		if errs := v.finish(); len(errs) > 0 {
			return validationErr(errs)
		}

		_, err := a.store.UserByEmail(r.Context(), email)
		if err == nil {
			return httpErr(http.StatusConflict, "User already exists")
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		err = a.store.CreateUser(r.Context(), store.User{
			ID: uuid.New(), Email: email, PasswordHash: hash, FirstName: first, LastName: last, IsAdmin: admin,
		})
		if errors.Is(err, store.ErrDuplicate) {
			// Two sign-ups passed the pre-check together; the unique index decided.
			return httpErr(http.StatusConflict, "Resource already exists")
		}
		if err != nil {
			return err
		}
		writeNoContent(w)
		return nil
	}
}

// signin checks the password first and the role second, so a caller cannot
// learn whether an email belongs to an admin without knowing its password.
func (a *App) signin(requireAdmin bool) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		v := newValidator(stateOf(r.Context()))
		email, _ := v.str("email", emailRule)
		password, _ := v.str("password", anyPasswordRule)
		if errs := v.finish(); len(errs) > 0 {
			return validationErr(errs)
		}

		user, err := a.store.UserByEmail(r.Context(), email)
		if errors.Is(err, store.ErrNotFound) {
			// Spend the same scrypt time as a real check so response timing
			// does not reveal which emails are registered.
			_, _ = auth.HashPassword(password)
			return httpErr(http.StatusUnauthorized, invalidCredentials)
		}
		if err != nil {
			return err
		}
		if !auth.VerifyPassword(user.PasswordHash, password) {
			return httpErr(http.StatusUnauthorized, invalidCredentials)
		}
		if requireAdmin && !user.IsAdmin {
			return httpErr(http.StatusForbidden, "Admin access required")
		}

		ttl := time.Duration(a.cfg.UserTokenTTLMinutes) * time.Minute
		if requireAdmin {
			ttl = time.Duration(a.cfg.AdminTokenTTLMinutes) * time.Minute
		}
		now := a.now()
		token, err := auth.SignJWT(a.secret, user.ID, now, ttl)
		if err != nil {
			return err
		}
		if err := a.store.PruneExpiredSessions(r.Context(), user.ID); err != nil {
			return err
		}
		err = a.store.CreateSession(r.Context(), store.Session{
			ID: uuid.New(), UserID: user.ID, TokenHash: auth.HashToken(token), ValidTill: now.Add(ttl),
		})
		if err != nil {
			return err
		}
		writeData(w, http.StatusOK, struct {
			AccessToken string `json:"accessToken"`
		}{token})
		return nil
	}
}

func (a *App) signoff(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	if err := a.store.DeleteSession(r.Context(), p.sessionID); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

// resetPassword needs the current password, so a stolen token alone cannot take
// over the account, and ends every other session of the user.
func (a *App) resetPassword(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	v := newValidator(stateOf(r.Context()))
	current, _ := v.str("currentPassword", anyPasswordRule)
	next, _ := v.str("password", newPasswordRule)
	if errs := v.finish(); len(errs) > 0 {
		return validationErr(errs)
	}
	if !auth.VerifyPassword(p.user.PasswordHash, current) {
		return httpErr(http.StatusUnauthorized, "Current password is incorrect")
	}
	if auth.VerifyPassword(p.user.PasswordHash, next) {
		return httpErr(http.StatusForbidden, "New password cannot be same as Old password")
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	if err := a.store.ChangePassword(r.Context(), p.user.ID, hash, p.sessionID); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}
