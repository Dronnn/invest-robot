package portfolio

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dronnn/invest-robot/internal/clock"
	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// Reason codes written to cash_ledger.reason — exactly DESIGN.md §5's
// documented fill|fee|deposit|dividend set (Phase 1 writes fill/fee/deposit
// only; dividend arrives with corporate actions, out of scope).
const (
	reasonFill    = "fill"
	reasonFee     = "fee"
	reasonDeposit = "deposit"
)

// Portfolio is the transactional owner of one account's cash, positions,
// fees, and PnL. It holds no mutable state of its own between calls — every
// method reads and writes through the sqlite.Querier it is given — so a
// single Portfolio value can be shared freely across goroutines and reused
// for the life of the process.
type Portfolio struct {
	clock    clock.Clock
	currency string
}

// New builds a Portfolio for one account settled in currency (e.g. "rub").
// Phase 1 is single-account, single-currency (DESIGN.md §14 already excludes
// multi-account/margin), so currency is fixed for the Portfolio's lifetime
// rather than threaded through every call.
func New(clk clock.Clock, currency string) *Portfolio {
	return &Portfolio{clock: clk, currency: currency}
}

// Init seeds the cash ledger with a starting balance. It is idempotent: if
// any cash_ledger row already exists (for any currency — this is a
// single-account portfolio, so any prior activity means "already seeded"),
// Init is a no-op. Call it once at first startup, before the first cycle.
func (p *Portfolio) Init(ctx context.Context, q sqlite.Querier, starting model.Decimal) error {
	existing, err := (sqlite.CashRepo{}).Recent(ctx, q, 1)
	if err != nil {
		return fmt.Errorf("portfolio: init: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	if _, err := (sqlite.CashRepo{}).Insert(ctx, q, sqlite.CashEntry{
		TS:       p.clock.Now(),
		Delta:    starting,
		Currency: p.currency,
		Reason:   reasonDeposit,
	}); err != nil {
		return fmt.Errorf("portfolio: init: seed starting cash: %w", err)
	}
	return nil
}

// FillApplication is everything ApplyFill needs to account for one
// execution. Its field set is a fixed cross-package contract with
// internal/execution (which defines its own identical-shaped type and
// adapts to this one at the wiring layer, since Go does not structurally
// unify distinct named struct types) — do not add, remove, or rename fields
// without coordinating that adapter.
//
// Fill.IntentID identifies the fills row this FillApplication accounts for;
// that row is written by internal/execution (order intent/fill records are
// execution's, per the layout both packages were briefed against — DESIGN.md
// §6's "fills applied transactionally by portfolio" is the accounting
// application, not the row insert) in the same transaction ApplyFill runs
// in, before or after calling ApplyFill — ApplyFill itself never reads or
// writes the fills table.
//
// Lot is the instrument's shares-per-lot at fill time (Fill.Qty is in lots,
// matching model.Fill's documented unit); it is carried here rather than
// looked up so ApplyFill never needs instrument metadata to price cash and
// position effects. LowFidelity flags a fill priced via the paper
// simulator's last-price fallback (DESIGN.md §7); ApplyFill accepts it for
// contract completeness but does not act on it — execution persists it on
// the fills row itself (fills.low_fidelity) before ApplyFill runs, and the
// cash/position math for a real fill does not depend on how it was priced.
type FillApplication struct {
	Fill          model.Fill
	InstrumentUID model.InstrumentUID
	Side          model.Side
	Lot           int64
	// Currency is the instrument's settlement currency. When set, it must
	// match the portfolio's fixed currency; a mismatch is rejected rather than
	// booked, so a fill in another currency can never post into the base-
	// currency ledger. An empty Currency skips the check (the caller is
	// asserting single-currency), keeping older call sites valid.
	Currency    string
	LowFidelity bool
}

// validate rejects a structurally invalid FillApplication before any I/O.
// currency is the portfolio's settlement currency, checked against
// fa.Currency when the latter is set.
func (fa FillApplication) validate(currency string) error {
	switch {
	case !fa.Side.Valid():
		return &InvalidFillError{Reason: fmt.Sprintf("unknown side %q", fa.Side)}
	case fa.InstrumentUID == "":
		return &InvalidFillError{Reason: "instrument uid must not be empty"}
	case fa.Fill.IntentID == "":
		return &InvalidFillError{Reason: "fill intent id must not be empty"}
	case fa.Fill.Qty <= 0:
		return &InvalidFillError{Reason: fmt.Sprintf("fill qty must be positive, got %d", fa.Fill.Qty)}
	case fa.Lot <= 0:
		return &InvalidFillError{Reason: fmt.Sprintf("lot must be positive, got %d", fa.Lot)}
	case fa.Fill.Price.Sign() < 0:
		return &InvalidFillError{Reason: "fill price must not be negative"}
	case fa.Fill.Fee.Sign() < 0:
		return &InvalidFillError{Reason: "fill fee must not be negative"}
	case fa.Currency != "" && !strings.EqualFold(fa.Currency, currency):
		return &InvalidFillError{Reason: fmt.Sprintf("fill currency %q does not match the account currency %q", fa.Currency, currency)}
	default:
		return nil
	}
}

