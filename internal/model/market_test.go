package model

import (
	"testing"
	"time"
)

func TestQuote_HasBidAsk(t *testing.T) {
	cases := []struct {
		name     string
		bid, ask string
		want     bool
	}{
		{"both known", "100.1", "100.2", true},
		{"no bid", "0", "100.2", false},
		{"no ask", "100.1", "0", false},
		{"neither", "0", "0", false},
	}
	for _, c := range cases {
		q := Quote{Bid: MustDecimal(c.bid), Ask: MustDecimal(c.ask)}
		if got := q.HasBidAsk(); got != c.want {
			t.Errorf("%s: HasBidAsk() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUTC(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skipf("timezone data unavailable: %v", err)
	}
	local := time.Date(2026, 7, 19, 12, 0, 0, 0, loc)
	got := UTC(local)
	if got.Location() != time.UTC {
		t.Errorf("UTC() location = %v, want UTC", got.Location())
	}
	if !got.Equal(local) {
		t.Errorf("UTC() changed the instant: %v vs %v", got, local)
	}
}
