package loan

import "time"

// Split divides cents into term instalments: all equal except the last, which
// absorbs the remainder so they add up exactly (10000 over 3 is 3333, 3333, 3334).
func Split(cents int64, term int) []int64 {
	out := make([]int64, term)
	base := cents / int64(term)
	for i := 0; i < term-1; i++ {
		out[i] = base
	}
	out[term-1] = cents - base*int64(term-1)
	return out
}

// DueDates returns the weekly due dates: UTC midnight of today plus i weeks.
func DueDates(now time.Time, term int) []time.Time {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	out := make([]time.Time, term)
	for i := range out {
		out[i] = today.AddDate(0, 0, 7*(i+1))
	}
	return out
}