// ApplyFill accounts for one execution inside the caller's transaction: it
// never opens its own (WithTx is the caller's job, per DESIGN.md §3 — "a
// fill and its portfolio effects commit in one SQLite transaction").
//
// It expects the fills row for fa.Fill.IntentID to already exist in the same
// transaction (written by internal/execution — see the FillApplication doc
// comment); ApplyFill itself never touches the fills table. It updates the
// position (weighted-average recompute on a buy, quantity reduction plus
// realized-PnL bookkeeping on a sell) and appends cash_ledger rows for the
// notional and the fee.
//
// A buy always writes two cash_ledger rows: reason=fill (delta =
// -price*qty*lot) and reason=fee (delta = -fee), both ref'd to the intent
// id, even when the fee is zero — every fill produces exactly one row of
// each reason, so a downstream "total fees" query never has to special-case
// absence.
//
// A sell writes the same two cash_ledger rows (reason=fill credits the
// notional, reason=fee debits the commission) plus one FillRepo.SetRealizedPnL
// call that sets fills.realized_pnl for this fill's row — the sell's PnL
// against the position's average price, computed here where the pre-fill
// average is known. This does not touch cash_ledger at all: realized PnL is
// informational (already fully captured by the fill/fee rows above), not a
// separate cash movement.
//
// A fill whose Currency is set and differs from the account currency is
// rejected with *InvalidFillError and writes nothing — a fill settled in
// another currency must never post into this single-currency ledger.
//
// Selling more lots than the position holds returns *OversellError and
// writes nothing (Phase 1 forbids shorting). A position that fully closes
// (resulting qty 0) is zeroed in place (qty=0, avg_price reset to zero), not
// deleted — PositionRepo exposes no delete operation.
func (p *Portfolio) ApplyFill(ctx context.Context, q sqlite.Querier, fa FillApplication) error {
	if err := fa.validate(p.currency); err != nil {
		return err
	}

	pos, found, err := (sqlite.PositionRepo{}).Get(ctx, q, fa.InstrumentUID)
	if err != nil {
		return fmt.Errorf("portfolio: apply fill: get position %s: %w", fa.InstrumentUID, err)
	}
	if !found {
		pos = model.Position{InstrumentUID: fa.InstrumentUID}
	}

	shares, ok := sharesFor(fa.Fill.Qty, fa.Lot)
	if !ok {
		return fmt.Errorf("portfolio: apply fill: shares overflow for %d lots of %d", fa.Fill.Qty, fa.Lot)
	}
	notional, err := fa.Fill.Price.MulInt(shares)
	if err != nil {
		return fmt.Errorf("portfolio: apply fill: notional overflow: %w", err)
	}

	now := p.clock.Now()

	switch fa.Side {
	case model.SideBuy:
		newQty := pos.Qty + fa.Fill.Qty
		newAvg, err := recomputeAvgPrice(pos.AvgPrice, pos.Qty, fa.Fill.Price, fa.Fill.Qty, newQty)
		if err != nil {
			return fmt.Errorf("portfolio: apply fill: %w", err)
		}
		pos.Qty = newQty
		pos.AvgPrice = newAvg
		pos.UpdatedAt = now

		if err := (sqlite.PositionRepo{}).Upsert(ctx, q, pos); err != nil {
			return fmt.Errorf("portfolio: apply fill: upsert position: %w", err)
		}
		if _, err := (sqlite.CashRepo{}).Insert(ctx, q, sqlite.CashEntry{
			TS: now, Delta: notional.Neg(), Currency: p.currency, Reason: reasonFill, Ref: fa.Fill.IntentID,
		}); err != nil {
			return fmt.Errorf("portfolio: apply fill: insert fill cash entry: %w", err)
		}
		if _, err := (sqlite.CashRepo{}).Insert(ctx, q, sqlite.CashEntry{
			TS: now, Delta: fa.Fill.Fee.Neg(), Currency: p.currency, Reason: reasonFee, Ref: fa.Fill.IntentID,
		}); err != nil {
			return fmt.Errorf("portfolio: apply fill: insert fee cash entry: %w", err)
		}

	case model.SideSell:
		if fa.Fill.Qty > pos.Qty {
			return &OversellError{InstrumentUID: fa.InstrumentUID, Have: pos.Qty, Want: fa.Fill.Qty}
		}

		pnlPerShare, err := fa.Fill.Price.Sub(pos.AvgPrice)
		if err != nil {
			return fmt.Errorf("portfolio: apply fill: realized pnl per share: %w", err)
		}
		pnl, err := pnlPerShare.MulInt(shares)
		if err != nil {
			return fmt.Errorf("portfolio: apply fill: realized pnl overflow: %w", err)
		}

		newQty := pos.Qty - fa.Fill.Qty
		pos.Qty = newQty
		if newQty == 0 {
			// Fully closed: reset the cost basis so a later re-entry starts
			// its own weighted average from zero rather than inheriting a
			// stale price (see recomputeAvgPrice — existingQty=0 makes this
			// correct regardless of what AvgPrice held before).
			pos.AvgPrice = model.Decimal{}
		}
		pos.UpdatedAt = now

		if err := (sqlite.PositionRepo{}).Upsert(ctx, q, pos); err != nil {
			return fmt.Errorf("portfolio: apply fill: upsert position: %w", err)
		}
		if _, err := (sqlite.CashRepo{}).Insert(ctx, q, sqlite.CashEntry{
			TS: now, Delta: notional, Currency: p.currency, Reason: reasonFill, Ref: fa.Fill.IntentID,
		}); err != nil {
			return fmt.Errorf("portfolio: apply fill: insert fill cash entry: %w", err)
		}
		if _, err := (sqlite.CashRepo{}).Insert(ctx, q, sqlite.CashEntry{
			TS: now, Delta: fa.Fill.Fee.Neg(), Currency: p.currency, Reason: reasonFee, Ref: fa.Fill.IntentID,
		}); err != nil {
			return fmt.Errorf("portfolio: apply fill: insert fee cash entry: %w", err)
		}
		if err := (sqlite.FillRepo{}).SetRealizedPnL(ctx, q, fa.Fill.IntentID, pnl); err != nil {
			return fmt.Errorf("portfolio: apply fill: set realized pnl: %w", err)
		}

	default:
		// Unreachable: fa.validate already rejected any side other than
		// buy/sell.
		return &InvalidFillError{Reason: fmt.Sprintf("unhandled side %q", fa.Side)}
	}

	return nil
}

