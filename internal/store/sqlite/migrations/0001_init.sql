-- Phase 1 schema (DESIGN.md §5). Money/qty-price fields are TEXT decimals
-- (model.Decimal canonical form); share/lot counts are INTEGER; every
-- timestamp is a UTC RFC3339 TEXT column. news_items and analyst_data are
-- Phase 2 and are intentionally not created here.

CREATE TABLE instruments (
    uid                 TEXT PRIMARY KEY,
    figi                TEXT NOT NULL,
    ticker              TEXT NOT NULL,
    class_code          TEXT NOT NULL,
    lot                 INTEGER NOT NULL,
    min_price_increment TEXT NOT NULL,
    currency            TEXT NOT NULL,
    name                TEXT NOT NULL,
    cached_at           TEXT NOT NULL
);

-- Candle bars. The UNIQUE constraint doubles as the index for the primary
-- query path (an instrument+interval range scan over ts), so no separate
-- index is declared.
CREATE TABLE candles (
    id             INTEGER PRIMARY KEY,
    instrument_uid TEXT NOT NULL REFERENCES instruments (uid),
    interval       TEXT NOT NULL,
    open           TEXT NOT NULL,
    high           TEXT NOT NULL,
    low            TEXT NOT NULL,
    close          TEXT NOT NULL,
    volume         INTEGER NOT NULL,
    ts             TEXT NOT NULL,
    complete       INTEGER NOT NULL,
    UNIQUE (instrument_uid, interval, ts)
);

-- Append-only top-of-book snapshots; "latest" is queried via ORDER BY ts DESC
-- LIMIT 1 against the index below.
CREATE TABLE quotes (
    id             INTEGER PRIMARY KEY,
    instrument_uid TEXT NOT NULL REFERENCES instruments (uid),
    bid            TEXT NOT NULL,
    ask            TEXT NOT NULL,
    last           TEXT NOT NULL,
    ts             TEXT NOT NULL
);
-- id DESC mirrors the "latest" query's ORDER BY ts DESC, id DESC tie-breaker so
-- equal-ts snapshots resolve deterministically (newest row wins) index-covered.
CREATE INDEX idx_quotes_instrument_ts ON quotes (instrument_uid, ts DESC, id DESC);

CREATE TABLE feature_snapshots (
    id             INTEGER PRIMARY KEY,
    instrument_uid TEXT NOT NULL REFERENCES instruments (uid),
    as_of          TEXT NOT NULL,
    payload        TEXT NOT NULL
);
CREATE INDEX idx_feature_snapshots_instrument_asof ON feature_snapshots (instrument_uid, as_of DESC, id DESC);

CREATE TABLE cycles (
    id                   INTEGER PRIMARY KEY,
    started_at           TEXT NOT NULL,
    as_of                TEXT NOT NULL,
    mode                 TEXT NOT NULL,
    engine               TEXT NOT NULL,
    engine_version       TEXT NOT NULL,
    prompt_template_hash TEXT NOT NULL,
    config_snapshot      TEXT NOT NULL,
    status               TEXT NOT NULL
);
CREATE INDEX idx_cycles_started_at ON cycles (started_at DESC, id DESC);

CREATE TABLE decisions (
    id                INTEGER PRIMARY KEY,
    cycle_id          INTEGER NOT NULL REFERENCES cycles (id),
    instrument_uid    TEXT NOT NULL REFERENCES instruments (uid),
    action            TEXT NOT NULL,
    qty               INTEGER NOT NULL,
    order_type        TEXT NOT NULL,
    limit_price       TEXT,
    time_in_force     TEXT NOT NULL,
    rationale         TEXT NOT NULL,
    confidence        REAL NOT NULL,
    raw_response      TEXT,
    validation_status TEXT NOT NULL
);
CREATE INDEX idx_decisions_cycle ON decisions (cycle_id);
CREATE INDEX idx_decisions_instrument ON decisions (instrument_uid);

CREATE TABLE llm_calls (
    id          INTEGER PRIMARY KEY,
    cycle_id    INTEGER NOT NULL REFERENCES cycles (id),
    model       TEXT NOT NULL,
    request     TEXT NOT NULL,
    response    TEXT,
    duration_ms INTEGER NOT NULL,
    error       TEXT,
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_llm_calls_cycle ON llm_calls (cycle_id);

-- CHECK constraints mirror the model enums and the OrderIntent invariants so a
-- zero-value or corrupt row cannot persist and then fail only when read back
-- (side/type/tif/state must be known tokens; qty is in lots and is positive).
CREATE TABLE order_intents (
    client_order_id TEXT PRIMARY KEY,
    decision_id     INTEGER NOT NULL REFERENCES decisions (id),
    instrument_uid  TEXT NOT NULL REFERENCES instruments (uid),
    side            TEXT NOT NULL CHECK (side IN ('buy', 'sell')),
    qty             INTEGER NOT NULL CHECK (qty > 0),
    type            TEXT NOT NULL CHECK (type IN ('market', 'limit')),
    limit_price     TEXT,
    time_in_force   TEXT NOT NULL CHECK (time_in_force IN ('day', 'ioc')),
    state           TEXT NOT NULL CHECK (state IN ('new', 'submitted', 'acked', 'filled', 'canceled', 'rejected', 'unknown')),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX idx_order_intents_state ON order_intents (state);

-- Broker-reported view of an intent after submission/reconciliation; one row
-- per intent, upserted as the broker's view changes.
CREATE TABLE orders (
    client_order_id TEXT PRIMARY KEY REFERENCES order_intents (client_order_id),
    broker_order_id TEXT,
    status          TEXT NOT NULL,
    lots_executed   INTEGER NOT NULL,
    executed_price  TEXT,
    raw_status      TEXT,
    updated_at      TEXT NOT NULL
);
CREATE INDEX idx_orders_broker_order_id ON orders (broker_order_id);

CREATE TABLE fills (
    id              INTEGER PRIMARY KEY,
    order_intent_id TEXT NOT NULL REFERENCES order_intents (client_order_id),
    price           TEXT NOT NULL,
    qty             INTEGER NOT NULL,
    fee             TEXT NOT NULL,
    ts              TEXT NOT NULL
);
CREATE INDEX idx_fills_order_intent ON fills (order_intent_id, ts ASC, id ASC);

CREATE TABLE positions (
    instrument_uid TEXT PRIMARY KEY REFERENCES instruments (uid),
    qty            INTEGER NOT NULL,
    avg_price      TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

-- Cash movements. ref is a free-form pointer (e.g. a fill id) whose shape
-- depends on reason; it is intentionally not a foreign key.
CREATE TABLE cash_ledger (
    id       INTEGER PRIMARY KEY,
    ts       TEXT NOT NULL,
    delta    TEXT NOT NULL,
    currency TEXT NOT NULL,
    reason   TEXT NOT NULL,
    ref      TEXT
);
CREATE INDEX idx_cash_ledger_ts ON cash_ledger (ts DESC, id DESC);

CREATE TABLE equity_snapshots (
    id           INTEGER PRIMARY KEY,
    ts           TEXT NOT NULL,
    cash         TEXT NOT NULL,
    market_value TEXT NOT NULL,
    total        TEXT NOT NULL
);
CREATE INDEX idx_equity_snapshots_ts ON equity_snapshots (ts DESC, id DESC);

CREATE TABLE events (
    id      INTEGER PRIMARY KEY,
    ts      TEXT NOT NULL,
    level   TEXT NOT NULL,
    code    TEXT NOT NULL,
    payload TEXT
);
CREATE INDEX idx_events_ts ON events (ts DESC, id DESC);
