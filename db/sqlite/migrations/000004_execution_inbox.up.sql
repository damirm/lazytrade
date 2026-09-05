CREATE TABLE execution_inbox (
    id TEXT PRIMARY KEY,
    exchange_account_id TEXT NOT NULL,
    source_family TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    payload_checksum TEXT NOT NULL,
    payload TEXT NOT NULL CHECK (json_valid(payload)),
    trading_day TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','applied')),
    received_at INTEGER NOT NULL,
    applied_at INTEGER,
    UNIQUE (exchange_account_id, source_family, dedupe_key)
);

CREATE INDEX execution_inbox_pending
ON execution_inbox(exchange_account_id, status, received_at, id);
