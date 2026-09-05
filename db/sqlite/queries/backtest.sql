-- name: InsertBacktestRun :exec
INSERT INTO backtest_runs (
    id, configured_run_id, strategy_id, application_version, config_hash,
    dataset_checksum, seed, status, revision, started_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'running', 0, ?);

-- name: GetBacktestRun :one
SELECT id, configured_run_id, strategy_id, application_version, config_hash,
       dataset_checksum, seed, status, revision, started_at, finished_at,
       metrics_payload, warnings_payload, error_code, error_message
FROM backtest_runs WHERE id = ?;

-- name: ListBacktestRuns :many
SELECT id, configured_run_id, strategy_id, application_version, config_hash,
       dataset_checksum, seed, status, revision, started_at, finished_at,
       metrics_payload, warnings_payload, error_code, error_message
FROM backtest_runs
ORDER BY started_at, id
LIMIT ?;

-- name: FinishBacktestRun :execrows
UPDATE backtest_runs
SET status = ?, revision = revision + 1, finished_at = ?,
    metrics_payload = ?, warnings_payload = ?, error_code = ?, error_message = ?
WHERE id = ? AND status = 'running' AND revision = ?;

-- name: InsertBacktestArtifact :exec
INSERT INTO backtest_artifacts (
    id, backtest_run_id, artifact_type, path, content_checksum, size,
    schema_version, media_type, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListBacktestArtifacts :many
SELECT id, artifact_type, path, content_checksum, size, schema_version,
       media_type, created_at
FROM backtest_artifacts
WHERE backtest_run_id = ?
ORDER BY artifact_type, path, id;
