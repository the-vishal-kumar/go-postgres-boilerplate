package loan

// Loan and instalment states.
const (
	StatusPending  = "PENDING"
	StatusApproved = "APPROVED"
	StatusPaid     = "PAID"
)

// Refusal is a payment the rules do not allow. The text is the API message.
type Refusal string

func (r Refusal) Error() string { return string(r) }

const (
	ErrNotApproved Refusal = "Cannot make payment on Pending or Paid loan"
	ErrOverLoan    Refusal = "Cannot make a payment more than loan amount"
	ErrNoPending   Refusal = "Loan has no pending repayments"
	ErrBelowDue    Refusal = "Cannot make a payment less than due amount"
	ErrOverBalance Refusal = "Cannot make a payment more than total due amount"
)

// Instalment is the part of a schedule row a payment can change.
type Instalment struct {
	ID     string
	Due    int64
	Paid   int64
	Status string
}

// Outcome lists the instalments a payment changed and whether it cleared the loan.
type Outcome struct {
	Changed  []Instalment
	LoanPaid bool
}

// ApplyPayment spends amount (cents) on pending, which must be the loan's
// pending instalments in due order. Refusals are checked in a fixed order.
func ApplyPayment(loanStatus string, loanCents int64, pending []Instalment, amount int64) (Outcome, error) {
	if loanStatus != StatusApproved {
		return Outcome{}, ErrNotApproved
	}
	if amount > loanCents {
		return Outcome{}, ErrOverLoan
	}
	if len(pending) == 0 {
		return Outcome{}, ErrNoPending
	}
	if amount < pending[0].Due {
		return Outcome{}, ErrBelowDue
	}
	var balance int64
	for _, p := range pending {
		balance += p.Due
	}
	if amount > balance {
		return Outcome{}, ErrOverBalance
	}

	out := Outcome{LoanPaid: true}
	remaining := amount
	for _, p := range pending {
		if remaining > 0 {
			take := min(remaining, p.Due)
			remaining -= take
			p.Due -= take
			p.Paid += take
			if p.Due == 0 {
				p.Status = StatusPaid
			}
			out.Changed = append(out.Changed, p)
		}
		if p.Status != StatusPaid {
			out.LoanPaid = false
		}
	}
	return out, nil
}
