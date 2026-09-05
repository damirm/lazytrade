CREATE TABLE order_commissions (
    order_id TEXT PRIMARY KEY REFERENCES orders(id),
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    asset TEXT NOT NULL,
    cumulative_amount TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    observed_at INTEGER NOT NULL
);

-- Existing orders are initialized lazily from order_executions using decimal
-- arithmetic in the repository. SQLite numeric aggregation would lose exact
-- decimal precision and is deliberately avoided here.
