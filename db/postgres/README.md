# PostgreSQL storage (future)

PostgreSQL is intentionally deferred. Its adapter will have independent
migrations, queries and sqlc output, while implementing the same narrow Go
contracts and contract tests as SQLite. One database will still belong to one
agent; a session advisory lock will be used only as a fail-fast guard.
