package decision

import (
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// Field names used in ActionError, exported as constants so callers can
// match on them without depending on exact message text.
const (
	FieldAction      = "action"
	FieldOrderType   = "order_type"
	FieldQuantity    = "quantity"
	FieldLimitPrice  = "limit_price"
	FieldTimeInForce = "time_in_force"
	FieldConfidence  = "confidence"
	FieldDuplicate   = "duplicate"
	FieldInstrument  = "instrument_uid"
)

// DefaultPriceSanityBandBps is the default limit-price sanity band used by
// ValidateSemantics: 1000 bps = 10% either side of the instrument's last
// close.
const DefaultPriceSanityBandBps int64 = 1000

// ActionError reports a single invalid action within a Response, identified
// by its index so the cycle can reject that one action and keep the rest.
type ActionError struct {
	Index   int                 // index into Response.Actions
	UID     model.InstrumentUID // the action's instrument, as given (may be unknown)
	Field   string              // one of the Field* constants
	Message string
}

func (e ActionError) Error() string {
	return fmt.Sprintf("decision: action[%d] instrument=%s field=%s: %s", e.Index, e.UID, e.Field, e.Message)
}

func newActionErr(index int, uid model.InstrumentUID, field, message string) ActionError {
	return ActionError{Index: index, UID: uid, Field: field, Message: message}
}

// ValidateShape checks a Response against the response schema alone, with
// no reference to the Request that produced it: action/order-type/
// time-in-force enum validity, quantity positive for buy/sell, limit_price
// present iff order_type is limit, confidence in [0,1], and no duplicate
// instrument+action pairs. A nil/empty result means the response is
// shape-valid.
func ValidateShape(resp Response) []ActionError {
	var errs []ActionError
	seen := make(map[actionKey]bool, len(resp.Actions))

	for i, a := range resp.Actions {
		validAction := a.Action.Valid()
		if !validAction {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldAction, fmt.Sprintf("invalid action %q", a.Action)))
		}

		if !a.OrderType.Valid() {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldOrderType, fmt.Sprintf("invalid order type %q", a.OrderType)))
		} else {
			wantLimitPrice := a.OrderType == model.OrderLimit
			hasLimitPrice := a.LimitPrice != nil
			if wantLimitPrice != hasLimitPrice {
				errs = append(errs, newActionErr(i, a.InstrumentUID, FieldLimitPrice, "limit_price must be present if and only if order_type is limit"))
			}
		}

		if (a.Action == model.ActionBuy || a.Action == model.ActionSell) && a.Quantity <= 0 {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldQuantity, "quantity must be positive for buy/sell"))
		}

		if !a.TimeInForce.Valid() {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldTimeInForce, fmt.Sprintf("invalid time in force %q", a.TimeInForce)))
		}

		if a.Confidence < 0 || a.Confidence > 1 {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldConfidence, fmt.Sprintf("confidence %v out of [0,1]", a.Confidence)))
		}

		if validAction {
			key := actionKey{uid: a.InstrumentUID, action: a.Action}
			if seen[key] {
				errs = append(errs, newActionErr(i, a.InstrumentUID, FieldDuplicate, fmt.Sprintf("duplicate %q action for this instrument", a.Action)))
			} else {
				seen[key] = true
			}
		}
	}
	return errs
}

type actionKey struct {
	uid    model.InstrumentUID
	action model.Action
}

// ValidateSemantics checks a Response against the Request that produced it:
// every instrument must be known, quantities must be positive whole lots for
// buy/sell, hold/close actions must carry neither a quantity nor a limit
// price, and limit prices must be tick-aligned and within a sanity band of
// the instrument's last close. It uses DefaultPriceSanityBandBps; use
// ValidateSemanticsWithBand for a configured band.
func ValidateSemantics(resp Response, req Request) []ActionError {
	return ValidateSemanticsWithBand(resp, req, DefaultPriceSanityBandBps)
}

// ValidateSemanticsWithBand is ValidateSemantics with an explicit limit-price
// sanity band, in basis points of the instrument's last close.
func ValidateSemanticsWithBand(resp Response, req Request, bandBps int64) []ActionError {
	var errs []ActionError
	byUID := make(map[model.InstrumentUID]InstrumentContext, len(req.Instruments))
	for _, instr := range req.Instruments {
		byUID[instr.UID] = instr
	}

	for i, a := range resp.Actions {
		instr, known := byUID[a.InstrumentUID]
		if !known {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldInstrument, "instrument not present in request"))
			continue // nothing else here can be checked meaningfully without instrument context
		}

		if (a.Action == model.ActionBuy || a.Action == model.ActionSell) && a.Quantity <= 0 {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldQuantity, "quantity must be a positive whole number of lots"))
		}

		if (a.Action == model.ActionHold || a.Action == model.ActionClose) && (a.Quantity != 0 || a.LimitPrice != nil) {
			errs = append(errs, newActionErr(i, a.InstrumentUID, FieldQuantity, fmt.Sprintf("%s actions must not carry a quantity or limit price", a.Action)))
		}

		if a.OrderType == model.OrderLimit && a.LimitPrice != nil {
			if err := checkLimitPrice(*a.LimitPrice, instr, bandBps); err != nil {
				errs = append(errs, newActionErr(i, a.InstrumentUID, FieldLimitPrice, err.Error()))
			}
		}
	}
	return errs
}

// checkLimitPrice verifies price is aligned to instr's price tick and falls
// within bandBps of instr's last close. A zero MinPriceIncrement or LastClose
// (missing data) skips the corresponding check rather than reporting a false
// positive.
func checkLimitPrice(price model.Decimal, instr InstrumentContext, bandBps int64) error {
	if instr.MinPriceIncrement.Sign() > 0 {
		aligned, err := price.RoundToIncrement(instr.MinPriceIncrement, model.Nearest)
		if err != nil {
			return fmt.Errorf("tick alignment check: %w", err)
		}
		if aligned.Cmp(price) != 0 {
			return fmt.Errorf("not aligned to tick size %s", instr.MinPriceIncrement)
		}
	}

	last := instr.Features.LastClose
	if last.Sign() > 0 {
		band, err := last.MulBps(bandBps)
		if err != nil {
			return fmt.Errorf("sanity band: %w", err)
		}
		lower, err := last.Sub(band)
		if err != nil {
			return fmt.Errorf("sanity band: %w", err)
		}
		upper, err := last.Add(band)
		if err != nil {
			return fmt.Errorf("sanity band: %w", err)
		}
		if price.Cmp(lower) < 0 || price.Cmp(upper) > 0 {
			return fmt.Errorf("limit price %s outside sanity band [%s, %s] of last close %s", price, lower, upper, last)
		}
	}
	return nil
}
