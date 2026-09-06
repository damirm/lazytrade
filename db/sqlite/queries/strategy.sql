-- name: GetStrategyRuntime :one
SELECT strategy_id, state_version, state_payload, revision,
       event_timestamp, event_priority, event_sequence,
       states.state_checksum, states.updated_at
FROM strategy_states AS states WHERE strategy_id = ?;

-- name: GetStrategyLifecycle :one
SELECT strategy_id, runtime_status, status_reason, updated_at
FROM strategy_lifecycle WHERE strategy_id = ?;

-- name: InsertStrategyInstance :exec
INSERT INTO strategy_instances
    (id, exchange_account_id, instrument_id, strategy_type, config_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: InsertStrategyLifecycle :exec
INSERT INTO strategy_lifecycle
    (strategy_id, runtime_status, status_reason, updated_at)
VALUES (?, 'stopped', '', ?);

-- name: InsertStrategyRuntime :exec
INSERT INTO strategy_states
    (strategy_id, state_version, state_payload, revision, runtime_status,
     status_reason, event_timestamp, event_priority, event_sequence, state_checksum, updated_at)
VALUES (?, ?, ?, 1, 'state', '', ?, ?, ?, ?, ?);

-- name: UpdateStrategyRuntime :execrows
UPDATE strategy_states
SET state_version=?, state_payload=?, revision=revision+1,
    event_timestamp=?, event_priority=?, event_sequence=?,
    state_checksum=?, updated_at=?
WHERE strategy_id=? AND revision=?;

-- name: UpdateStrategyLifecycle :execrows
UPDATE strategy_lifecycle
SET runtime_status=?, status_reason=?, updated_at=?
WHERE strategy_id=?;

-- name: InsertSignal :exec
INSERT INTO signals
    (id, strategy_id, exchange_account_id, instrument_id, action, order_type,
     quantity, limit_price, price_asset, reason_code, reason, created_at,
     cursor_timestamp, cursor_priority, cursor_sequence, ordinal, payload_checksum)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
