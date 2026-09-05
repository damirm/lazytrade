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
    state_version INTEGER NOT NULL CHECK (state_version > 0),
    state_payload TEXT NOT NULL CHECK (json_valid(state_payload)),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    runtime_status TEXT NOT NULL,
    status_reason TEXT NOT NULL DEFAULT '',
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
    reason_code TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
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
    payload TEXT NOT NULL CHECK (json_valid(payload)),
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
    status TEXT NOT NULL,
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
CREATE UNIQUE INDEX orders_exchange_id
    ON orders(exchange_account_id, exchange_order_id)
    WHERE exchange_order_id IS NOT NULL;

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

CREATE TABLE equity_snapshots (
    id TEXT PRIMARY KEY,
    strategy_id TEXT NOT NULL REFERENCES strategy_instances(id),
    trading_day TEXT NOT NULL,
    equity_amount TEXT NOT NULL,
    asset TEXT NOT NULL,
    snapshot_type TEXT NOT NULL,
    captured_at INTEGER NOT NULL,
    UNIQUE (strategy_id, trading_day, asset, snapshot_type)
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
CREATE UNIQUE INDEX pnl_execution_component
    ON pnl_events(source_execution_id, component_type)
    WHERE source_execution_id IS NOT NULL;

CREATE TABLE control_states (
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    state TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (scope_type, scope_id)
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
    complete INTEGER NOT NULL CHECK (complete IN (0,1)),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (strategy_id, trading_day, asset)
);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    actor TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    payload TEXT NOT NULL CHECK (json_valid(payload)),
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
    status TEXT NOT NULL,
    revision INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    metrics_payload TEXT,
    warnings_payload TEXT,
    error_code TEXT,
    error_message TEXT
);

CREATE TABLE backtest_artifacts (
    id TEXT PRIMARY KEY,
    backtest_run_id TEXT NOT NULL REFERENCES backtest_runs(id),
    artifact_type TEXT NOT NULL,
    path TEXT NOT NULL,
    content_checksum TEXT NOT NULL,
    size INTEGER NOT NULL,
    schema_version INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (backtest_run_id, artifact_type, path)
);

CREATE INDEX signals_strategy_cursor ON signals(strategy_id, cursor_timestamp, cursor_sequence);
CREATE INDEX intents_unresolved ON order_intents(exchange_account_id, status, created_at);
CREATE INDEX orders_open ON orders(exchange_account_id, status, submitted_at);
CREATE INDEX executions_order_time ON order_executions(order_id, executed_at);
CREATE INDEX pnl_strategy_day_asset ON pnl_events(strategy_id, occurred_at, asset);
CREATE INDEX audit_time_id ON audit_events(created_at, id);
CREATE INDEX audit_scope_time ON audit_events(scope_type, scope_id, created_at);
CREATE INDEX backtest_input ON backtest_runs(application_version, config_hash, dataset_checksum, seed);
