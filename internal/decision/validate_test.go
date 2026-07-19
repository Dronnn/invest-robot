package decision

import (
	"testing"
	"time"

	"github.com/Dronnn/invest-robot/internal/features"
	"github.com/Dronnn/invest-robot/internal/model"
)

func validBuyAction() model.Decision {
	limit := model.MustDecimal("250.10")
	return model.Decision{
		InstrumentUID: "SBER-UID",
		Action:        model.ActionBuy,
		Quantity:      10,
		OrderType:     model.OrderLimit,
		LimitPrice:    &limit,
		TimeInForce:   model.TIFDay,
		Rationale:     "entry",
		Confidence:    0.7,
	}
}

func TestValidateShape(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(a *model.Decision)
		wantErr string // Field constant expected in the resulting error, "" means no error
	}{
		{"valid action passes", func(a *model.Decision) {}, ""},
		{"invalid action enum", func(a *model.Decision) { a.Action = "yolo" }, FieldAction},
		{"invalid order type enum", func(a *model.Decision) { a.OrderType = "stop" }, FieldOrderType},
		{"limit price missing for limit order", func(a *model.Decision) { a.LimitPrice = nil }, FieldLimitPrice},
		{"limit price present for market order", func(a *model.Decision) {
			a.OrderType = model.OrderMarket
			// LimitPrice stays set from validBuyAction — contradiction.
		}, FieldLimitPrice},
		{"non-positive quantity for buy", func(a *model.Decision) { a.Quantity = 0 }, FieldQuantity},
		{"negative quantity for sell", func(a *model.Decision) {
			a.Action = model.ActionSell
			a.Quantity = -1
		}, FieldQuantity},
		{"invalid time in force", func(a *model.Decision) { a.TimeInForce = "gtc" }, FieldTimeInForce},
		{"confidence below zero", func(a *model.Decision) { a.Confidence = -0.1 }, FieldConfidence},
		{"confidence above one", func(a *model.Decision) { a.Confidence = 1.1 }, FieldConfidence},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validBuyAction()
			tc.mutate(&a)
			errs := ValidateShape(Response{Actions: []model.Decision{a}})
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %+v", errs)
				}
				return
			}
			if !hasField(errs, tc.wantErr) {
				t.Fatalf("errors %+v do not contain field %q", errs, tc.wantErr)
			}
		})
	}
}

func TestValidateShape_DuplicateInstrumentActionPair(t *testing.T) {
	a := validBuyAction()
	resp := Response{Actions: []model.Decision{a, a}}
	errs := ValidateShape(resp)
	if !hasField(errs, FieldDuplicate) {
		t.Fatalf("expected a duplicate error, got %+v", errs)
	}
	// Only the second occurrence should be flagged.
	found := false
	for _, e := range errs {
		if e.Field == FieldDuplicate {
			if e.Index != 1 {
				t.Errorf("duplicate error Index = %d, want 1", e.Index)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no duplicate error found")
	}
}

func TestValidateShape_SameInstrumentDifferentActionIsNotDuplicate(t *testing.T) {
	buy := validBuyAction()
	hold := model.Decision{
		InstrumentUID: buy.InstrumentUID,
		Action:        model.ActionHold,
		OrderType:     model.OrderMarket,
		TimeInForce:   model.TIFDay,
		Confidence:    0.5,
	}
	errs := ValidateShape(Response{Actions: []model.Decision{buy, hold}})
	if hasField(errs, FieldDuplicate) {
		t.Fatalf("unexpected duplicate error: %+v", errs)
	}
}

func hasField(errs []ActionError, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
}

func semanticsRequest() Request {
	asOf := time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC)
	return Request{
		AsOf: asOf,
		Instruments: []InstrumentContext{
			{
				UID:               "SBER-UID",
				Ticker:            "SBER",
				Lot:               10,
				MinPriceIncrement: model.MustDecimal("0.01"),
				Features: features.Snapshot{
					UID:       "SBER-UID",
					AsOf:      asOf,
					LastClose: model.MustDecimal("250"),
				},
			},
		},
	}
}

func TestValidateSemantics(t *testing.T) {
	req := semanticsRequest()

	cases := []struct {
		name    string
		action  model.Decision
		wantErr string
	}{
		{
			name:    "valid buy passes",
			action:  validBuyAction(),
			wantErr: "",
		},
		{
			name: "unknown instrument",
			action: model.Decision{
				InstrumentUID: "UNKNOWN-UID",
				Action:        model.ActionBuy,
				Quantity:      1,
				OrderType:     model.OrderMarket,
				TimeInForce:   model.TIFDay,
			},
			wantErr: FieldInstrument,
		},
		{
			name: "hold with nonzero quantity",
			action: model.Decision{
				InstrumentUID: "SBER-UID",
				Action:        model.ActionHold,
				Quantity:      5,
				OrderType:     model.OrderMarket,
				TimeInForce:   model.TIFDay,
			},
			wantErr: FieldQuantity,
		},
		{
			name: "close with a limit price",
			action: func() model.Decision {
				p := model.MustDecimal("250")
				return model.Decision{
					InstrumentUID: "SBER-UID",
					Action:        model.ActionClose,
					OrderType:     model.OrderLimit,
					LimitPrice:    &p,
					TimeInForce:   model.TIFDay,
				}
			}(),
			wantErr: FieldQuantity,
		},
		{
			name: "limit price not tick-aligned",
			action: func() model.Decision {
				p := model.MustDecimal("250.105")
				return model.Decision{
					InstrumentUID: "SBER-UID",
					Action:        model.ActionBuy,
					Quantity:      10,
					OrderType:     model.OrderLimit,
					LimitPrice:    &p,
					TimeInForce:   model.TIFDay,
				}
			}(),
			wantErr: FieldLimitPrice,
		},
		{
			name: "non-positive limit price is rejected",
			action: func() model.Decision {
				p := model.MustDecimal("-250.10")
				return model.Decision{
					InstrumentUID: "SBER-UID",
					Action:        model.ActionBuy,
					Quantity:      10,
					OrderType:     model.OrderLimit,
					LimitPrice:    &p,
					TimeInForce:   model.TIFDay,
				}
			}(),
			wantErr: FieldLimitPrice,
		},
		{
			name: "limit price outside sanity band",
			action: func() model.Decision {
				p := model.MustDecimal("500.00") // 250 last close, +100% > 10% band
				return model.Decision{
					InstrumentUID: "SBER-UID",
					Action:        model.ActionBuy,
					Quantity:      10,
					OrderType:     model.OrderLimit,
					LimitPrice:    &p,
					TimeInForce:   model.TIFDay,
				}
			}(),
			wantErr: FieldLimitPrice,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateSemantics(Response{Actions: []model.Decision{tc.action}}, req)
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %+v", errs)
				}
				return
			}
			if !hasField(errs, tc.wantErr) {
				t.Fatalf("errors %+v do not contain field %q", errs, tc.wantErr)
			}
		})
	}
}

