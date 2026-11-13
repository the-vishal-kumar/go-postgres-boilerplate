// Package loan holds the money, schedule and payment rules as pure functions,
// so they can be tested without a database or HTTP server.
package loan

import (
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MaxTerm is the longest loan, in weeks.
	MaxTerm = 520
	// MaxAmount is the largest single amount, in currency units.
	MaxAmount = 1_000_000_000
)

// AmountProblem says why an amount was refused. At most one is reported.
type AmountProblem int

const (
	AmountOK AmountProblem = iota
	AmountNotNumber
	AmountTooSmall
	AmountTooLarge
	AmountPrecision
)

// TermProblem says why a term was refused. Several can apply to one value.
type TermProblem int

const (
	TermNotNumber TermProblem = iota + 1
	TermNotInteger
	TermTooSmall
	TermTooLarge
)

var numeric = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

var (
	ratMin = big.NewRat(1, 100)
	ratMax = big.NewRat(MaxAmount, 1)
	ratHi  = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(401), nil))
	ratLo  = new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(401), nil))
)

// ParseNumber converts a decoded JSON value to an exact rational. It accepts
// JSON numbers and numeric strings (surrounding spaces allowed); anything else
// reports false. Exactness matters: 10.005 must stay 10.005, not 10.00499...
func ParseNumber(v any) (*big.Rat, bool) {
	var s string
	switch t := v.(type) {
	case json.Number:
		s = t.String()
	case string:
		s = strings.TrimSpace(t)
	default:
		return nil, false
	}
	if !numeric.MatchString(s) {
		return nil, false
	}
	mantissa, exp := s, 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e > 400 || e < -400 {
			// Out of any range we accept; avoid building a giant rational.
			if strings.Trim(mantissa, "+-0.") == "" {
				return new(big.Rat), true
			}
			if err == nil && e < 0 || err != nil && strings.HasPrefix(s[i+1:], "-") {
				return new(big.Rat).Set(ratLo).Mul(ratLo, signOf(mantissa)), true
			}
			return new(big.Rat).Mul(ratHi, signOf(mantissa)), true
		}
		exp = e
	}
	r, ok := new(big.Rat).SetString(mantissa)
	if !ok {
		return nil, false
	}
	if exp != 0 {
		p := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(exp))), nil))
		if exp > 0 {
			r.Mul(r, p)
		} else {
			r.Quo(r, p)
		}
	}
	return r, true
}

func signOf(mantissa string) *big.Rat {
	if strings.HasPrefix(mantissa, "-") {
		return big.NewRat(-1, 1)
	}
	return big.NewRat(1, 1)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ParseAmount converts a decoded JSON value to integer cents. Extra precision
// is refused rather than rounded: rounding money is the client's decision.
func ParseAmount(v any) (int64, AmountProblem) {
	r, ok := ParseNumber(v)
	if !ok {
		return 0, AmountNotNumber
	}
	if r.Cmp(ratMin) < 0 {
		return 0, AmountTooSmall
	}
	if r.Cmp(ratMax) > 0 {
		return 0, AmountTooLarge
	}
	cents := new(big.Rat).Mul(r, big.NewRat(100, 1))
	if !cents.IsInt() {
		return 0, AmountPrecision
	}
	return cents.Num().Int64(), AmountOK
}

// ParseTerm converts a decoded JSON value to a whole number of weeks.
func ParseTerm(v any) (int, []TermProblem) {
	r, ok := ParseNumber(v)
	if !ok {
		return 0, []TermProblem{TermNotNumber}
	}
	var problems []TermProblem
	if !r.IsInt() {
		problems = append(problems, TermNotInteger)
	}
	if r.Cmp(big.NewRat(1, 1)) < 0 {
		problems = append(problems, TermTooSmall)
	}
	if r.Cmp(big.NewRat(MaxTerm, 1)) > 0 {
		problems = append(problems, TermTooLarge)
	}
	if len(problems) > 0 {
		return 0, problems
	}
	return int(r.Num().Int64()), nil
}

// FormatCents renders cents as a JSON number with no float drift and no
// trailing zeros, the way a JavaScript number prints: 1500 -> 15, 2250 -> 22.5.
func FormatCents(c int64) json.Number {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	whole, frac := c/100, c%100
	switch {
	case frac == 0:
		return json.Number(sign + strconv.FormatInt(whole, 10))
	case frac%10 == 0:
		return json.Number(sign + strconv.FormatInt(whole, 10) + "." + strconv.FormatInt(frac/10, 10))
	default:
		f := strconv.FormatInt(frac, 10)
		if frac < 10 {
			f = "0" + f
		}
		return json.Number(sign + strconv.FormatInt(whole, 10) + "." + f)
	}
}
