package portfolio

import (
	"context"
	"fmt"
	"time"

	"github.com/Dronnn/invest-robot/internal/model"
	"github.com/Dronnn/invest-robot/internal/store/sqlite"
)

// Equity is one mark-to-market valuation of the account: cash plus the
// current market value of every held position.
type Equity struct {
	Cash        model.Decimal
	MarketValue model.Decimal
	Total       model.Decimal
	AsOf        time.Time
}

// MarkToMarket values the account as of at using quotes, persists the
// result as a new equity_snapshots row, and returns it.
//
// quotes must carry a usable (non-zero Last) entry for every instrument with
// a non-zero position; if any held position cannot be priced —missing quote
// or missing instrument metadata — MarkToMarket returns *MissingQuoteError
// listing every affected instrument and persists nothing. This package never
// values a position at zero or at a stale price as a fallback (the caller
// decides how to handle the gap: skip the cycle, alert, retry).
func (p *Portfolio) MarkToMarket(ctx context.Context, q sqlite.Querier, quotes map[model.InstrumentUID]model.Quote, at time.Time) (Equity, error) {
	cash, err := (sqlite.CashRepo{}).Balance(ctx, q, p.currency)
	if err != nil {
		return Equity{}, fmt.Errorf("portfolio: mark to market: cash balance: %w", err)
	}
	marketValue, _, err := p.valuePositions(ctx, q, quotes)
	if err != nil {
		return Equity{}, err
	}
	total, err := cash.Add(marketValue)
	if err != nil {
		return Equity{}, fmt.Errorf("portfolio: mark to market: total overflow: %w", err)
	}

	if _, err := (sqlite.EquityRepo{}).Insert(ctx, q, sqlite.EquitySnapshot{
		TS: at, Cash: cash, MarketValue: marketValue, Total: total,
	}); err != nil {
		return Equity{}, fmt.Errorf("portfolio: mark to market: insert equity snapshot: %w", err)
	}

	return Equity{Cash: cash, MarketValue: marketValue, Total: total, AsOf: at}, nil
}

// valuePositions is the shared pricing pass behind both MarkToMarket and
// Summary: it lists every non-flat position, prices each one from quotes
// and the instrument's lot size, and returns the summed market value
// alongside a PositionView per position. Both callers apply the same
// missing-quote discipline (see MarkToMarket's doc comment) so a caller
// cannot get a silently-different answer depending on which entry point it
// used.
func (p *Portfolio) valuePositions(ctx context.Context, q sqlite.Querier, quotes map[model.InstrumentUID]model.Quote) (model.Decimal, []PositionView, error) {
	positions, err := (sqlite.PositionRepo{}).List(ctx, q)
	if err != nil {
		return model.Decimal{}, nil, fmt.Errorf("portfolio: list positions: %w", err)
	}

	instruments, err := (sqlite.InstrumentRepo{}).List(ctx, q)
	if err != nil {
		return model.Decimal{}, nil, fmt.Errorf("portfolio: list instruments: %w", err)
	}
	lotOf := make(map[model.InstrumentUID]int64, len(instruments))
	for _, in := range instruments {
		lotOf[in.UID] = in.Lot
	}

	var total model.Decimal
	var views []PositionView
	var missing []model.InstrumentUID

	for _, pos := range positions {
		if pos.Qty == 0 {
			continue // zeroed (fully closed) position: nothing to value or show
		}

		lot, hasLot := lotOf[pos.InstrumentUID]
		quote, hasQuote := quotes[pos.InstrumentUID]
		if !hasLot || !hasQuote || quote.Last.IsZero() {
			missing = append(missing, pos.InstrumentUID)
			continue
		}

		shares, ok := sharesFor(pos.Qty, lot)
		if !ok {
			missing = append(missing, pos.InstrumentUID)
			continue
		}
		value, err := quote.Last.MulInt(shares)
		if err != nil {
			missing = append(missing, pos.InstrumentUID)
			continue
		}
		pnlPerShare, err := quote.Last.Sub(pos.AvgPrice)
		if err != nil {
			missing = append(missing, pos.InstrumentUID)
			continue
		}
		pnl, err := pnlPerShare.MulInt(shares)
		if err != nil {
			missing = append(missing, pos.InstrumentUID)
			continue
		}
		newTotal, err := total.Add(value)
		if err != nil {
			missing = append(missing, pos.InstrumentUID)
			continue
		}
		total = newTotal

		views = append(views, PositionView{
			UID: pos.InstrumentUID, Qty: pos.Qty, AvgPrice: pos.AvgPrice, LastPrice: quote.Last, UnrealizedPnL: pnl,
		})
	}

	if len(missing) > 0 {
		return model.Decimal{}, nil, &MissingQuoteError{Instruments: missing}
	}
	return total, views, nil
}
