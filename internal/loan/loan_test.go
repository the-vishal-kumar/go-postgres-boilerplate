package loan

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func num(s string) json.Number { return json.Number(s) }

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in    any
		cents int64
		prob  AmountProblem
	}{
		{num("100"), 10000, AmountOK},
		{num("22.5"), 2250, AmountOK},
		{"22.5", 2250, AmountOK},
		{"  7 ", 700, AmountOK},
		{"1e2", 10000, AmountOK},
		{".5", 50, AmountOK},
		{num("0.01"), 1, AmountOK},
		{num("1000000000"), 100000000000, AmountOK},
		{num("1.1"), 110, AmountOK},
		{num("0.29"), 29, AmountOK},
		{num("0"), 0, AmountTooSmall},
		{num("0.009"), 0, AmountTooSmall},
		{num("-5"), 0, AmountTooSmall},
		{num("1e-400"), 0, AmountTooSmall},
		{num("1000000000.01"), 0, AmountTooLarge},
		{num("1e999999999999"), 0, AmountTooLarge},
		{num("10.005"), 0, AmountPrecision},
		{num("30.001"), 0, AmountPrecision},
		{"lots", 0, AmountNotNumber},
		{"", 0, AmountNotNumber},
		{"1/2", 0, AmountNotNumber},
		{"0x10", 0, AmountNotNumber},
		{"NaN", 0, AmountNotNumber},
		{true, 0, AmountNotNumber},
		{nil, 0, AmountNotNumber},
		{map[string]any{}, 0, AmountNotNumber},
		{[]any{}, 0, AmountNotNumber},
	}
	for _, c := range cases {
		cents, prob := ParseAmount(c.in)
		if cents != c.cents || prob != c.prob {
			t.Errorf("ParseAmount(%#v) = %d, %d; want %d, %d", c.in, cents, prob, c.cents, c.prob)
		}
	}
}

func TestParseTerm(t *testing.T) {
	cases := []struct {
		in    any
		term  int
		probs []TermProblem
	}{
		{num("3"), 3, nil},
		{"7", 7, nil},
		{num("3.0"), 3, nil},
		{num("520"), 520, nil},
		{num("2.5"), 0, []TermProblem{TermNotInteger}},
		{num("0"), 0, []TermProblem{TermTooSmall}},
		{num("0.5"), 0, []TermProblem{TermNotInteger, TermTooSmall}},
		{num("521"), 0, []TermProblem{TermTooLarge}},
		{"abc", 0, []TermProblem{TermNotNumber}},
		{nil, 0, []TermProblem{TermNotNumber}},
	}
	for _, c := range cases {
		term, probs := ParseTerm(c.in)
		if term != c.term || len(probs) != len(c.probs) {
			t.Errorf("ParseTerm(%#v) = %d, %v; want %d, %v", c.in, term, probs, c.term, c.probs)
			continue
		}
		for i := range probs {
			if probs[i] != c.probs[i] {
				t.Errorf("ParseTerm(%#v) problems = %v; want %v", c.in, probs, c.probs)
			}
		}
	}
}

func TestFormatCents(t *testing.T) {
	cases := map[int64]string{0: "0", 1: "0.01", 5: "0.05", 10: "0.1", 100: "1", 1432: "14.32", 1500: "15", 2250: "22.5", 333333: "3333.33", 100000000000: "1000000000"}
	for in, want := range cases {
		if got := FormatCents(in); string(got) != want {
			t.Errorf("FormatCents(%d) = %s; want %s", in, got, want)
		}
	}
	b, _ := json.Marshal(struct {
		A json.Number `json:"a"`
	}{FormatCents(2250)})
	if string(b) != `{"a":22.5}` {
		t.Fatalf("marshal: %s", b)
	}
}

func sum(xs []int64) (s int64) {
	for _, x := range xs {
		s += x
	}
	return
}

