package portfolio

import (
	"context"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// PositionView is one held position, valued at the last known price — the
// shape internal/decision and internal/risk consume (their own
// decision.PositionView / risk.Position types are assembled from this one at
// the wiring layer, adding fields like Ticker this package has no reason to
// know about).
type PositionView struct {
	UID           model.InstrumentUID
	Qty           int64
	AvgPrice      model.Decimal
	LastPrice     model.Decimal
	UnrealizedPnL model.Decimal
}

// SummaryView is the read-only account snapshot other modules pull to build
// their own request/state contracts.
type SummaryView struct {
	Cash      model.Decimal
	Equity    model.Decimal
	Positions []PositionView
}

// Summary returns the current cash, mark-to-market equity, and per-position
// detail, priced from quotes. It persists nothing (contrast MarkToMarket,
// which is the same pricing pass but also records an equity_snapshots row)
// and applies the same missing-quote discipline: a held position with no
// usable quote or instrument metadata makes the whole call fail with
// *MissingQuoteError rather than report a partial or zero-valued position.
func (p *Portfolio) Summary(ctx context.Context, q sqlite.Querier, quotes map[model.InstrumentUID]model.Quote) (SummaryView, error) {
	cash, err := (sqlite.CashRepo{}).Balance(ctx, q, p.currency)
	if err != nil {
		return SummaryView{}, fmt.Errorf("portfolio: summary: cash balance: %w", err)
	}
	marketValue, views, err := p.valuePositions(ctx, q, quotes)
	if err != nil {
		return SummaryView{}, err
	}
	equity, err := cash.Add(marketValue)
	if err != nil {
		return SummaryView{}, fmt.Errorf("portfolio: summary: equity overflow: %w", err)
	}
	return SummaryView{Cash: cash, Equity: equity, Positions: views}, nil
}

// DayPnLResult splits DayPnL into its components. Realized is gross price PnL
// (no fees); Unrealized is the pure mark-to-market movement of open positions;
// Fees is the commission paid this session, reported on its own rather than
// buried in Unrealized. Total is the net equity change and is what feeds a
// single-figure consumer (e.g. risk.State.DayPnL) — it already nets the fees
// out, so Total == Realized + Unrealized - Fees.
type DayPnLResult struct {
	Realized   model.Decimal
	Unrealized model.Decimal
	Fees       model.Decimal
	Total      model.Decimal
}

// DayPnL reports today's profit/loss as of the latest equity snapshot,
// relative to sessionStart:
//
//	Total      = latestSnapshot.Total - sessionStartSnapshot.Total
//	Realized   = sum of fills.realized_pnl for every fill with ts >= sessionStart
//	Fees       = sum of fills.fee for every fill with ts >= sessionStart
//	Unrealized = Total - Realized + Fees
//
// Unrealized is deliberately the algebraic residual rather than a direct
// per-position computation: Total already captures every cash and
// mark-to-market change since sessionStart (assuming no external cash flows
// mid-session, which Phase 1 has none of — deposits only happen via Init).
// Total includes the commissions paid, which are a real cash outflow but not a
// mark-to-market movement, so they are added back after subtracting gross
// realized PnL — otherwise a flat round-trip's fees would surface as a phantom
// unrealized loss. The fees are reported on their own in Fees.
//
// DayPnL depends on an equity_snapshots row existing at or after
// sessionStart; call EnsureSessionStartSnapshot once near the start of the
// trading session before relying on DayPnL. Without one, DayPnL returns
// ErrSessionSnapshotMissing rather than guessing a baseline.
func (p *Portfolio) DayPnL(ctx context.Context, q sqlite.Querier, sessionStart time.Time) (DayPnLResult, error) {
	snaps, err := (sqlite.EquityRepo{}).Range(ctx, q, sessionStart, p.clock.Now())
	if err != nil {
		return DayPnLResult{}, fmt.Errorf("portfolio: day pnl: range equity snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return DayPnLResult{}, ErrSessionSnapshotMissing
	}
	sessionSnap := snaps[0] // ascending order: earliest snapshot at/after sessionStart

	latest, ok, err := (sqlite.EquityRepo{}).Latest(ctx, q)
	if err != nil {
		return DayPnLResult{}, fmt.Errorf("portfolio: day pnl: latest equity snapshot: %w", err)
	}
	if !ok {
		// Unreachable in practice (snaps non-empty implies at least one row
		// exists), kept as a defensive fallback rather than a panic.
		return DayPnLResult{}, ErrSessionSnapshotMissing
	}

	total, err := latest.Total.Sub(sessionSnap.Total)
	if err != nil {
		return DayPnLResult{}, fmt.Errorf("portfolio: day pnl: total delta overflow: %w", err)
	}

	// Bound the realized/fee window to the latest snapshot's timestamp, the
	// same instant Total is measured to. A fill recorded after the last equity
	// snapshot is not yet reflected in Total, so counting its fee/realized here
	// would make Total, Fees and Unrealized mutually inconsistent (a phantom
	// unrealized swing equal to the unaccounted fee). It is picked up once a
	// snapshot captures it.
	realized, fees, err := p.realizedAndFeesBetween(ctx, q, sessionStart, latest.TS)
	if err != nil {
		return DayPnLResult{}, err
	}

	unrealized, err := total.Sub(realized)
	if err != nil {
		return DayPnLResult{}, fmt.Errorf("portfolio: day pnl: unrealized residual overflow: %w", err)
	}
	unrealized, err = unrealized.Add(fees)
	if err != nil {
		return DayPnLResult{}, fmt.Errorf("portfolio: day pnl: unrealized residual overflow: %w", err)
	}

	return DayPnLResult{Realized: realized, Unrealized: unrealized, Fees: fees, Total: total}, nil
}

// realizedAndFeesBetween sums, across every fill with sessionStart <= ts <=
// until, both fills.realized_pnl (gross price PnL; nil on a buy fill,
// contributing nothing) and fills.fee (the commission on every fill, buy or
// sell). until is the latest equity-snapshot timestamp, so the window matches
// the interval Total is measured over — a fill after the last snapshot is not
// yet in Total and must not be in Realized/Fees either. FillRepo exposes no
// time-ranged read narrower than "every fill", so this walks the full table via
// Recent(ctx, q, -1) — a negative limit is SQLite's documented "no upper
// bound" — and filters/sums in Go. Acceptable at this project's scale (a
// personal trading robot's fills table is not going to reach a size where this
// is a bottleneck); if it ever became one, FillRepo would need a ranged query.
func (p *Portfolio) realizedAndFeesBetween(ctx context.Context, q sqlite.Querier, sessionStart, until time.Time) (realized, fees model.Decimal, err error) {
	fills, err := (sqlite.FillRepo{}).Recent(ctx, q, -1)
	if err != nil {
		return model.Decimal{}, model.Decimal{}, fmt.Errorf("portfolio: day pnl: recent fills: %w", err)
	}
	for _, f := range fills {
		if f.TS.Before(sessionStart) || f.TS.After(until) {
			continue
		}
		if f.RealizedPnL != nil {
			v, err := realized.Add(*f.RealizedPnL)
			if err != nil {
				return model.Decimal{}, model.Decimal{}, fmt.Errorf("portfolio: day pnl: realized sum overflow: %w", err)
			}
			realized = v
		}
		v, err := fees.Add(f.Fee)
		if err != nil {
			return model.Decimal{}, model.Decimal{}, fmt.Errorf("portfolio: day pnl: fee sum overflow: %w", err)
		}
		fees = v
	}
	return realized, fees, nil
}

// EnsureSessionStartSnapshot guarantees an equity_snapshots row exists at or
// after at, establishing the baseline DayPnL needs. It is idempotent: if one
// already exists, this is a no-op.
//
// Otherwise it rolls the latest known snapshot forward — inserting a new row
// at time at with that snapshot's cash/market_value/total unchanged. This is
// financially correct as an opening baseline: the session's opening equity
// is whatever the account was last marked at (typically the prior session's
// close), and no market movement has been attributed to the new session yet.
//
// If no snapshot has ever been taken (true genesis) and the account holds no
// open positions, it seeds one from the cash balance alone (market value
// zero — there is nothing to price). If open positions exist with no prior
// snapshot to roll forward, it cannot safely establish a baseline without
// quotes and returns ErrCannotEstablishSessionSnapshot — call MarkToMarket
// first.
func (p *Portfolio) EnsureSessionStartSnapshot(ctx context.Context, q sqlite.Querier, at time.Time) error {
	now := p.clock.Now()
	if at.After(now) {
		return fmt.Errorf("portfolio: ensure session start snapshot: session start %s is in the future (now %s)", at, now)
	}

	existing, err := (sqlite.EquityRepo{}).Range(ctx, q, at, now)
	if err != nil {
		return fmt.Errorf("portfolio: ensure session start snapshot: range equity snapshots: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	latest, ok, err := (sqlite.EquityRepo{}).Latest(ctx, q)
	if err != nil {
		return fmt.Errorf("portfolio: ensure session start snapshot: latest equity snapshot: %w", err)
	}
	if ok {
		if _, err := (sqlite.EquityRepo{}).Insert(ctx, q, sqlite.EquitySnapshot{
			TS: at, Cash: latest.Cash, MarketValue: latest.MarketValue, Total: latest.Total,
		}); err != nil {
			return fmt.Errorf("portfolio: ensure session start snapshot: roll forward: %w", err)
		}
		return nil
	}

	positions, err := (sqlite.PositionRepo{}).List(ctx, q)
	if err != nil {
		return fmt.Errorf("portfolio: ensure session start snapshot: list positions: %w", err)
	}
	for _, pos := range positions {
		if pos.Qty != 0 {
			return ErrCannotEstablishSessionSnapshot
		}
	}

	cash, err := (sqlite.CashRepo{}).Balance(ctx, q, p.currency)
	if err != nil {
		return fmt.Errorf("portfolio: ensure session start snapshot: cash balance: %w", err)
	}
	if _, err := (sqlite.EquityRepo{}).Insert(ctx, q, sqlite.EquitySnapshot{
		TS: at, Cash: cash, MarketValue: model.Decimal{}, Total: cash,
	}); err != nil {
		return fmt.Errorf("portfolio: ensure session start snapshot: seed from cash: %w", err)
	}
	return nil
}
