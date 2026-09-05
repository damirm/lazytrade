-- sqlc input snapshot. Keep byte-for-byte table/query semantics aligned with
-- db/sqlite/migrations/000001_initial.up.sql. Migrations are source of truth.
CREATE TABLE strategy_instances (
    id TEXT PRIMARY KEY,
    exchange_account_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL,
    strategy_type TEXT NOT NULL,
    config_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (exchange_account_id, instrument_id)
);

CREATE TABLE strategy_states (
    strategy_id TEXT PRIMARY KEY REFERENCES strategy_instances(id),
    state_version INTEGER NOT NULL,
    state_payload TEXT NOT NULL,
    revision INTEGER NOT NULL,
    runtime_status TEXT NOT NULL,
    status_reason TEXT NOT NULL,
    event_timestamp INTEGER NOT NULL,
    event_priority INTEGER NOT NULL,
    event_sequence INTEGER NOT NULL,
    state_checksum TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE signals (
    id TEXT PRIMARY KEY,
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    exchange_account_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL,
    action INTEGER NOT NULL,
    order_type INTEGER NOT NULL,
    quantity TEXT NOT NULL,
    limit_price TEXT,
    price_asset TEXT,
    reason_code TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    cursor_timestamp INTEGER NOT NULL,
    cursor_priority INTEGER NOT NULL,
    cursor_sequence INTEGER NOT NULL,
    ordinal INTEGER NOT NULL,
    payload_checksum TEXT NOT NULL,
    UNIQUE (strategy_id, cursor_timestamp, cursor_priority, cursor_sequence, ordinal)
);

CREATE TABLE risk_decisions (
    id TEXT PRIMARY KEY,
    signal_id TEXT NOT NULL UNIQUE REFERENCES signals(id),
    decision TEXT NOT NULL,
    reason_code TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE order_intents (
    id TEXT PRIMARY KEY,
    signal_id TEXT NOT NULL UNIQUE REFERENCES signals(id),
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    exchange_account_id TEXT NOT NULL,
    instrument_id TEXT NOT NULL,
    client_order_id TEXT NOT NULL UNIQUE,
    side INTEGER NOT NULL,
    order_type INTEGER NOT NULL,
    quantity TEXT NOT NULL,
    limit_price TEXT,
    price_asset TEXT,
    status TEXT NOT NULL CHECK (status IN ('ready','submitting','submitted','rejected','unknown','failed','not_submitted')),
    payload_checksum TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    order_intent_id TEXT NOT NULL UNIQUE REFERENCES order_intents(id),
    exchange_account_id TEXT NOT NULL,
    exchange_order_id TEXT,
    status TEXT NOT NULL,
    requested_quantity TEXT NOT NULL,
    filled_quantity TEXT NOT NULL,
    average_price TEXT,
    price_asset TEXT,
    submitted_at INTEGER,
    updated_at INTEGER NOT NULL
);

CREATE TABLE order_executions (
    id TEXT PRIMARY KEY,
    exchange_account_id TEXT NOT NULL,
    source_family TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    payload_checksum TEXT NOT NULL,
    order_id TEXT NOT NULL REFERENCES orders(id),
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    instrument_id TEXT NOT NULL,
    quantity TEXT NOT NULL,
    price TEXT NOT NULL,
    price_asset TEXT NOT NULL,
    commission_amount TEXT NOT NULL,
    commission_asset TEXT NOT NULL,
    executed_at INTEGER NOT NULL,
    received_at INTEGER NOT NULL,
    UNIQUE (exchange_account_id, source_family, dedupe_key)
);

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

CREATE TABLE order_commissions (
    order_id TEXT PRIMARY KEY REFERENCES orders(id),
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    asset TEXT NOT NULL,
    cumulative_amount TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    observed_at INTEGER NOT NULL
);

CREATE TABLE positions (
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    instrument_id TEXT NOT NULL,
    quantity TEXT NOT NULL,
    average_price TEXT NOT NULL,
    valuation_asset TEXT NOT NULL,
    revision INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (strategy_id, instrument_id)
);

CREATE TABLE pnl_events (
    id TEXT PRIMARY KEY,
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    event_type TEXT NOT NULL,
    amount TEXT NOT NULL,
    asset TEXT NOT NULL,
    source_execution_id TEXT REFERENCES order_executions(id),
    component_type TEXT,
    occurred_at INTEGER NOT NULL
);

CREATE TABLE daily_statistics (
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    trading_day TEXT NOT NULL,
    asset TEXT NOT NULL,
    realized_pnl TEXT NOT NULL,
    unrealized_pnl TEXT NOT NULL,
    total_pnl TEXT NOT NULL,
    commissions TEXT NOT NULL,
    funding TEXT NOT NULL,
    trade_count INTEGER NOT NULL,
    complete INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (strategy_id, trading_day, asset)
);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    actor TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE backtest_runs (
    id TEXT PRIMARY KEY,
    configured_run_id TEXT NOT NULL,
    strategy_id TEXT NOT NULL,
    application_version TEXT NOT NULL,
    config_hash TEXT NOT NULL,
    dataset_checksum TEXT NOT NULL,
    seed INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','completed','failed','cancelled')),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    metrics_payload TEXT CHECK (metrics_payload IS NULL OR json_valid(metrics_payload)),
    warnings_payload TEXT CHECK (warnings_payload IS NULL OR json_valid(warnings_payload)),
    error_code TEXT,
    error_message TEXT
);

CREATE TABLE backtest_artifacts (
    id TEXT PRIMARY KEY,
    backtest_run_id TEXT NOT NULL REFERENCES backtest_runs(id),
    artifact_type TEXT NOT NULL,
    path TEXT NOT NULL,
    content_checksum TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    schema_version INTEGER NOT NULL CHECK (schema_version >= 1),
    media_type TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (backtest_run_id, artifact_type, path)
);
