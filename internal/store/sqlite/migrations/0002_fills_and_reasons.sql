-- v2: order_intents.reason (rejected/canceled prose, DESIGN §12) and
-- fills.realized_pnl / fills.low_fidelity (set by portfolio and by whichever
-- Executor priced the fill, respectively). These were briefly folded into
-- 0001_init.sql before release; migrations are immutable once published, so
-- they land here instead.
--
-- The unique index enforces the full-fill-only model FillRepo.SetRealizedPnL
-- already assumed (Phase 1, DESIGN §7): at most one fill row per order
-- intent, so an update-by-intent-id is unambiguous.

ALTER TABLE order_intents ADD COLUMN reason TEXT;
ALTER TABLE fills ADD COLUMN realized_pnl TEXT;
ALTER TABLE fills ADD COLUMN low_fidelity INTEGER NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX idx_fills_order_intent_unique ON fills (order_intent_id);
