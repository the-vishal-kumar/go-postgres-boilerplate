package uuid

import (
	"regexp"
	"testing"
)

func TestNewIsV4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := New()
		if !re.MatchString(id) {
			t.Fatalf("not a v4 uuid: %s", id)
		}
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
	}
}

func TestValid(t *testing.T) {
	good := []string{"00000000-0000-4000-8000-000000000000", "ABCDEF01-2345-6789-abcd-ef0123456789", "11111111-1111-1111-1111-111111111111"}
	for _, s := range good {
		if !Valid(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	bad := []string{"", "not-a-uuid", "00000000-0000-4000-8000-00000000000", "00000000_0000_4000_8000_000000000000", "g0000000-0000-4000-8000-000000000000", "00000000-0000-4000-8000-0000000000000"}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
