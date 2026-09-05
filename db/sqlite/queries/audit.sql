-- name: InsertAuditEvent :exec
INSERT INTO audit_events
    (id, event_type, actor, scope_type, scope_id, payload, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListAuditEvents :many
SELECT id, event_type, actor, scope_type, scope_id, payload, created_at
FROM audit_events
ORDER BY created_at, id
LIMIT ?;

-- name: GetLatestExecutionHistoryCheckpoint :one
SELECT id, event_type, actor, scope_type, scope_id, payload, created_at
FROM audit_events
WHERE event_type = ? AND scope_type = 'exchange_account' AND scope_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1;
