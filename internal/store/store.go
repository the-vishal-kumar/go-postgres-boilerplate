// Package store holds every SQL statement the service runs.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/loan"
)

// Errors the HTTP layer maps to responses.
var (
	ErrNotFound   = errors.New("not found")
	ErrDuplicate  = errors.New("duplicate")
	ErrNotOwner   = errors.New("not the owner")
	ErrOwnLoan    = errors.New("own loan")
	ErrNotPending = errors.New("loan is not pending")
	ErrConflict   = errors.New("changed by another request")
)

const uniqueViolation = "23505"

type User struct {
	ID           string
	Email        string
	PasswordHash string
	FirstName    string
	LastName     string
	IsAdmin      bool
}

type Session struct {
	ID        string
	UserID    string
	TokenHash string
	ValidTill time.Time
}

type Repayment struct {
	ID        string
	LoanID    string
	Seq       int
	DueCents  int64
	PaidCents int64
	IsLast    bool
	Status    string
	DueDate   time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Loan struct {
	ID          string
	UserID      string
	AmountCents int64
	Term        int
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Repayments  []Repayment
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == uniqueViolation
}

// CreateUser stores u; the email is expected lower-cased. A unique-index hit
// (two sign-ups racing past the pre-check) comes back as ErrDuplicate.
func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, first_name, last_name, is_admin)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		u.ID, u.Email, u.PasswordHash, u.FirstName, u.LastName, u.IsAdmin)
	if isUnique(err) {
		return ErrDuplicate
	}
	return err
}

const userColumns = `id::text, email, password_hash, first_name, last_name, is_admin`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName, &u.IsAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email))
}

// PruneExpiredSessions is cheap housekeeping run at sign-in, instead of a cron.
func (s *Store) PruneExpiredSessions(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND valid_till <= now()`, userID)
	return err
}

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, valid_till) VALUES ($1, $2, $3, $4)`,
		sess.ID, sess.UserID, sess.TokenHash, sess.ValidTill)
	return err
}

