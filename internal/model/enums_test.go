package model

import "testing"

func TestCandleInterval_ParseStringRoundTrip(t *testing.T) {
	valid := []CandleInterval{Interval1m, Interval5m, Interval15m, Interval1h, Interval1d}
	for _, iv := range valid {
		got, err := ParseCandleInterval(iv.String())
		if err != nil || got != iv {
			t.Errorf("ParseCandleInterval(%q) = %q, %v", iv, got, err)
		}
		if !iv.Valid() {
			t.Errorf("%q reported invalid", iv)
		}
	}
	for _, bad := range []string{"", "2m", "1M", "1d ", "day"} {
		if _, err := ParseCandleInterval(bad); err == nil {
			t.Errorf("ParseCandleInterval(%q) succeeded, want error", bad)
		}
	}
}

func TestSide_ParseStringRoundTrip(t *testing.T) {
	for _, s := range []Side{SideBuy, SideSell} {
		got, err := ParseSide(s.String())
		if err != nil || got != s {
			t.Errorf("ParseSide(%q) = %q, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "BUY", "long", "b"} {
		if _, err := ParseSide(bad); err == nil {
			t.Errorf("ParseSide(%q) succeeded, want error", bad)
		}
	}
}

func TestOrderType_ParseStringRoundTrip(t *testing.T) {
	for _, o := range []OrderType{OrderMarket, OrderLimit} {
		got, err := ParseOrderType(o.String())
		if err != nil || got != o {
			t.Errorf("ParseOrderType(%q) = %q, %v", o, got, err)
		}
	}
	for _, bad := range []string{"", "stop", "MARKET"} {
		if _, err := ParseOrderType(bad); err == nil {
			t.Errorf("ParseOrderType(%q) succeeded, want error", bad)
		}
	}
}

func TestTimeInForce_ParseStringRoundTrip(t *testing.T) {
	for _, tif := range []TimeInForce{TIFDay, TIFIOC} {
		got, err := ParseTimeInForce(tif.String())
		if err != nil || got != tif {
			t.Errorf("ParseTimeInForce(%q) = %q, %v", tif, got, err)
		}
	}
	for _, bad := range []string{"", "gtc", "fok"} {
		if _, err := ParseTimeInForce(bad); err == nil {
			t.Errorf("ParseTimeInForce(%q) succeeded, want error", bad)
		}
	}
}

func TestAction_ParseStringRoundTrip(t *testing.T) {
	for _, a := range []Action{ActionBuy, ActionSell, ActionHold, ActionClose} {
		got, err := ParseAction(a.String())
		if err != nil || got != a {
			t.Errorf("ParseAction(%q) = %q, %v", a, got, err)
		}
	}
	for _, bad := range []string{"", "flatten", "HOLD"} {
		if _, err := ParseAction(bad); err == nil {
			t.Errorf("ParseAction(%q) succeeded, want error", bad)
		}
	}
}

func TestIntentState_ParseStringRoundTrip(t *testing.T) {
	all := []IntentState{
		IntentNew, IntentSubmitted, IntentAcked, IntentFilled,
		IntentCanceled, IntentRejected, IntentUnknown,
	}
	for _, s := range all {
		got, err := ParseIntentState(s.String())
		if err != nil || got != s {
			t.Errorf("ParseIntentState(%q) = %q, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "done", "NEW"} {
		if _, err := ParseIntentState(bad); err == nil {
			t.Errorf("ParseIntentState(%q) succeeded, want error", bad)
		}
	}
}
