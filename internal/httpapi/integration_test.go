package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/auth"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/migrate"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/store"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/uuid"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/migrations"
)

// These tests need a real PostgreSQL and run only when DATABASE_URL is set.
// Every test uses fresh emails, so they can share one database.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		os.Exit(m.Run())
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if err := migrate.Run(ctx, pool, migrations.FS, log); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	testPool = pool
	code := m.Run()
	pool.Close()
	os.Exit(code)
}

func needDB(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL is not set")
	}
}

func dbApp(t *testing.T, extra map[string]string) *App {
	t.Helper()
	needDB(t)
	return newTestApp(t, extra, store.New(testPool))
}

func email() string { return "it-" + uuid.New() + "@example.com" }

const password = "password123"

type actor struct {
	ID, Email, Token string
}

func register(t *testing.T, h http.Handler, admin bool) actor {
	t.Helper()
	e := email()
	signup, signin := "/auth/signup", "/auth/signin"
	if admin {
		signup, signin = "/auth/signup/admin", "/auth/signin/admin"
	}
	if r := call(t, h, "POST", signup, "", map[string]any{"email": e, "password": password, "firstName": "It"}); r.Status != 204 {
		t.Fatalf("signup: %d %s", r.Status, r.Raw)
	}
	r := call(t, h, "POST", signin, "", map[string]any{"email": e, "password": password})
	if r.Status != 200 {
		t.Fatalf("signin: %d %s", r.Status, r.Raw)
	}
	data := r.Body["data"].(map[string]any)
	token := data["accessToken"].(string)
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM users WHERE email = $1`, e).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return actor{ID: id, Email: e, Token: token}
}

func adminApp(t *testing.T) *App {
	return dbApp(t, map[string]string{"ADMIN_SIGNUP_ENABLED": "true"})
}

func newLoan(t *testing.T, h http.Handler, token string, amount, term any) string {
	t.Helper()
	r := call(t, h, "POST", "/loan", token, map[string]any{"amount": amount, "term": term})
	if r.Status != 201 {
		t.Fatalf("request loan: %d %s", r.Status, r.Raw)
	}
	return r.Body["data"].(map[string]any)["id"].(string)
}

func approvedLoan(t *testing.T, h http.Handler, amount, term any) (customer, admin actor, id string) {
	t.Helper()
	admin, customer = register(t, h, true), register(t, h, false)
	id = newLoan(t, h, customer.Token, amount, term)
	if r := call(t, h, "PUT", "/loan/"+id+"/approve", admin.Token, nil); r.Status != 204 {
		t.Fatalf("approve: %d %s", r.Status, r.Raw)
	}
	return customer, admin, id
}

func loanData(t *testing.T, h http.Handler, token, id string) map[string]any {
	t.Helper()
	r := call(t, h, "GET", "/loan/"+id, token, nil)
	if r.Status != 200 {
		t.Fatalf("get loan: %d %s", r.Status, r.Raw)
	}
	return r.Body["data"].(map[string]any)
}

func TestIntegrationFlow(t *testing.T) {
	h := adminApp(t).Handler()
	customer, _, id := approvedLoan(t, h, 90, 3)

	loan := loanData(t, h, customer.Token, id)
	if loan["status"] != "APPROVED" || loan["userId"] != customer.ID || loan["id"] != id {
		t.Fatalf("%v", loan)
	}
	if r := call(t, h, "PUT", "/loan/"+id+"/repayment", customer.Token, map[string]any{"amount": 45}); r.Status != 204 {
		t.Fatalf("pay: %d %s", r.Status, r.Raw)
	}
	var got [][3]any
	for _, rp := range loanData(t, h, customer.Token, id)["repayments"].([]any) {
		m := rp.(map[string]any)
		got = append(got, [3]any{m["status"], m["dueAmount"].(json.Number).String(), m["paidAmount"].(json.Number).String()})
	}
	want := [][3]any{{"PAID", "0", "30"}, {"PENDING", "15", "15"}, {"PENDING", "30", "0"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// 10000/3 split and the wire format of money.
	other := register(t, h, false)
	third := loanData(t, h, other.Token, newLoan(t, h, other.Token, 10000, 3))["repayments"].([]any)
	for i, due := range []string{"3333.33", "3333.33", "3333.34"} {
		if g := third[i].(map[string]any)["dueAmount"].(json.Number).String(); g != due {
			t.Errorf("instalment %d = %s; want %s", i, g, due)
		}
	}
	last := third[2].(map[string]any)
	if last["isLast"] != true || last["dueDate"].(string)[len("2026-11-20"):] != "T00:00:00.000Z" {
		t.Errorf("%v", last)
	}
}

func TestTokenIsStoredOnlyAsHash(t *testing.T) {
	h := dbApp(t, nil).Handler()
	u := register(t, h, false)
	rows, err := testPool.Query(context.Background(), `SELECT token_hash FROM sessions WHERE user_id = $1`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored == u.Token {
			t.Fatal("raw token stored")
		}
		if stored != auth.HashToken(u.Token) {
			t.Fatal("stored value is not the token's SHA-256")
		}
		n++
	}
	if n != 1 {
		t.Fatalf("sessions = %d", n)
	}
	var leaked int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE token_hash = $1`, u.Token).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("raw token lookup: %d %v", leaked, err)
	}
}

