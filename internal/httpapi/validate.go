package httpapi

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/loan"
	"github.com/the-vishal-kumar/go-postgres-boilerplate/internal/uuid"
)

// validator collects every problem in a request body, with Joi-style messages,
// so a client sees all of them in one 422.
type validator struct {
	body map[string]any
	errs []string
	used map[string]bool
	skip bool
}

func newValidator(st *reqState) *validator {
	v := &validator{used: map[string]bool{}}
	if !st.hasBody {
		v.body = map[string]any{}
		return v
	}
	m, ok := st.body.(map[string]any)
	if !ok {
		v.skip = true
		v.errs = append(v.errs, "request body must be a JSON object")
		return v
	}
	v.body = m
	return v
}

func (v *validator) fail(format string, args ...any) {
	v.errs = append(v.errs, fmt.Sprintf(format, args...))
}

type strRule struct {
	required bool
	trim     bool
	lower    bool
	email    bool
	min, max int
}

// str reads a string field. ok is false when anything about it was wrong.
func (v *validator) str(name string, r strRule) (string, bool) {
	if v.skip {
		return "", false
	}
	v.used[name] = true
	raw, present := v.body[name]
	if !present {
		if r.required {
			v.fail(`"%s" is required`, name)
		}
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		v.fail(`"%s" must be a string`, name)
		return "", false
	}
	if r.trim {
		s = strings.TrimSpace(s)
	}
	if r.lower {
		s = strings.ToLower(s)
	}
	if s == "" {
		v.fail(`"%s" is not allowed to be empty`, name)
		return "", false
	}
	good := true
	n := utf8.RuneCountInString(s)
	if r.email && !validEmail(s) {
		v.fail(`"%s" must be a valid email`, name)
		good = false
	}
	if r.min > 0 && n < r.min {
		v.fail(`"%s" length must be at least %d characters long`, name, r.min)
		good = false
	}
	if r.max > 0 && n > r.max {
		v.fail(`"%s" length must be less than or equal to %d characters long`, name, r.max)
		good = false
	}
	return s, good
}

// money reads an amount field and returns integer cents.
func (v *validator) money(name string) (int64, bool) {
	if v.skip {
		return 0, false
	}
	v.used[name] = true
	raw, present := v.body[name]
	if !present {
		v.fail(`"%s" is required`, name)
		return 0, false
	}
	cents, p := loan.ParseAmount(raw)
	switch p {
	case loan.AmountNotNumber:
		v.fail(`"%s" must be a number`, name)
	case loan.AmountTooSmall:
		v.fail(`"%s" must be greater than or equal to 0.01`, name)
	case loan.AmountTooLarge:
		v.fail(`"%s" must be less than or equal to %d`, name, loan.MaxAmount)
	case loan.AmountPrecision:
		v.fail(`"%s" must have no more than 2 decimal places`, name)
	default:
		return cents, true
	}
	return 0, false
}

// term reads the loan term in weeks.
func (v *validator) term(name string) (int, bool) {
	if v.skip {
		return 0, false
	}
	v.used[name] = true
	raw, present := v.body[name]
	if !present {
		v.fail(`"%s" is required`, name)
		return 0, false
	}
	n, problems := loan.ParseTerm(raw)
	for _, p := range problems {
		switch p {
		case loan.TermNotNumber:
			v.fail(`"%s" must be a number`, name)
		case loan.TermNotInteger:
			v.fail(`"%s" must be an integer`, name)
		case loan.TermTooSmall:
			v.fail(`"%s" must be greater than or equal to 1`, name)
		case loan.TermTooLarge:
			v.fail(`"%s" must be less than or equal to %d`, name, loan.MaxTerm)
		}
	}
	return n, len(problems) == 0
}

// finish reports keys the schema does not know, then returns all problems.
func (v *validator) finish() []string {
	if !v.skip {
		var extra []string
		for k := range v.body {
			if !v.used[k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		for _, k := range extra {
			v.fail(`"%s" is not allowed`, k)
		}
	}
	return v.errs
}

// validID checks a path id and returns it normalised, or the Joi-style message.
func validID(raw string) (string, string) {
	id := strings.TrimSpace(raw)
	if !uuid.Valid(id) {
		return "", `"id" must be a valid GUID`
	}
	return strings.ToLower(id), ""
}

// validEmail is a pragmatic check, not RFC 5322: exactly one @, a local part,
// and a dotted domain whose last label is at least two letters.
func validEmail(s string) bool {
	if strings.Count(s, "@") != 1 {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	local, domain, _ := strings.Cut(s, "@")
	if local == "" || !strings.Contains(domain, ".") {
		return false
	}
	labels := strings.Split(domain, ".")
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	last := labels[len(labels)-1]
	if len(last) < 2 {
		return false
	}
	for _, r := range last {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

var (
	emailRule       = strRule{required: true, trim: true, lower: true, email: true, max: 254}
	newPasswordRule = strRule{required: true, min: 8, max: 128}
	anyPasswordRule = strRule{required: true, max: 128}
	firstNameRule   = strRule{required: true, trim: true, min: 1, max: 100}
	lastNameRule    = strRule{trim: true, max: 100}
)
