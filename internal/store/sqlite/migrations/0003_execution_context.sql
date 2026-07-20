-- v3: durable execution context the paper simulator gates fills on, so its
-- safety survives a restart independently of the next Submit (DESIGN §7).

-- execution_session holds the single current trading-session window. A fill is
-- only attempted inside it; persisting it means resting orders no longer fill
-- against any observation after a restart, before a fresh Submit reinstalls the
-- window. The zero window (both bounds the zero time) is the documented
-- 24-hour-open default.
CREATE TABLE execution_session (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    session_start TEXT NOT NULL,
    session_end   TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- instrument_trading_status carries the authoritative per-instrument trading
-- permissions, so a suspended or side-disabled instrument is not filled. The
-- simulator enforces a present row; an instrument with no row is treated as
-- unrestricted (the cycle records a row only for instruments whose status it
-- knows).
CREATE TABLE instrument_trading_status (
    instrument_uid TEXT PRIMARY KEY REFERENCES instruments (uid),
    status         TEXT NOT NULL,
    buy_available  INTEGER NOT NULL,
    sell_available INTEGER NOT NULL,
    updated_at     TEXT NOT NULL
);
