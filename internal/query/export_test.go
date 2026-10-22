package query

import "database/sql"

// DB is the engine's pool, for tests that need a connection of their own.
func (e *Engine) DB() *sql.DB { return e.db }
