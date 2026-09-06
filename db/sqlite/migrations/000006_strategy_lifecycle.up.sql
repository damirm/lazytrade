CREATE TABLE strategy_lifecycle (
    strategy_id TEXT PRIMARY KEY REFERENCES strategy_instances(id),
    runtime_status TEXT NOT NULL CHECK (runtime_status IN ('reconciling','running','stopped','failed','blocked')),
    status_reason TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

INSERT INTO strategy_lifecycle (
    strategy_id,
    runtime_status,
    status_reason,
    updated_at
)
SELECT
    instances.id,
    COALESCE(states.runtime_status, 'stopped'),
    COALESCE(states.status_reason, ''),
    COALESCE(states.updated_at, instances.updated_at)
FROM strategy_instances AS instances
LEFT JOIN strategy_states AS states ON states.strategy_id = instances.id;