func TestExpiredSessionIsRefused(t *testing.T) {
	h := dbApp(t, nil).Handler()
	u := register(t, h, false)
	if r := call(t, h, "GET", "/loan", u.Token, nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	tag, err := testPool.Exec(context.Background(),
		`UPDATE sessions SET valid_till = now() - interval '1 minute' WHERE token_hash = $1`, auth.HashToken(u.Token))
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("%v %v", tag, err)
	}
	r := call(t, h, "GET", "/loan", u.Token, nil)
	if r.Status != 401 || r.Body["error"] != "Invalid access token" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	// Signing in again prunes the expired row for that user.
	call(t, h, "POST", "/auth/signin", "", map[string]any{"email": u.Email, "password": password})
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id = $1 AND valid_till <= now()`, u.ID).Scan(&n)
	if n != 0 {
		t.Errorf("expired sessions left: %d", n)
	}
}

func TestDatabaseFailureAnswers500WithRequestID(t *testing.T) {
	needDB(t)
	cfgPool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfgPool.Close() // every query now fails
	h := newTestApp(t, nil, store.New(cfgPool)).Handler()
	tok, _ := auth.SignJWT([]byte(testSecret), uuid.New(), time.Now(), time.Hour)

	r := call(t, h, "GET", "/loan", tok, nil)
	if r.Status != 500 || r.Body["error"] != "An error occurred" || r.Body["requestId"] != r.Header.Get("X-Request-Id") {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if len(r.Body) != 3 {
		t.Fatalf("body has extra fields: %s", r.Raw)
	}
	r = call(t, h, "POST", "/auth/signin", "", map[string]any{"email": email(), "password": password})
	if r.Status != 500 {
		t.Fatalf("signin: %d %s", r.Status, r.Raw)
	}
	if ready := call(t, h, "GET", "/health/ready", "", nil); ready.Status != 503 {
		t.Fatalf("ready: %d %s", ready.Status, ready.Raw)
	}
}

func TestReadinessFailsWhileShuttingDown(t *testing.T) {
	app := dbApp(t, nil)
	h := app.Handler()
	if r := call(t, h, "GET", "/health/ready", "", nil); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	app.BeginShutdown()
	r := call(t, h, "GET", "/health/ready", "", nil)
	if r.Status != 503 || r.Raw != `{"status":"not ready","checks":{"postgres":"ok","shutdown":"fail"}}` {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if live := call(t, h, "GET", "/health/live", "", nil); live.Status != 200 {
		t.Fatalf("live: %d", live.Status)
	}
}

func TestAdminSignupCanBeDisabled(t *testing.T) {
	h := dbApp(t, map[string]string{"ADMIN_SIGNUP_ENABLED": "false"}).Handler()
	e := email()
	r := call(t, h, "POST", "/auth/signup/admin", "", map[string]any{"email": e, "password": password, "firstName": "A"})
	if r.Status != 403 || r.Body["error"] != "Admin sign-up is disabled" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE email = $1`, e).Scan(&n)
	if n != 0 {
		t.Fatal("user was created")
	}
}