func TestSplit(t *testing.T) {
	cases := []struct {
		cents int64
		term  int
		want  []int64
	}{
		{1000000, 3, []int64{333333, 333333, 333334}},
		{10000, 7, []int64{1428, 1428, 1428, 1428, 1428, 1428, 1432}},
		{7, 7, []int64{1, 1, 1, 1, 1, 1, 1}},
		{123456, 1, []int64{123456}},
	}
	for _, c := range cases {
		got := Split(c.cents, c.term)
		if len(got) != len(c.want) {
			t.Fatalf("Split(%d,%d) = %v", c.cents, c.term, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Split(%d,%d) = %v; want %v", c.cents, c.term, got, c.want)
				break
			}
		}
		if sum(got) != c.cents {
			t.Errorf("Split(%d,%d) sums to %d", c.cents, c.term, sum(got))
		}
	}
	for term := 1; term <= 520; term += 13 {
		if got := Split(99999999, term); sum(got) != 99999999 {
			t.Errorf("term %d does not add up", term)
		}
	}
}

func TestDueDates(t *testing.T) {
	now := time.Date(2026, 11, 13, 17, 45, 1, 5, time.FixedZone("x", 5*3600))
	d := DueDates(now, 2)
	// 17:45 at +05:00 is still 12:45 UTC on the 13th.
	if !d[0].Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) || !d[1].Equal(time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v", d)
	}
	if got := d[0].Format("2006-01-02T15:04:05.000Z"); got != "2026-11-20T00:00:00.000Z" {
		t.Fatal(got)
	}
}

func schedule(dues ...int64) []Instalment {
	out := make([]Instalment, len(dues))
	for i, d := range dues {
		out[i] = Instalment{ID: string(rune('a' + i)), Due: d, Status: StatusPending}
	}
	return out
}

func TestApplyPaymentSpreadsForward(t *testing.T) {
	out, err := ApplyPayment(StatusApproved, 9000, schedule(3000, 3000, 3000), 4500)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Changed) != 2 || out.LoanPaid {
		t.Fatalf("%+v", out)
	}
	a, b := out.Changed[0], out.Changed[1]
	if a.Due != 0 || a.Paid != 3000 || a.Status != StatusPaid || b.Due != 1500 || b.Paid != 1500 || b.Status != StatusPending {
		t.Fatalf("%+v", out.Changed)
	}
	// The next payment may equal the part-paid instalment.
	rest := []Instalment{b, {ID: "c", Due: 3000, Status: StatusPending}}
	out, err = ApplyPayment(StatusApproved, 9000, rest, 1500)
	if err != nil || len(out.Changed) != 1 || out.Changed[0].Status != StatusPaid || out.LoanPaid {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestApplyPaymentClearsLoan(t *testing.T) {
	out, err := ApplyPayment(StatusApproved, 10000, schedule(5000, 5000), 10000)
	if err != nil || !out.LoanPaid || len(out.Changed) != 2 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestApplyPaymentRefusalOrder(t *testing.T) {
	sched := schedule(3000, 3000, 3000)
	cases := []struct {
		name   string
		status string
		loan   int64
		pend   []Instalment
		amount int64
		want   Refusal
	}{
		{"pending loan beats everything", StatusPending, 9000, nil, 99999, ErrNotApproved},
		{"paid loan", StatusPaid, 9000, sched, 3000, ErrNotApproved},
		{"over loan amount beats no pending", StatusApproved, 9000, nil, 9001, ErrOverLoan},
		{"no pending", StatusApproved, 9000, nil, 100, ErrNoPending},
		{"below due", StatusApproved, 9000, sched, 2999, ErrBelowDue},
		{"over balance", StatusApproved, 9000, schedule(3000, 3000), 6001, ErrOverBalance},
	}
	for _, c := range cases {
		_, err := ApplyPayment(c.status, c.loan, c.pend, c.amount)
		var r Refusal
		if !errors.As(err, &r) || r != c.want {
			t.Errorf("%s: got %v; want %q", c.name, err, c.want)
		}
	}
}

func TestApplyPaymentDoesNotMutateInput(t *testing.T) {
	in := schedule(3000, 3000)
	if _, err := ApplyPayment(StatusApproved, 6000, in, 3000); err != nil {
		t.Fatal(err)
	}
	if in[0].Due != 3000 || in[0].Status != StatusPending {
		t.Fatalf("input changed: %+v", in)
	}
}
