package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/loan"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/store"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/uuid"
)

const dateLayout = "2006-01-02T15:04:05.000Z"

type repaymentJSON struct {
	ID         string      `json:"id"`
	LoanID     string      `json:"loanId"`
	DueAmount  json.Number `json:"dueAmount"`
	PaidAmount json.Number `json:"paidAmount"`
	IsLast     bool        `json:"isLast"`
	Status     string      `json:"status"`
	DueDate    string      `json:"dueDate"`
	CreatedAt  int64       `json:"createdAt"`
	UpdatedAt  int64       `json:"updatedAt"`
}

type loanJSON struct {
	ID         string          `json:"id"`
	UserID     string          `json:"userId"`
	Amount     json.Number     `json:"amount"`
	Term       int             `json:"term"`
	Status     string          `json:"status"`
	CreatedAt  int64           `json:"createdAt"`
	UpdatedAt  int64           `json:"updatedAt"`
	Repayments []repaymentJSON `json:"repayments"`
}

func toLoanJSON(l store.Loan) loanJSON {
	out := loanJSON{
		ID: l.ID, UserID: l.UserID, Amount: loan.FormatCents(l.AmountCents), Term: l.Term, Status: l.Status,
		CreatedAt: l.CreatedAt.Unix(), UpdatedAt: l.UpdatedAt.Unix(),
		Repayments: make([]repaymentJSON, 0, len(l.Repayments)),
	}
	for _, r := range l.Repayments {
		out.Repayments = append(out.Repayments, repaymentJSON{
			ID: r.ID, LoanID: r.LoanID, DueAmount: loan.FormatCents(r.DueCents), PaidAmount: loan.FormatCents(r.PaidCents),
			IsLast: r.IsLast, Status: r.Status, DueDate: r.DueDate.UTC().Format(dateLayout),
			CreatedAt: r.CreatedAt.Unix(), UpdatedAt: r.UpdatedAt.Unix(),
		})
	}
	return out
}

func (a *App) requestLoan(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	v := newValidator(stateOf(r.Context()))
	cents, okAmount := v.money("amount")
	term, okTerm := v.term("term")
	errs := v.finish()
	// Every weekly instalment must be at least one cent.
	if len(errs) == 0 && okAmount && okTerm && cents < int64(term) {
		errs = append(errs, `"amount" must be at least 0.01 per week of "term"`)
	}
	if len(errs) > 0 {
		return validationErr(errs)
	}

	l := store.Loan{ID: uuid.New(), UserID: p.user.ID, AmountCents: cents, Term: term}
	dues := loan.Split(cents, term)
	dates := loan.DueDates(a.now(), term)
	for i := range dues {
		l.Repayments = append(l.Repayments, store.Repayment{
			ID: uuid.New(), Seq: i + 1, DueCents: dues[i], IsLast: i == term-1, DueDate: dates[i],
		})
	}
	if err := a.store.CreateLoan(r.Context(), l); err != nil {
		return err
	}
	writeData(w, http.StatusCreated, struct {
		ID string `json:"id"`
	}{l.ID})
	return nil
}

func (a *App) listLoans(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	loans, err := a.store.LoansByUser(r.Context(), p.user.ID)
	if err != nil {
		return err
	}
	out := make([]loanJSON, 0, len(loans))
	for _, l := range loans {
		out = append(out, toLoanJSON(l))
	}
	writeData(w, http.StatusOK, out)
	return nil
}

func (a *App) getLoan(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	id, msg := validID(r.PathValue("id"))
	if msg != "" {
		return validationErr([]string{msg})
	}
	l, err := a.store.LoanByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return httpErr(http.StatusNotFound, "Loan not found")
	}
	if err != nil {
		return err
	}
	if l.UserID != p.user.ID {
		return httpErr(http.StatusForbidden, "Authorization error")
	}
	writeData(w, http.StatusOK, toLoanJSON(l))
	return nil
}

func (a *App) approveLoan(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	if !p.user.IsAdmin {
		return httpErr(http.StatusForbidden, "Admin access required")
	}
	id, msg := validID(r.PathValue("id"))
	if msg != "" {
		return validationErr([]string{msg})
	}
	switch err := a.store.Approve(r.Context(), id, p.user.ID); {
	case err == nil:
		writeNoContent(w)
		return nil
	case errors.Is(err, store.ErrNotFound):
		return httpErr(http.StatusNotFound, "Loan not found")
	case errors.Is(err, store.ErrOwnLoan):
		return httpErr(http.StatusForbidden, "Cannot approve your own loan request")
	case errors.Is(err, store.ErrNotPending):
		return httpErr(http.StatusForbidden, "Cannot approve an already approved or paid loan")
	case errors.Is(err, store.ErrConflict):
		return httpErr(http.StatusConflict, "Loan was changed by another request, retry")
	default:
		return err
	}
}

func (a *App) makePayment(w http.ResponseWriter, r *http.Request) error {
	p, err := a.authenticate(r)
	if err != nil {
		return err
	}
	var errs []string
	id, msg := validID(r.PathValue("id"))
	if msg != "" {
		errs = append(errs, msg)
	}
	v := newValidator(stateOf(r.Context()))
	cents, _ := v.money("amount")
	errs = append(errs, v.finish()...)
	if len(errs) > 0 {
		return validationErr(errs)
	}

	var refusal loan.Refusal
	switch err := a.store.Pay(r.Context(), id, p.user.ID, cents); {
	case err == nil:
		writeNoContent(w)
		return nil
	case errors.Is(err, store.ErrNotFound):
		return httpErr(http.StatusNotFound, "Loan not found")
	case errors.Is(err, store.ErrNotOwner):
		return httpErr(http.StatusForbidden, "Authorization error")
	case errors.As(err, &refusal):
		return httpErr(http.StatusForbidden, refusal.Error())
	case errors.Is(err, store.ErrConflict):
		return httpErr(http.StatusConflict, "Loan was changed by another request, retry")
	default:
		return err
	}
}
