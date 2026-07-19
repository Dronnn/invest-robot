package paper

import (
	"testing"

	"github.com/Dronnn/invest-robot/internal/model"
)

func q(bid, ask, last string) model.Quote {
	return model.Quote{Bid: mustDec(bid), Ask: mustDec(ask), Last: mustDec(last)}
}

func TestPriceFill_Table(t *testing.T) {
	cases := []struct {
		name      string
		slipBps   int64
		side      model.Side
		typ       model.OrderType
		limit     string // "" => market / nil limit
		quote     model.Quote
		tick      string
		wantPrice string
		wantLowFi bool
		wantOK    bool
	}{
		{
			name: "market buy fills at ask, no slippage",
			side: model.SideBuy, typ: model.OrderMarket,
			quote: q("99.90", "100.00", "99.95"), tick: "0.01",
			wantPrice: "100.00", wantOK: true,
		},
		{
			name:    "market buy adds adverse slippage then floors to tick",
			slipBps: 33, side: model.SideBuy, typ: model.OrderMarket,
			// 100.00 + 33bps = 100.33; Floor to 0.05 => 100.30 (rounds down).
			quote: q("99.90", "100.00", "99.95"), tick: "0.05",
			wantPrice: "100.30", wantOK: true,
		},
		{
			name: "market sell fills at bid, no slippage",
			side: model.SideSell, typ: model.OrderMarket,
			quote: q("99.90", "100.00", "99.95"), tick: "0.01",
			wantPrice: "99.90", wantOK: true,
		},
		{
			name:    "market sell subtracts adverse slippage then ceils to tick",
			slipBps: 33, side: model.SideSell, typ: model.OrderMarket,
			// 100.00 - 33bps = 99.67; Ceil to 0.05 => 99.70 (rounds up).
			quote: q("100.00", "100.10", "100.00"), tick: "0.05",
			wantPrice: "99.70", wantOK: true,
		},
		{
			name:    "marketable limit buy takes the better ask, ignoring slippage",
			slipBps: 50, side: model.SideBuy, typ: model.OrderLimit, limit: "100.00",
			quote: q("99.40", "99.50", "99.45"), tick: "0.01",
			wantPrice: "99.50", wantOK: true,
		},
		{
			name: "limit buy at exactly the ask fills at the limit",
			side: model.SideBuy, typ: model.OrderLimit, limit: "100.00",
			quote: q("99.90", "100.00", "99.95"), tick: "0.01",
			wantPrice: "100.00", wantOK: true,
		},
		{
			name: "limit buy that does not cross rests",
			side: model.SideBuy, typ: model.OrderLimit, limit: "100.00",
			quote: q("100.05", "100.10", "100.07"), tick: "0.01",
			wantOK: false,
		},
		{
			name:    "marketable limit sell takes the better bid, ignoring slippage",
			slipBps: 50, side: model.SideSell, typ: model.OrderLimit, limit: "100.00",
			quote: q("100.50", "100.60", "100.55"), tick: "0.01",
			wantPrice: "100.50", wantOK: true,
		},
		{
			name: "limit sell that does not cross rests",
			side: model.SideSell, typ: model.OrderLimit, limit: "100.00",
			quote: q("99.80", "99.90", "99.85"), tick: "0.01",
			wantOK: false,
		},
		{
			name: "buy falls back to last price and marks low fidelity",
			side: model.SideBuy, typ: model.OrderMarket,
			quote: q("0", "0", "99.95"), tick: "0.01",
			wantPrice: "99.95", wantLowFi: true, wantOK: true,
		},
		{
			name: "sell falls back to last price and marks low fidelity",
			side: model.SideSell, typ: model.OrderMarket,
			quote: q("0", "0", "99.95"), tick: "0.01",
			wantPrice: "99.95", wantLowFi: true, wantOK: true,
		},
		{
			name: "no bid, ask or last cannot fill",
			side: model.SideBuy, typ: model.OrderMarket,
			quote: q("0", "0", "0"), tick: "0.01",
			wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Simulator{slippageBps: c.slipBps}
			var limit *model.Decimal
			if c.limit != "" {
				limit = decPtr(c.limit)
			}
			price, lowFi, ok, err := s.priceFill(c.side, c.typ, limit, c.quote, model.MustDecimal(c.tick))
			if err != nil {
				t.Fatalf("priceFill: %v", err)
			}
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if price.Cmp(model.MustDecimal(c.wantPrice)) != 0 {
				t.Errorf("price = %s, want %s", price.String(), c.wantPrice)
			}
			if lowFi != c.wantLowFi {
				t.Errorf("lowFidelity = %v, want %v", lowFi, c.wantLowFi)
			}
		})
	}
}

func TestCommission(t *testing.T) {
	// 10 lots × 10 shares × 100.00 = 10000 notional; 0.0005 rate => 5.00 fee.
	num, den, err := parseRate("0.0005")
	if err != nil {
		t.Fatalf("parseRate: %v", err)
	}
	fee, err := commission(model.MustDecimal("100.00"), 10, 10, num, den)
	if err != nil {
		t.Fatalf("commission: %v", err)
	}
	if fee.String() != "5" {
		t.Errorf("fee = %s, want 5", fee.String())
	}

	// Zero rate yields a zero fee with no arithmetic.
	fee, err = commission(model.MustDecimal("100.00"), 1, 1, 0, 1)
	if err != nil {
		t.Fatalf("commission zero: %v", err)
	}
	if !fee.IsZero() {
		t.Errorf("zero-rate fee = %s, want 0", fee.String())
	}
}

func TestParseRate(t *testing.T) {
	cases := []struct {
		in      string
		wantNum int64
		wantDen int64
		wantErr bool
	}{
		{in: "0", wantNum: 0, wantDen: 1},
		{in: "0.0005", wantNum: 5, wantDen: 10000},
		{in: "0.001", wantNum: 1, wantDen: 1000},
		{in: "1", wantNum: 1, wantDen: 1},
		{in: "0.25", wantNum: 25, wantDen: 100},
		{in: "", wantErr: true},
		{in: "abc", wantErr: true},
		{in: "0.1234567890", wantErr: true}, // more than nine fractional digits
	}
	for _, c := range cases {
		num, den, err := parseRate(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRate(%q) = (%d,%d), want error", c.in, num, den)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRate(%q): %v", c.in, err)
			continue
		}
		if num != c.wantNum || den != c.wantDen {
			t.Errorf("parseRate(%q) = (%d,%d), want (%d,%d)", c.in, num, den, c.wantNum, c.wantDen)
		}
	}
}
