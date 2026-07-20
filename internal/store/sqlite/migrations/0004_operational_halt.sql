-- v4: a durable operational-halt latch. A present row (id = 1) means the robot
-- is halted — risk blocks all new buys until an operator clears it — engaged
-- when a fill settles the account below the configured cash floor (DESIGN §8).
-- Absent means running. The reason and timestamp record the first engagement.
CREATE TABLE operational_halt (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    reason     TEXT NOT NULL,
    engaged_at TEXT NOT NULL
);