func TestValidateSemantics_PriceSanityBandIsConfigurable(t *testing.T) {
	req := semanticsRequest()
	p := model.MustDecimal("270") // 8% above 250 last close
	action := model.Decision{
		InstrumentUID: "SBER-UID",
		Action:        model.ActionBuy,
		Quantity:      10,
		OrderType:     model.OrderLimit,
		LimitPrice:    &p,
		TimeInForce:   model.TIFDay,
	}
	resp := Response{Actions: []model.Decision{action}}

	// Default 10% band accepts 8%.
	if errs := ValidateSemantics(resp, req); hasField(errs, FieldLimitPrice) {
		t.Fatalf("default band unexpectedly rejected an 8%% move: %+v", errs)
	}
	// A tighter 5% band rejects it.
	if errs := ValidateSemanticsWithBand(resp, req, 500); !hasField(errs, FieldLimitPrice) {
		t.Fatalf("5%% band should have rejected an 8%% move: %+v", errs)
	}
}

func TestValidateSemantics_NonPositiveLimitPriceRejectedWithMissingMetadata(t *testing.T) {
	// The exact bypass: with no tick and no last close, the tick and sanity-band
	// checks are both skipped, so nothing but the explicit positivity check
	// stands between a negative limit price and risk treating it as usable.
	req := semanticsRequest()
	req.Instruments[0].MinPriceIncrement = model.Decimal{}
	req.Instruments[0].Features.LastClose = model.Decimal{}
	p := model.MustDecimal("-1")
	action := model.Decision{
		InstrumentUID: "SBER-UID",
		Action:        model.ActionBuy,
		Quantity:      10,
		OrderType:     model.OrderLimit,
		LimitPrice:    &p,
		TimeInForce:   model.TIFDay,
	}
	errs := ValidateSemantics(Response{Actions: []model.Decision{action}}, req)
	if !hasField(errs, FieldLimitPrice) {
		t.Fatalf("a negative limit price must be rejected even with missing tick/last-close: %+v", errs)
	}
}

func TestValidateSemantics_ZeroMinPriceIncrementSkipsTickCheck(t *testing.T) {
	req := semanticsRequest()
	req.Instruments[0].MinPriceIncrement = model.Decimal{}
	p := model.MustDecimal("250.001") // would fail a 0.01 tick check
	action := model.Decision{
		InstrumentUID: "SBER-UID",
		Action:        model.ActionBuy,
		Quantity:      10,
		OrderType:     model.OrderLimit,
		LimitPrice:    &p,
		TimeInForce:   model.TIFDay,
	}
	errs := ValidateSemantics(Response{Actions: []model.Decision{action}}, req)
	if hasField(errs, FieldLimitPrice) {
		t.Fatalf("expected tick check to be skipped with a zero increment: %+v", errs)
	}
}
