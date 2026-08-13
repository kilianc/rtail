-- The catalog, per docs/proposal-logging-system.md Appendix A.
--
-- This is the piece that makes a directory of Parquet files behave like a
-- table: which files exist, what is in them, and what range of time and
-- sequence each one covers. The query planner answers "which files could
-- possibly match" from here in microseconds, and hands DuckDB an explicit
-- file list rather than a glob — which is the difference between opening
-- fourteen footers and opening nine thousand.
--
-- Timestamps are microseconds since the Unix epoch, matching the Parquet
-- columns exactly so no conversion can drift between the two.

CREATE TABLE IF NOT EXISTS files (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  stream        TEXT    NOT NULL,
  path          TEXT    NOT NULL UNIQUE,
  level         INTEGER NOT NULL,          -- 0 = flush, 1 = hourly, 2 = daily
  min_ts        INTEGER NOT NULL,
  max_ts        INTEGER NOT NULL,
  min_seq       INTEGER NOT NULL,
  max_seq       INTEGER NOT NULL,
  row_count     INTEGER NOT NULL,
  byte_size     INTEGER NOT NULL,
  has_raw       INTEGER NOT NULL,
  state         TEXT    NOT NULL DEFAULT 'live',   -- live | compacting | tombstoned
  created_at    INTEGER NOT NULL,
  tombstoned_at INTEGER
);

-- The pruning index. Column order matters: equality on stream and state first,
-- then the time range, so the planner's one hot query is a range scan over a
-- narrow slice rather than a table scan.
CREATE INDEX IF NOT EXISTS files_prune
  ON files (stream, state, max_ts, min_ts);

-- Deleting tombstoned files is a separate, retryable pass.
CREATE INDEX IF NOT EXISTS files_tombstoned
  ON files (state, tombstoned_at);

CREATE TABLE IF NOT EXISTS file_columns (
  file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  column_name TEXT    NOT NULL,   -- a_user_id
  source_key  TEXT    NOT NULL,   -- user_id, or "http.status-code"
  kind        TEXT    NOT NULL,   -- int | float | string | boolean | json
  polymorphic INTEGER NOT NULL,
  null_count  INTEGER NOT NULL,
  min_value   TEXT,
  max_value   TEXT,
  PRIMARY KEY (file_id, column_name)
);

-- "Which files even have this key" is the second-cheapest way to prune, after
-- time.
CREATE INDEX IF NOT EXISTS file_columns_key
  ON file_columns (source_key, file_id);

-- Every key ever seen on a stream, which is what makes the search bar's
-- autocomplete real rather than a guess.
CREATE TABLE IF NOT EXISTS schema_keys (
  stream      TEXT    NOT NULL,
  source_key  TEXT    NOT NULL,
  kind        TEXT    NOT NULL,
  polymorphic INTEGER NOT NULL DEFAULT 0,
  occurrences INTEGER NOT NULL DEFAULT 0,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  PRIMARY KEY (stream, source_key)
);

CREATE TABLE IF NOT EXISTS streams (
  name       TEXT    PRIMARY KEY,
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  row_count  INTEGER NOT NULL DEFAULT 0,
  byte_size  INTEGER NOT NULL DEFAULT 0,
  dedicated  INTEGER NOT NULL DEFAULT 1
);

-- Single-row bookkeeping that has to survive a restart. The sequence counter
-- is the important one: seq is the tiebreaker in the (ts, seq) ordering that
-- pagination depends on, so restarting it at 1 would make old and new records
-- collide.
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