func TestApproverIsRecorded(t *testing.T) {
	h := adminApp(t).Handler()
	admin, customer := register(t, h, true), register(t, h, false)
	id := newLoan(t, h, customer.Token, 50, 2)

	var approver *string
	query := func() (status string) {
		if err := testPool.QueryRow(context.Background(), `SELECT status::text, approved_by::text FROM loans WHERE id = $1`, id).Scan(&status, &approver); err != nil {
			t.Fatal(err)
		}
		return
	}
	if query() != "PENDING" || approver != nil {
		t.Fatal("new loan must be pending without approver")
	}
	if r := call(t, h, "PUT", "/loan/"+id+"/approve", customer.Token, nil); r.Status != 403 || r.Body["error"] != "Admin access required" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if r := call(t, h, "PUT", "/loan/"+id+"/approve", admin.Token, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	if query() != "APPROVED" || approver == nil || *approver != admin.ID {
		t.Fatalf("approved_by = %v; want %s", approver, admin.ID)
	}
	if r := call(t, h, "PUT", "/loan/"+id+"/approve", admin.Token, nil); r.Status != 403 || r.Body["error"] != "Cannot approve an already approved or paid loan" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
	own := newLoan(t, h, admin.Token, 10, 1)
	if r := call(t, h, "PUT", "/loan/"+own+"/approve", admin.Token, nil); r.Status != 403 || r.Body["error"] != "Cannot approve your own loan request" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
}

func TestConcurrentApprovalsHaveOneWinner(t *testing.T) {
	h := adminApp(t).Handler()
	customer := register(t, h, false)
	admins := []actor{register(t, h, true), register(t, h, true), register(t, h, true), register(t, h, true)}
	id := newLoan(t, h, customer.Token, 100, 4)
	var wg sync.WaitGroup
	codes := make([]int, len(admins))
	for i, a := range admins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = call(t, h, "PUT", "/loan/"+id+"/approve", a.Token, nil).Status
		}()
	}
	wg.Wait()
	wins := 0
	for _, c := range codes {
		switch c {
		case 204:
			wins++
		case 403, 409:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if wins != 1 {
		t.Fatalf("winners = %d (%v)", wins, codes)
	}
}

func TestConcurrentPaymentsKeepBooksExact(t *testing.T) {
	h := adminApp(t).Handler()
	customer, _, id := approvedLoan(t, h, 100, 10)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = call(t, h, "PUT", "/loan/"+id+"/repayment", customer.Token, map[string]any{"amount": 10}).Status
		}()
	}
	wg.Wait()
	// The row lock serialises payments, so every one succeeds on fresh data.
	for i, c := range codes {
		if c != 204 {
			t.Errorf("payment %d = %d", i, c)
		}
	}
	var paid, due int64
	var pending int
	err := testPool.QueryRow(context.Background(),
		`SELECT sum(paid_cents), sum(due_cents), count(*) FILTER (WHERE status = 'PENDING') FROM repayments WHERE loan_id = $1`, id).
		Scan(&paid, &due, &pending)
	if err != nil {
		t.Fatal(err)
	}
	if paid != n*1000 || paid+due != 10000 || pending != 2 {
		t.Fatalf("paid=%d due=%d pending=%d", paid, due, pending)
	}

	// Two more payments clear the loan; the 11th is refused.
	for i := 0; i < 2; i++ {
		if r := call(t, h, "PUT", "/loan/"+id+"/repayment", customer.Token, map[string]any{"amount": 10}); r.Status != 204 {
			t.Fatalf("%d %s", r.Status, r.Raw)
		}
	}
	if loanData(t, h, customer.Token, id)["status"] != "PAID" {
		t.Fatal("loan should be PAID")
	}
	r := call(t, h, "PUT", "/loan/"+id+"/repayment", customer.Token, map[string]any{"amount": 1})
	if r.Status != 403 || r.Body["error"] != "Cannot make payment on Pending or Paid loan" {
		t.Fatalf("%d %s", r.Status, r.Raw)
	}
}

func TestSignupRaceHasOneWinner(t *testing.T) {
	h := dbApp(t, nil).Handler()
	body := map[string]any{"email": email(), "password": password, "firstName": "R"}
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = call(t, h, "POST", "/auth/signup", "", body).Status
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case 204:
			ok++
		case 409:
			conflict++
		}
	}
	if ok != 1 || conflict != len(codes)-1 {
		t.Fatalf("statuses %v", codes)
	}
}

func TestSessionLifecycle(t *testing.T) {
	h := dbApp(t, nil).Handler()
	u := register(t, h, false)
	second := call(t, h, "POST", "/auth/signin", "", map[string]any{"email": u.Email, "password": password}).Body["data"].(map[string]any)["accessToken"].(string)

	if r := call(t, h, "PUT", "/auth/password/reset", u.Token, map[string]any{"currentPassword": password, "password": password}); r.Status != 403 {
		t.Fatalf("same password: %d %s", r.Status, r.Raw)
	}
	if r := call(t, h, "PUT", "/auth/password/reset", u.Token, map[string]any{"currentPassword": "wrong-password", "password": "newPassword123"}); r.Status != 401 {
		t.Fatalf("wrong current: %d %s", r.Status, r.Raw)
	}
	if r := call(t, h, "PUT", "/auth/password/reset", u.Token, map[string]any{"currentPassword": password, "password": "newPassword123"}); r.Status != 204 {
		t.Fatalf("reset: %d %s", r.Status, r.Raw)
	}
	if r := call(t, h, "GET", "/loan", second, nil); r.Status != 401 {
		t.Fatalf("other session survived: %d", r.Status)
	}
	if r := call(t, h, "GET", "/loan", u.Token, nil); r.Status != 200 {
		t.Fatalf("current session lost: %d", r.Status)
	}
	if r := call(t, h, "POST", "/auth/signoff", u.Token, nil); r.Status != 204 {
		t.Fatalf("signoff: %d", r.Status)
	}
	if r := call(t, h, "GET", "/loan", u.Token, nil); r.Status != 401 {
		t.Fatalf("token alive after signoff: %d", r.Status)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	count := func() (n int) {
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return
	}
	before := count()
	if before < 1 {
		t.Fatal("no migration recorded")
	}
	for i := 0; i < 2; i++ {
		if err := migrate.Run(ctx, testPool, migrations.FS, log); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if after := count(); after != before {
		t.Fatalf("rows %d -> %d", before, after)
	}
	// Concurrent migrators serialise on the advisory lock.
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = migrate.Run(ctx, testPool, migrations.FS, log)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var v string
	if err := testPool.QueryRow(ctx, `SELECT version FROM schema_migrations ORDER BY version LIMIT 1`).Scan(&v); err != nil || v != "0001_init" {
		t.Fatalf("version %q %v", v, err)
	}
}
