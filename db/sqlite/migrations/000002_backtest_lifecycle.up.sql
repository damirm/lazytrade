CREATE TRIGGER backtest_runs_validate_insert
BEFORE INSERT ON backtest_runs
WHEN NEW.status NOT IN ('running', 'completed', 'failed', 'cancelled')
  OR NEW.revision < 0
  OR (NEW.status = 'running' AND NEW.finished_at IS NOT NULL)
  OR (NEW.metrics_payload IS NOT NULL AND NOT json_valid(NEW.metrics_payload))
  OR (NEW.warnings_payload IS NOT NULL AND NOT json_valid(NEW.warnings_payload))
BEGIN
    SELECT RAISE(ABORT, 'invalid backtest run');
END;

CREATE TRIGGER backtest_runs_validate_update
BEFORE UPDATE ON backtest_runs
WHEN NEW.status NOT IN ('running', 'completed', 'failed', 'cancelled')
  OR NEW.revision < 0
  OR (NEW.status = 'running' AND NEW.finished_at IS NOT NULL)
  OR (NEW.status <> 'running' AND NEW.finished_at IS NULL)
  OR (NEW.metrics_payload IS NOT NULL AND NOT json_valid(NEW.metrics_payload))
  OR (NEW.warnings_payload IS NOT NULL AND NOT json_valid(NEW.warnings_payload))
BEGIN
    SELECT RAISE(ABORT, 'invalid backtest run');
END;

CREATE TRIGGER backtest_artifacts_validate_insert
BEFORE INSERT ON backtest_artifacts
WHEN NEW.size < 0 OR NEW.schema_version < 1
BEGIN
    SELECT RAISE(ABORT, 'invalid backtest artifact');
END;
