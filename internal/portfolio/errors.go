package portfolio

import (
	"errors"
	"fmt"

	"github.com/Dronnn/invest-robot/internal/model"
)

// ErrSessionSnapshotMissing is returned by DayPnL when no equity_snapshots
// row exists at or after the requested sessionStart. Call
// EnsureSessionStartSnapshot before DayPnL to establish one.
var ErrSessionSnapshotMissing = errors.New("portfolio: no equity snapshot at or after session start; call EnsureSessionStartSnapshot first")

// ErrCannotEstablishSessionSnapshot is returned by EnsureSessionStartSnapshot
// when no prior equity snapshot exists to roll forward and at least one open
// position exists that cannot be valued without quotes — MarkToMarket must
// run at least once before a session-start baseline can be established for a
// portfolio that already holds something.
var ErrCannotEstablishSessionSnapshot = errors.New("portfolio: cannot establish session-start snapshot: open positions exist and no prior equity snapshot to roll forward; call MarkToMarket first")

// InvalidFillError reports a structurally invalid FillApplication (bad
// side, non-positive qty/lot, negative price/fee, or missing identifiers).
// It is always a caller bug — the value could never represent a legitimate
// fill — never a trading outcome.
type InvalidFillError struct {
	Reason string
}

func (e *InvalidFillError) Error() string {
	return "portfolio: invalid fill application: " + e.Reason
}

// OversellError reports an attempt to sell more lots than the position
// currently holds. Phase 1 forbids shorting (DESIGN.md §14, config-off), so
// this is always rejected rather than driving the position negative.
type OversellError struct {
	InstrumentUID model.InstrumentUID
	Have          int64
	Want          int64
}

func (e *OversellError) Error() string {
	return fmt.Sprintf("portfolio: oversell on %s: have %d lot(s), want to sell %d", e.InstrumentUID, e.Have, e.Want)
}

// MissingQuoteError reports that one or more held positions could not be
// valued because no usable quote or instrument metadata was available.
// Returned by MarkToMarket and Summary instead of silently valuing a
// position at zero or a stale price — the caller decides how to proceed
// (e.g. skip the cycle, surface a TUI alert).
type MissingQuoteError struct {
	Instruments []model.InstrumentUID
}

func (e *MissingQuoteError) Error() string {
	return fmt.Sprintf("portfolio: cannot value %d held position(s), missing quote or instrument metadata: %v", len(e.Instruments), e.Instruments)
}