// SessionUser returns the live session for a token hash and its user in one
// round trip. ErrNotFound covers unknown, expired and orphaned sessions.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (Session, User, error) {
	var sess Session
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT s.id::text, s.user_id::text, s.token_hash, s.valid_till,
		        u.id::text, u.email, u.password_hash, u.first_name, u.last_name, u.is_admin
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = $1 AND s.valid_till > now()`, tokenHash).
		Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.ValidTill,
			&u.ID, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName, &u.IsAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	return sess, u, err
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// ChangePassword sets the new hash and ends every session except keepSessionID.
func (s *Store) ChangePassword(ctx context.Context, userID, hash, keepSessionID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, userID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateLoan writes the loan and its whole schedule in one transaction, so a
// failure never leaves a loan without instalments.
func (s *Store) CreateLoan(ctx context.Context, l Loan) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO loans (id, user_id, amount_cents, term) VALUES ($1, $2, $3, $4)`,
		l.ID, l.UserID, l.AmountCents, l.Term)
	for _, r := range l.Repayments {
		batch.Queue(`INSERT INTO repayments (id, loan_id, seq, due_cents, is_last, due_date)
		             VALUES ($1, $2, $3, $4, $5, $6)`,
			r.ID, l.ID, r.Seq, r.DueCents, r.IsLast, r.DueDate)
	}
	results := tx.SendBatch(ctx, batch)
	for i := 0; i <= len(l.Repayments); i++ {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	if err := results.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const (
	loanColumns = `id::text, user_id::text, amount_cents, term, status::text, created_at, updated_at`
	repayColumn = `id::text, loan_id::text, seq, due_cents, paid_cents, is_last, status::text, due_date, created_at, updated_at`
)

func scanLoan(row pgx.Row) (Loan, error) {
	var l Loan
	err := row.Scan(&l.ID, &l.UserID, &l.AmountCents, &l.Term, &l.Status, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Loan{}, ErrNotFound
	}
	return l, err
}

// attachRepayments loads the schedules for loans with one query. where selects
// the loans' repayments, using args as its parameters.
func (s *Store) attachRepayments(ctx context.Context, loans []Loan, where string, args ...any) error {
	if len(loans) == 0 {
		return nil
	}
	index := make(map[string]int, len(loans))
	for i, l := range loans {
		index[l.ID] = i
		loans[i].Repayments = []Repayment{}
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+repayColumn+` FROM repayments WHERE `+where+` ORDER BY loan_id, seq`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Repayment
		if err := rows.Scan(&r.ID, &r.LoanID, &r.Seq, &r.DueCents, &r.PaidCents, &r.IsLast, &r.Status,
			&r.DueDate, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return err
		}
		i := index[r.LoanID]
		loans[i].Repayments = append(loans[i].Repayments, r)
	}
	return rows.Err()
}

// LoansByUser returns the user's loans, oldest first, each with its schedule.
func (s *Store) LoansByUser(ctx context.Context, userID string) ([]Loan, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+loanColumns+` FROM loans WHERE user_id = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	loans := []Loan{}
	for rows.Next() {
		l, err := scanLoan(rows)
		if err != nil {
			return nil, err
		}
		loans = append(loans, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	err = s.attachRepayments(ctx, loans, `loan_id IN (SELECT id FROM loans WHERE user_id = $1)`, userID)
	return loans, err
}

func (s *Store) LoanByID(ctx context.Context, id string) (Loan, error) {
	l, err := scanLoan(s.pool.QueryRow(ctx, `SELECT `+loanColumns+` FROM loans WHERE id = $1`, id))
	if err != nil {
		return Loan{}, err
	}
	one := []Loan{l}
	if err := s.attachRepayments(ctx, one, `loan_id = $1`, id); err != nil {
		return Loan{}, err
	}
	return one[0], nil
}

// Approve marks a pending loan approved by adminID. The final UPDATE is
// conditional, so two admins approving at once cannot both succeed.
func (s *Store) Approve(ctx context.Context, loanID, adminID string) error {
	l, err := scanLoan(s.pool.QueryRow(ctx, `SELECT `+loanColumns+` FROM loans WHERE id = $1`, loanID))
	if err != nil {
		return err
	}
	if l.UserID == adminID {
		return ErrOwnLoan
	}
	if l.Status != loan.StatusPending {
		return ErrNotPending
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE loans SET status = 'APPROVED', approved_by = $2, updated_at = now()
		  WHERE id = $1 AND status = 'PENDING'`, loanID, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// Pay applies amountCents to the loan inside one transaction. The row lock on
// the loan serialises concurrent payments: the second waits, then reads the
// schedule the first one left, so money is never applied twice or lost.
func (s *Store) Pay(ctx context.Context, loanID, userID string, amountCents int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var owner, status string
	var total int64
	err = tx.QueryRow(ctx,
		`SELECT user_id::text, amount_cents, status::text FROM loans WHERE id = $1 FOR UPDATE`, loanID).
		Scan(&owner, &total, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrNotOwner
	}

	rows, err := tx.Query(ctx,
		`SELECT id::text, due_cents, paid_cents, status::text FROM repayments
		  WHERE loan_id = $1 AND status = 'PENDING' ORDER BY seq`, loanID)
	if err != nil {
		return err
	}
	var pending []loan.Instalment
	for rows.Next() {
		var in loan.Instalment
		if err := rows.Scan(&in.ID, &in.Due, &in.Paid, &in.Status); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	out, err := loan.ApplyPayment(status, total, pending, amountCents)
	if err != nil {
		return err
	}

	batch := &pgx.Batch{}
	for _, in := range out.Changed {
		batch.Queue(`UPDATE repayments SET due_cents = $2, paid_cents = $3, status = $4::repayment_status, updated_at = now()
		              WHERE id = $1`, in.ID, in.Due, in.Paid, in.Status)
	}
	if out.LoanPaid {
		batch.Queue(`UPDATE loans SET status = 'PAID', updated_at = now() WHERE id = $1`, loanID)
	}
	results := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	if err := results.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
