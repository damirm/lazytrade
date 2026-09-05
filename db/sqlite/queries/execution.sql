-- name: InsertOrderIntent :exec
INSERT INTO order_intents
    (id, signal_id, strategy_id, exchange_account_id, instrument_id,
     client_order_id, side, order_type, quantity, limit_price, price_asset,
     status, payload_checksum, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOrderIntentByClientOrderID :one
SELECT id, signal_id, strategy_id, exchange_account_id, instrument_id,
       client_order_id, side, order_type, quantity, limit_price, price_asset,
       status, payload_checksum, created_at, updated_at
FROM order_intents WHERE client_order_id = ?;

-- name: ListSignalsPendingRisk :many
SELECT s.id, s.strategy_id, s.exchange_account_id, s.instrument_id,
       s.action, s.order_type, s.quantity, s.limit_price, s.price_asset,
       s.reason_code, s.reason, s.created_at, s.cursor_timestamp,
       s.cursor_priority, s.cursor_sequence, s.ordinal
FROM signals s
LEFT JOIN risk_decisions r ON r.signal_id = s.id
WHERE r.signal_id IS NULL
ORDER BY s.cursor_timestamp, s.cursor_priority, s.cursor_sequence, s.ordinal
LIMIT ?;

-- name: ListSignalsPendingRiskByStrategy :many
SELECT s.id, s.strategy_id, s.exchange_account_id, s.instrument_id,
       s.action, s.order_type, s.quantity, s.limit_price, s.price_asset,
       s.reason_code, s.reason, s.created_at, s.cursor_timestamp,
       s.cursor_priority, s.cursor_sequence, s.ordinal
FROM signals s
WHERE s.strategy_id = ?
  AND NOT EXISTS (SELECT 1 FROM risk_decisions r WHERE r.signal_id = s.id)
ORDER BY s.cursor_timestamp, s.cursor_priority, s.cursor_sequence, s.ordinal, s.id
LIMIT ?;

-- name: InsertRiskDecision :exec
INSERT INTO risk_decisions
    (id, signal_id, decision, reason_code, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListPendingOrderIntents :many
SELECT id, signal_id, strategy_id, exchange_account_id, instrument_id,
       client_order_id, side, order_type, quantity, limit_price, price_asset,
       status, payload_checksum, created_at, updated_at
FROM order_intents
WHERE status IN ('ready', 'submitting', 'unknown')
ORDER BY created_at, id
LIMIT ?;

-- name: ListPendingOrderIntentsByStrategy :many
SELECT id, signal_id, strategy_id, exchange_account_id, instrument_id,
       client_order_id, side, order_type, quantity, limit_price, price_asset,
       status, payload_checksum, created_at, updated_at
FROM order_intents
WHERE strategy_id = ? AND status IN ('ready', 'submitting', 'unknown')
ORDER BY created_at, id
LIMIT ?;

-- name: ResolveOrderIntent :execrows
UPDATE order_intents
SET status = ?, updated_at = ?
WHERE id = ? AND status IN ('ready', 'submitting', 'unknown');

-- name: TransitionOrderIntent :execrows
UPDATE order_intents
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?;

-- name: InsertExchangeOrder :exec
INSERT INTO orders
    (id, order_intent_id, exchange_account_id, exchange_order_id, status,
     requested_quantity, filled_quantity, average_price, price_asset,
     submitted_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: FindOrderForExecution :one
SELECT o.id, o.requested_quantity, o.filled_quantity, oi.strategy_id, oi.instrument_id, oi.side
FROM orders o
JOIN order_intents oi ON oi.id = o.order_intent_id
WHERE o.exchange_account_id = ? AND o.exchange_order_id = ?;

-- name: FindOrderForCommission :one
SELECT o.id, oi.strategy_id
FROM orders o
JOIN order_intents oi ON oi.id = o.order_intent_id
WHERE o.exchange_account_id = ? AND o.exchange_order_id = ?;

-- name: GetOrderCommission :one
SELECT order_id, strategy_id, asset, cumulative_amount, revision, observed_at
FROM order_commissions
WHERE order_id = ?;

-- name: ListPersistedCommissionsForOrder :many
SELECT commission_amount, commission_asset, received_at
FROM order_executions
WHERE order_id = ?
ORDER BY executed_at, id;

-- name: InsertOrderCommission :execrows
INSERT INTO order_commissions
    (order_id, strategy_id, asset, cumulative_amount, revision, observed_at)
VALUES (?, ?, ?, ?, 1, ?)
ON CONFLICT(order_id) DO NOTHING;

-- name: AdvanceOrderCommission :execrows
UPDATE order_commissions
SET cumulative_amount = ?, revision = revision + 1, observed_at = ?
WHERE order_id = ? AND asset = ? AND cumulative_amount = ?;

-- name: InsertOrderExecution :execrows
INSERT INTO order_executions
    (id, exchange_account_id, source_family, dedupe_key, payload_checksum,
     order_id, strategy_id, instrument_id, quantity, price, price_asset,
     commission_amount, commission_asset, executed_at, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(exchange_account_id, source_family, dedupe_key) DO NOTHING;

-- name: InsertExecutionInbox :execrows
INSERT INTO execution_inbox
    (id, exchange_account_id, source_family, dedupe_key, payload_checksum,
     payload, trading_day, status, received_at, applied_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, NULL)
ON CONFLICT(exchange_account_id, source_family, dedupe_key) DO NOTHING;

-- name: GetExecutionInboxByDedupe :one
SELECT id, exchange_account_id, source_family, dedupe_key, payload_checksum,
       payload, trading_day, status, received_at, applied_at
FROM execution_inbox
WHERE exchange_account_id = ? AND source_family = ? AND dedupe_key = ?;

-- name: GetExecutionInboxByID :one
SELECT id, exchange_account_id, source_family, dedupe_key, payload_checksum,
       payload, trading_day, status, received_at, applied_at
FROM execution_inbox
WHERE id = ?;

-- name: ListPendingExecutionInbox :many
SELECT id, exchange_account_id, source_family, dedupe_key, payload_checksum,
       payload, trading_day, status, received_at, applied_at
FROM execution_inbox
WHERE exchange_account_id = ? AND status = 'pending'
ORDER BY received_at, id
LIMIT ?;

-- name: MarkExecutionInboxApplied :execrows
UPDATE execution_inbox
SET status = 'applied', applied_at = ?
WHERE id = ? AND status = 'pending';

-- name: GetExecutionChecksum :one
SELECT payload_checksum
FROM order_executions
WHERE exchange_account_id = ? AND source_family = ? AND dedupe_key = ?;

-- name: ListExecutionQuantitiesForOrder :many
SELECT quantity
FROM order_executions
WHERE order_id = ?
ORDER BY executed_at, id;

-- name: UpdateOrderAfterExecution :execrows
UPDATE orders
SET filled_quantity = ?, status = ?, updated_at = ?
WHERE id = ?;

-- name: GetPosition :one
SELECT strategy_id, instrument_id, quantity, average_price, valuation_asset,
       revision, updated_at
FROM positions
WHERE strategy_id = ? AND instrument_id = ?;

-- name: UpsertPosition :exec
INSERT INTO positions
    (strategy_id, instrument_id, quantity, average_price, valuation_asset,
     revision, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(strategy_id, instrument_id) DO UPDATE SET
    quantity = excluded.quantity,
    average_price = excluded.average_price,
    valuation_asset = excluded.valuation_asset,
    revision = positions.revision + 1,
    updated_at = excluded.updated_at;

-- name: InsertPnLEvent :exec
INSERT INTO pnl_events
    (id, strategy_id, event_type, amount, asset, source_execution_id,
     component_type, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDailyStatistics :one
SELECT strategy_id, trading_day, asset, realized_pnl, unrealized_pnl,
       total_pnl, commissions, funding, trade_count, complete, updated_at
FROM daily_statistics
WHERE strategy_id = ? AND trading_day = ? AND asset = ?;

-- name: UpsertDailyStatistics :exec
INSERT INTO daily_statistics
    (strategy_id, trading_day, asset, realized_pnl, unrealized_pnl,
     total_pnl, commissions, funding, trade_count, complete, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(strategy_id, trading_day, asset) DO UPDATE SET
    realized_pnl = excluded.realized_pnl,
    unrealized_pnl = excluded.unrealized_pnl,
    total_pnl = excluded.total_pnl,
    commissions = excluded.commissions,
    funding = excluded.funding,
    trade_count = excluded.trade_count,
    complete = excluded.complete,
    updated_at = excluded.updated_at;

-- name: ListPositionsByExchange :many
SELECT p.strategy_id, p.instrument_id, p.quantity, p.average_price,
       p.valuation_asset, p.revision, p.updated_at
FROM positions p
JOIN strategy_instances s ON s.id = p.strategy_id
WHERE s.exchange_account_id = ?
ORDER BY p.instrument_id, p.strategy_id;

-- name: ListOpenOrdersByExchange :many
SELECT oi.strategy_id, oi.instrument_id, oi.client_order_id,
       o.exchange_order_id, oi.side, o.status, o.requested_quantity, o.filled_quantity
FROM orders o
JOIN order_intents oi ON oi.id = o.order_intent_id
WHERE o.exchange_account_id = ?
  AND o.status IN ('pending', 'accepted', 'partially_filled', 'unknown')
ORDER BY oi.instrument_id, oi.client_order_id;