// recomputeAvgPrice returns the new weighted-average entry price after
// adding a fill of qty lots at price to an existing position of
// existingQty lots at existingAvg (newQty must equal existingQty+qty).
//
// The lot size cancels out of the ratio: it is constant per instrument and
// enters both the existing and incoming cost-basis terms identically
// (existingAvg*existingQty*lot + price*qty*lot) / (existingQty*lot +
// qty*lot) == (existingAvg*existingQty + price*qty) / (existingQty+qty). So
// the weights used here are lot counts, not share counts, and this function
// needs no lot parameter at all.
//
// Rounding is half-away-from-zero at Decimal's native nano precision
// (model.RoundHalfAwayFromZero), matching the rounding mode the rest of the
// money paths use for non-exact divisions.
func recomputeAvgPrice(existingAvg model.Decimal, existingQty int64, price model.Decimal, qty int64, newQty int64) (model.Decimal, error) {
	if newQty <= 0 {
		return model.Decimal{}, fmt.Errorf("recompute avg price: non-positive resulting qty %d", newQty)
	}
	existingWeighted, err := existingAvg.MulInt(existingQty)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("recompute avg price: existing weight overflow: %w", err)
	}
	incomingWeighted, err := price.MulInt(qty)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("recompute avg price: incoming weight overflow: %w", err)
	}
	sum, err := existingWeighted.Add(incomingWeighted)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("recompute avg price: weighted sum overflow: %w", err)
	}
	avg, err := sum.MulRat(1, newQty, model.RoundHalfAwayFromZero)
	if err != nil {
		return model.Decimal{}, fmt.Errorf("recompute avg price: division overflow: %w", err)
	}
	return avg, nil
}

// sharesFor returns qtyLots*lot as a positive share count, or ok=false when
// either input is non-positive or the product overflows int64. Money math
// multiplies lots by lot size before pricing, and a wrapped int64 product
// would price a fill against a bogus share count; the overflow guard divides
// the product back out and rejects any value that cannot recover the quantity.
func sharesFor(qtyLots, lot int64) (int64, bool) {
	if qtyLots <= 0 || lot <= 0 {
		return 0, false
	}
	shares := qtyLots * lot
	if shares/lot != qtyLots {
		return 0, false
	}
	return shares, true
}
