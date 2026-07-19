package execution

import (
	"regexp"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewClientOrderID_FormatAndUniqueness(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id, err := NewClientOrderID()
		if err != nil {
			t.Fatalf("NewClientOrderID: %v", err)
		}
		if !uuidV4Re.MatchString(id) {
			t.Fatalf("id %q is not a canonical v4 UUID", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate client order id %q after %d draws", id, i)
		}
		seen[id] = struct{}{}
	}
}
