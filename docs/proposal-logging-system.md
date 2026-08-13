# rTail → a real logging system

**Status:** proposal, nothing built
**Author:** drafted with Claude, 2026-08-11
**Scope:** turn rTail from an ephemeral UDP viewer into a durable, searchable,
schema-less log store backed by Parquet, with a Go backend, a React frontend,
and a plausible SaaS on top.

---

## 1. Where we are

Today rTail is ~330 lines of Node across two binaries:

- [`cli/rtail-client.js`](../cli/rtail-client.js) — reads stdin, tries `JSON5.parse`,
  falls back to chrono date extraction, sends one UDP datagram per line.
- [`cli/rtail-server.js`](../cli/rtail-server.js) — binds UDP, keeps a
  `Map<streamId, Line[]>` capped at 100 entries, fans out over socket.io.
- [`app/`](../app) — a Preact SPA that renders those lines with ANSI colouring
  and JSON highlighting, plus a client-side regexp filter.

The README says the quiet part out loud: *"There is no persistent layer, nor
does the tool store any data."* Everything below is about deleting that
sentence without losing the thing that makes rTail good — that
`cmd | rtail` is the entire onboarding flow.

**Non-negotiable constraint:** `cmd | rtail` keeps working, byte-for-byte, with
the client people already have installed. The UDP wire format is frozen.

---

## 2. The shape of the thing

```
                    ┌──────────────────────────────────────────────┐
   cmd | rtail ───► │  ingest                                      │
   OTLP/HTTP   ───► │   receivers → normalize → WAL → row batches  │
   syslog      ───► │                              │               │
   HTTP NDJSON ───► │                              ▼               │
                    │                   ┌────────────────────┐     │
                    │                   │  L0 parquet flush  │     │
                    │                   └─────────┬──────────┘     │
                    └─────────────────────────────┼────────────────┘
                                                  │
                    ┌─────────────────────────────▼────────────────┐
                    │  storage                                     │
                    │   data/<stream>/<date>/L{0,1,2}-*.parquet    │
                    │   catalog.sqlite  (files, columns, streams)  │
                    │   compactor: L0→L1 hourly, L1→L2 daily       │
                    └─────────────────────────────┬────────────────┘
                                                  │
                    ┌─────────────────────────────▼────────────────┐
                    │  query                                       │
                    │   rQL ──┐                                    │
                    │         ├─► plan ─► prune (catalog) ─► DuckDB│
                    │   SQL ──┘                    read_parquet([])│
                    └─────────────────────────────┬────────────────┘
                                                  │
                    ┌─────────────────────────────▼────────────────┐
                    │  api: /v1/search /v1/histogram /v1/tail (SSE)│
                    └─────────────────────────────┬────────────────┘
                                                  │
                    ┌─────────────────────────────▼────────────────┐
                    │  web: React SPA — command bar, histogram,    │
                    │       virtualized results, field explorer    │
                    └──────────────────────────────────────────────┘
```

Three ideas carry the whole design:

1. **Logs are just Parquet files on disk.** No proprietary format, no daemon
   required to read them. `duckdb -c "select * from 'data/**/*.parquet'"` works
   with rTail uninstalled. This is the anti-lock-in story and it is also the
   SaaS story.
2. **A tiny transactional catalog next to the files** (SQLite) holds the file
   list, per-file column inventory, and min/max stats. It is what makes
   pruning, compaction, and snapshot isolation possible. Think Iceberg, minus
   999 pages of spec.
3. **One filter AST, two backends.** The same parsed query compiles to SQL for
   historical search *and* to an in-process row matcher for live tail. Filtered
   live tail then costs nothing extra to build.

---

## 3. Schema: fixed envelope, dynamic body

This is the crux, and it is what you described: root JSON keys become Parquet
columns; anything deeper stays JSON and is reachable with JSON path operators.

### 3.1 Envelope — always present, always these types

| column      | parquet type              | notes |
|-------------|---------------------------|-------|
| `ts`        | `TIMESTAMP(MICROS, UTC)`  | event time; from the payload, else ingest time |
| `ingest_ts` | `TIMESTAMP(MICROS, UTC)`  | when we received it; never trusts the client |
| `stream`    | `STRING` (dict)           | the `--id` |
| `seq`       | `INT64`                   | monotonic per shard — stable ordering + keyset pagination |
| `level`     | `STRING` (dict)           | normalized from `level`/`severity`/`lvl`/`@l`; else `NULL` |
| `msg`       | `STRING`                  | from `message`/`msg`/`text`; for plain-text lines, the whole line |
| `host`      | `STRING` (dict)           | source address |
| `is_json`   | `BOOLEAN`                 | did the line parse as an object |
| `raw`       | `STRING`                  | the original line, verbatim |

`raw` is the safety net. Parsing can be wrong, promotion can be lossy, type
inference can guess badly — `raw` means none of those ever destroy data, and
any field we failed to promote is still queryable via `raw ->> '$.path'`.

Cost: roughly +60–80% storage over columns alone, before compression; much less
after, because zstd sees the same tokens twice in the same row group. Make it a
policy knob: `keep_raw = always | l0_only | never`, default `always`.

### 3.2 Dynamic — one column per observed root key

For `{"level":"error","user_id":42,"latency_ms":12.5,"req":{"path":"/v1/x"}}`:

```
optional binary  a_level      (STRING)   -- also lifted into the envelope
optional int64   a_user_id    (INT64)
optional double  a_latency_ms (DOUBLE)
optional binary  a_req        (STRING, JSON)   -- nested → JSON text
```

The `a_` prefix keeps user keys from colliding with envelope names. The catalog
stores the original key alongside the mangled column name, so a key like
`"http.status-code"` becomes `a_http_status_code` on disk and still displays
and autocompletes as `http.status-code` in the UI.

**Type inference, per key, per file** (a file is one flush — minutes of data,
so this converges fast):

| observed in this file | column type |
|---|---|
| all integers | `INT64` |
| integers + floats | `DOUBLE` |
| all booleans | `BOOLEAN` |
| all strings | `STRING` |
| objects or arrays | `STRING` annotated `JSON` |
| mixed scalar kinds | `STRING`, and the catalog marks the key `polymorphic` |
| only nulls | column omitted; catalog records "seen, always null" |

Two files can disagree about a key's type. That is fine and expected —
`read_parquet(..., union_by_name := true)` reconciles by name, and the planner
inserts `TRY_CAST` when the catalog reports a conflict across the pruned set.
Compaction resolves the conflict permanently by widening to the common
supertype (usually `STRING`).

### 3.3 The one query rewrite that matters

A user typing `user_id = 42` should never have to know whether that key was
promoted in every file, some files, or none.

```
field foo, pruned set has a_foo in ALL files
    → a_foo

field foo, pruned set has a_foo in SOME files
    → COALESCE(a_foo, TRY_CAST(raw ->> '$.foo' AS <type>))

field foo, no file has a_foo
    → TRY_CAST(raw ->> '$.foo' AS <type>)

field req.path (dotted)
    → a_req ->> '$.path'   if a_req is JSON, else raw ->> '$.req.path'
```

Emitting the bare column when we can is not an optimization detail — it is the
difference between a scan that reads one dictionary-encoded column and one that
decompresses every `raw` byte in the range.

### 3.4 On-disk layout

```
data/
  catalog.sqlite
  wal/<stream>-<shard>.wal
  streams/api.example.com/2026/08/11/
    L0-01H9X...-1754899200.parquet
    L1-01H9Y...-1754899200.parquet
  streams/_shared/2026/08/11/
    L1-01H9Z...-1754899200.parquet        # low-volume streams pooled
```

Hive-ish, human-navigable, and directly openable by DuckDB, pandas, Polars,
Spark, or Athena.

**Small-files problem, addressed up front:** a per-stream file path is wrong if
you have 5,000 streams each emitting 3 lines an hour. Streams under a volume
threshold are pooled into `_shared` files with `stream` as the leading sort
column, so pruning by stream still works at row-group granularity. A stream
graduates to its own path when it sustains its own files.

---

## 4. Write path

### 4.1 Receivers

| protocol | why |
|---|---|
| **UDP, current format** | frozen; `cmd \| rtail` keeps working forever |
| **HTTP `POST /v1/ingest`** | NDJSON, gzip/zstd, batched, acked. UDP is at-most-once and 64KB-capped; anything that cares about not losing lines needs this |
| **OTLP/HTTP logs** | every OpenTelemetry collector on earth can point at us with a config line and no code change. Highest-leverage single feature for adoption |
| **syslog RFC5424** | appliances, nginx, systemd |
| **Vector / Fluent Bit sink docs** | not code, just a documented endpoint — but it is how people actually migrate |

### 4.2 Pipeline

```
receive → parse → normalize envelope → WAL append (fsync group-commit)
                                    → in-memory Arrow row batch
                                            │
        flush when: 1M rows │ 128MB uncompressed │ 30s
                                            ▼
                        write L0 parquet to tmp/ → fsync
                        catalog txn: INSERT file + file_columns
                        rename into place → truncate WAL segment
```

The WAL is the difference between "logging system" and "log viewer". Without
it, a crash loses up to 30 seconds of everything. It is an append-only file per
stream-shard, group-committed, truncated on flush, and replayed into Parquet on
boot before the server accepts traffic.

**Late and out-of-order data:** an event whose `ts` is three hours old goes
into the current L0 anyway. Files are described by `[min_ts, max_ts]` in the
catalog and are allowed to overlap; the planner never assumes disjointness.
The compactor sorts by event time, so the mess is transient.

**Dedup:** UDP is at-most-once, HTTP ingest is at-least-once. Optional
`(client_id, client_seq)` dedup during compaction gets us effectively-once for
the HTTP path. Ship it in v2, do not pretend otherwise in v1.

---

## 5. Compaction and retention

Leveled, size-tiered, single-writer-per-stream:

| level | trigger | contents | encoding |
|---|---|---|---|
| **L0** | flush | ~5–50MB, minutes, sorted by `seq` | zstd:3, no bloom |
| **L1** | hourly, or ≥8 L0s | one file per stream-hour, sorted by `ts` | zstd:6, dictionary, bloom on high-cardinality strings, 1M-row row groups |
| **L2** | daily | one file per stream-day, sorted by `(level, ts)` or a configured clustering key | zstd:12, full stats |

Sort order is the single biggest lever on query latency. Sorting by `ts` makes
time-range pruning exact at row-group granularity; a secondary clustering key
(`service`, `level`, `env`) turns the most common filter into a row-group skip
instead of a scan.

Compaction also does the schema work: it computes the union schema of its
inputs, widens conflicting types, drops always-null columns, and rewrites the
catalog's column inventory. This is where a messy L0 population becomes a clean
L1.

**Transactionality:** write to `tmp/`, fsync, then a single catalog transaction
inserts the new files and tombstones the old ones. Tombstoned files are deleted
by a GC pass after a grace period, so a query that snapshotted the file list
five seconds ago still has its files on disk. That grace period is our MVCC.

**Retention:** per-stream policies evaluated on a schedule — `delete after N
days`, `drop raw after N days` (keeps columns, halves the footprint), `downsample
after N days` (keep only `level >= WARN`, or 1-in-N sampling). All expressed as
catalog-driven rewrites, all using the same machinery as compaction.

---

## 6. Query

### 6.1 Engine: DuckDB, embedded

Recommended: **[`go-duckdb`](https://github.com/marcboeker/go-duckdb)** for
reads, **[`parquet-go`](https://github.com/parquet-go/parquet-go)** for writes.

DuckDB gives us, for free, things that are each multi-month projects: a real
SQL planner, Parquet predicate and projection pushdown, `union_by_name` across
heterogeneous schemas, JSON path operators, window functions, `time_bucket`,
and vectorized execution that saturates NVMe.

**The cost is cgo**, and it is a real cost, so name it:
- No `CGO_ENABLED=0` static binary. Per-platform builds for linux/amd64,
  linux/arm64, darwin/arm64.
- ~40–60MB added to the binary.
- Cross-compilation needs a toolchain per target (or a CI matrix, which we want
  anyway).

Alternatives considered and rejected: DataFusion via FFI (same cgo cost,
less mature Go bindings), chDB/clickhouse-local (a second process to supervise),
pure-Go query engine (no SQL, which is the whole ask). **This is decision #1 and
it needs your sign-off before anything else is worth building.**

Writes use `parquet-go` rather than DuckDB `COPY` because the write path is a
streaming, per-row, schema-discovering thing and DuckDB wants a table first.

### 6.2 Two query languages

**rQL — the filter bar.** Terse, Stackdriver-shaped, what 95% of use looks like:

```
level>=ERROR service="api" user_id=42 "connection timeout"
-path:/health AND (region=us-east-1 OR region=us-west-2)
latency_ms>500 req.path:~"^/v1/.*"
```

- bare words → full-text on `msg`, falling back to `raw`
- `k=v` equality, `k:v` substring, `k:~re` regexp, `k>n` comparison
- `-` or `NOT` negation, `AND`/`OR`, parens
- `k=*` exists, `k=null` absent

**Raw SQL — the escape hatch.** Full DuckDB over a `logs` view:

```sql
SELECT service, count(*) c, quantile_cont(latency_ms, 0.99) p99
FROM logs
WHERE level = 'ERROR'
GROUP BY 1 ORDER BY c DESC;
```

Users can join their own CSV/Parquet against their logs. That is a genuinely
differentiated capability and it costs us nothing.

### 6.3 Planning and pruning

```
rQL AST ──┐
          ├─► logical plan ─► catalog prune ─► DuckDB SQL
SQL ──────┘                        │           read_parquet([f1, f2, …],
   (parse for ts/stream            │              union_by_name := true)
    predicates only)               │
                                   ▼
                     files WHERE max_ts >= lo AND min_ts <= hi
                       AND stream matches
                       AND (column exists OR raw retained)
                       AND column min/max intersects predicate
```

We hand DuckDB an **explicit file list**, never a glob. That is what makes this
fast: the catalog answers "which files could possibly match" from an indexed
SQLite query in microseconds, and DuckDB then never opens the footer of a file
that cannot contribute.

The UI's time picker is therefore not decoration — it is the partition pruner,
and it is always present, exactly as it is in Stackdriver. Raw SQL without a
time bound gets the default window injected, with a visible notice.

**Safety rails:** read-only DuckDB connection, external access confined to the
data dir, per-query memory limit, statement timeout, row cap, context
cancellation wired to the HTTP request, and a bounded query queue so one
`SELECT *` over a year cannot starve live tail.

### 6.4 Pagination

Keyset, on `(ts, seq)` — never `OFFSET`. `OFFSET 50000` re-scans 50,000 rows;
`WHERE (ts, seq) < (?, ?)` skips row groups. The pair is unique and totally
ordered, so pagination is stable even while new data lands.

### 6.5 Live tail, filtered

The rQL AST also compiles to a Go predicate over the in-flight row. So the
ingest path can evaluate a subscriber's filter directly and push matches over
SSE. Historical search and live tail then present the *same* semantics from the
*same* parse, which is the property that makes Stackdriver's "stream with a
filter applied" feel correct rather than approximate.

---

## 7. The interface

Stackdriver's Logs Explorer got four things genuinely right. Take all four,
then make it look like something built this decade.

1. **The histogram is the navigation.** Counts over time, stacked by severity,
   drag to zoom the range.
2. **Query building by clicking.** Expand a row, click any value, get
   "filter to this" / "exclude this" — the query bar updates. Most queries
   should be built with the mouse and refined with the keyboard.
3. **The field explorer.** A sidebar of top values per field *for the current
   result set*, with counts and percentages. It answers "what is even in here"
   before you know what to search for.
4. **Everything is a URL.** Full query state in the address bar — time range,
   filter, expanded rows, scroll anchor.

```
┌────────────────────────────────────────────────────────────────────────────┐
│ ▪ rtail    level>=ERROR service="api" "timeout"          ⌄ Last 6 hours    │  ← command bar + time
├────────────────────────────────────────────────────────────────────────────┤
│  ▁▂▃▅█▇▅▃▂▁▁▂▄▆█▇▄▂▁▁▁▂▃▄▃▂▁▁▂▃▅▇█▆▄▂▁▁▂▃▂▁▁▁▂▄▅▃▂▁    2,481 events      │  ← brushable histogram
├──────────────┬─────────────────────────────────────────────────────────────┤
│ FIELDS       │ 14:22:07.441  ERROR  api    upstream timeout after 30000ms  │
│              │ 14:22:07.441  ERROR  api    upstream timeout after 30000ms  │
│ service      │ ▼ 14:22:09.102 ERROR api    connection reset by peer        │
│  api    84%  │     ts          2026-08-11T14:22:09.102Z                    │
│  worker 12%  │     service     "api"              ⊕ filter   ⊖ exclude     │
│  cron    4%  │     user_id     4471               ⊕ filter   ⊖ exclude     │
│              │     req ▸ { path: "/v1/orders", method: "POST" }            │
│ level        │     latency_ms  30004.2                                     │
│  ERROR  91%  │     ⧉ copy   ↗ context   ⌘⏎ query this trace                │
│  FATAL   9%  │ 14:22:11.550  ERROR  api    upstream timeout after 30000ms  │
│              │ …                                                            │
│ + 41 fields  │                                                              │
└──────────────┴─────────────────────────────────────────────────────────────┘
```

**Command bar.** Monospace, single line, the hero element. Token-aware
autocomplete driven by the catalog's `schema_keys` table — it knows every field
name that has ever appeared on this stream, its type, and its top values, so
the completion is real rather than a guess. `⌘K` for the palette, `/` to focus,
`⌘⏎` to run, `⌥⏎` to drop into raw SQL with the current rQL pre-translated.

**Results.** Virtualized (TanStack Virtual) — 100k rows scroll at 60fps. Row
collapsed is `ts │ level │ stream │ msg`; expanded is a JSON tree with
filter/exclude affordances on every leaf. Level is a 2px left border, not a
badge — colour carries it, chrome does not.

**Context view.** From any row, "show 50 lines around this" — the neighbourhood
by `(ts, seq)` with the filter dropped. This is the feature people actually
debug with and most tools bury it.

**Aesthetic direction.** Keep the existing dark/light token system in
[`_tokens.scss`](../app/scss/_tokens.scss); tighten everything else. Hairline
borders, one accent colour, a real 1.2 type scale, no gradients, no shadows
beyond a single elevation for popovers. Density over whitespace — this is an
instrument, and legibility at 12px mono is the goal, not air. Motion only where
it communicates state (histogram brush, row expand), 120ms, nothing decorative.

**React note.** The app is currently **Preact**, not React. Recommendation:
keep Preact and add `preact/compat` aliasing so we can pull TanStack Virtual,
cmdk, and Radix primitives while keeping a 12KB runtime. If any of those fight
compat, switching to React 19 is a `tools/build.js` alias change and a
`package.json` line. Decision #2, low stakes, reversible.

---

## 8. What this requires

### 8.1 To run it

Single binary + one data directory:

```bash
rtail-server --data ./data --retention 30d
```

Webapp embedded via `embed.FS`. Container image as today. Because of cgo, three
release artifacts (linux/amd64, linux/arm64, darwin/arm64) instead of one.

Rough sizing, structured JSON logs, zstd, after L2:
- **30–80 bytes/event on disk** — 1TB of raw log text lands around 40–80GB.
- **~100–200k events/s ingest** on 4 cores with NVMe.
- **1–2GB/s scan**, so a well-pruned query over a day of one stream returns in
  tens of milliseconds; a badly-pruned one over a year takes seconds.
- RAM is a function of flush buffers: ~256MB per active high-volume stream.

### 8.2 To build it

Honest estimate, one focused engineer:

| phase | scope | est. |
|---|---|---|
| **P0** | Go rewrite at parity — UDP in, SSE out, existing UI, no storage | 1–2 wk |
| **P1** | storage engine — normalize, WAL, L0 writer, catalog | 3–4 wk |
| **P2** | query — DuckDB, planner, pruning, rQL, SQL, HTTP API | 3–4 wk |
| **P3** | compaction, retention, GC, crash-recovery tests | 2–3 wk |
| **P4** | the new UI | 4–6 wk |
| **P5** | OTLP, HTTP ingest, syslog, docs | 2 wk |
| | **credible OSS 1.0** | **~4–5 months** |

The riskiest phase is P3 — compaction correctness under crash is where storage
engines go to die. Budget deterministic simulation tests (kill -9 at every
fsync boundary, assert catalog and disk agree) rather than hoping.

P0 is worth doing first and alone: it de-risks the Go port while the surface
area is 330 lines, and it ships something immediately.

### 8.3 Decisions I need from you

1. **cgo/DuckDB — yes or no?** Everything downstream depends on it. "No" means
   no SQL, or a second process, and I'd argue the feature is not worth having
   without it.
2. **Preact + compat, or move to React 19?** Low stakes.
3. **Licence.** MIT as today invites a hyperscaler to run our SaaS. AGPL or BSL
   protects the business and costs some adoption. This has to be decided
   *before* the first commit of new code, not after.
4. **Does the OSS single-node version stay fully featured forever?** My
   recommendation is yes — the moat is multi-tenancy, S3, and operations, not
   features. Crippled OSS kills the funnel.

---

## 9. The SaaS

### 9.1 Why the architecture already is the product

Files + a catalog + stateless query is exactly the disaggregated shape a cloud
version needs. The port is:

| self-hosted | cloud |
|---|---|
| local disk | S3 (behind a `storage.Backend` interface — **define it on day one**) |
| SQLite catalog | Postgres catalog, same schema |
| in-process query | stateless query workers, NVMe cache in front of S3 |
| one tenant | tenant id as a catalog column + storage prefix + a query-level guard that cannot be bypassed |
| compactor goroutine | compaction as a work queue |

That is a storage-interface swap and an auth layer, not a rewrite — *provided*
the interface exists from the beginning. If we hardcode `os.Open`, this
paragraph becomes six months of work instead of six weeks.

### 9.2 Positioning

The wedge is **price and openness**, not technology. Columnar-on-object-store
is commoditized — Axiom, Baselime, and every ClickHouse-backed startup already
have it. Datadog's log pricing is the loudest complaint in the category, and
that is the opening.

Three things to be genuinely differentiated on:

1. **Bring your own bucket.** Data lands in *the customer's* S3. We run compute
   and the catalog. Solves compliance and data residency, eliminates egress
   from their bill, and makes leaving trivial — which is exactly why people
   will trust it.
2. **No lock-in, provably.** It is Parquet. The day they leave, they already
   have everything, in a format Spark and DuckDB read natively. Say it on the
   pricing page.
3. **SQL, not a DSL.** No proprietary query language to learn and abandon.

Pricing: **per GB ingested + per GB-month retained. No per-seat, no per-query,
no per-host.** Seat pricing is what makes teams ration access to their own
logs, and rationed observability is the thing everybody hates.

### 9.3 What the SaaS needs beyond the OSS core

- Auth: OIDC/SAML, orgs, teams, RBAC, API keys with scopes
- Ingest gateway: quotas, rate limiting, backpressure, per-tenant isolation
- Metering and billing (Stripe), usage dashboards, spend alerts
- Alerting: saved query + threshold + schedule + destinations
- Retention policy UI, audit log, data export
- Status page, on-call, SOC 2 Type II (~6–9 months, gates enterprise deals)

Realistically +3–4 months on top of OSS 1.0 before a paid tier is defensible,
and the SOC 2 clock should start early because it is wall-clock-bound, not
effort-bound.

### 9.4 Risks worth stating

- **Crowded market.** Datadog, Grafana Loki, Better Stack, Axiom, Signoz,
  Openobserve. Differentiation must be price + openness + UX.
- **Observability is a three-legged stool.** Logs alone is a hard sell to teams
  who want metrics and traces in one place. The OTLP receiver is the hedge:
  traces are structurally the same problem and the storage engine already
  handles them.
- **rTail's existing audience** (developers tailing local processes) is not the
  same audience as the buyer of a log SaaS. The OSS tool is a funnel, but a
  leaky one. Worth being clear-eyed that the SaaS is a new go-to-market, not a
  conversion of the existing users.
- **cgo makes releases fiddly.** Not fatal, but it turns "one binary" into a CI
  matrix and a support surface.

---

## 10. Recommended sequence

```
now ──► P0  Go server at parity, UDP frozen, SSE instead of socket.io
        │   ship it, delete the Node server, prove the port
        ▼
        P1  storage: WAL + L0 + catalog. Logs survive a restart.
        │   ← first genuinely new capability, ship at this point
        ▼
        P2  query: DuckDB + rQL + SQL API + a crude UI over it
        │   ← "search your logs" is now true
        ▼
        P3  compaction + retention. It stops growing without bound.
        │   ← now it is a logging system
        ▼
        P4  the interface. This is what people will judge it on.
        ▼
        P5  OTLP + HTTP + syslog. Now other people's logs can arrive.
        ▼
        OSS 1.0  ──►  storage.Backend → S3, catalog → Postgres, auth, billing
```

Each phase ends with something shippable. P1 is where rTail stops being a
viewer, P3 is where it becomes a system, P4 is where it becomes a product.

---

## Appendix A — catalog schema (SQLite)

```sql
CREATE TABLE files (
  id          INTEGER PRIMARY KEY,
  stream      TEXT NOT NULL,
  path        TEXT NOT NULL UNIQUE,
  level       INTEGER NOT NULL,          -- 0, 1, 2
  min_ts      INTEGER NOT NULL,          -- micros, UTC
  max_ts      INTEGER NOT NULL,
  min_seq     INTEGER NOT NULL,
  max_seq     INTEGER NOT NULL,
  row_count   INTEGER NOT NULL,
  byte_size   INTEGER NOT NULL,
  has_raw     INTEGER NOT NULL,
  state       TEXT NOT NULL,             -- live | compacting | tombstoned
  created_at  INTEGER NOT NULL,
  tombstoned_at INTEGER
);
CREATE INDEX files_prune ON files (stream, state, max_ts, min_ts);

CREATE TABLE file_columns (
  file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  column_name TEXT NOT NULL,             -- a_user_id
  source_key  TEXT NOT NULL,             -- user_id  (or "http.status-code")
  phys_type   TEXT NOT NULL,             -- INT64 | DOUBLE | BOOLEAN | STRING
  is_json     INTEGER NOT NULL,
  null_count  INTEGER NOT NULL,
  min_value   BLOB,
  max_value   BLOB,
  PRIMARY KEY (file_id, column_name)
);
CREATE INDEX file_columns_lookup ON file_columns (source_key, file_id);

CREATE TABLE schema_keys (                -- powers autocomplete
  stream      TEXT NOT NULL,
  source_key  TEXT NOT NULL,
  phys_type   TEXT NOT NULL,
  polymorphic INTEGER NOT NULL,
  occurrences INTEGER NOT NULL,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  PRIMARY KEY (stream, source_key)
);

CREATE TABLE streams (
  name       TEXT PRIMARY KEY,
  first_seen INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  row_count  INTEGER NOT NULL,
  byte_size  INTEGER NOT NULL,
  dedicated  INTEGER NOT NULL             -- own path, or pooled into _shared
);
```

## Appendix B — a compiled query, end to end

Input:

```
level>=ERROR service="api" latency_ms>500 "timeout"
```

with the time picker on *last 6 hours*.

Catalog prune (SQLite, sub-millisecond):

```sql
SELECT path FROM files
WHERE state = 'live'
  AND max_ts >= :lo AND min_ts <= :hi
  AND (stream = 'api' OR NOT dedicated)
ORDER BY min_ts;
-- → 14 files, 2 of which lack a_latency_ms
```

Emitted SQL:

```sql
SELECT ts, level, stream, msg, raw
FROM read_parquet(
       ['…/L2-01H…parquet', …14 paths…],
       union_by_name := true
     )
WHERE ts >= :lo AND ts < :hi
  AND level IN ('ERROR','FATAL','CRITICAL')
  AND COALESCE(a_service, raw ->> '$.service') = 'api'
  AND COALESCE(a_latency_ms,
               TRY_CAST(raw ->> '$.latency_ms' AS DOUBLE)) > 500
  AND (msg ILIKE '%timeout%' OR raw ILIKE '%timeout%')
  AND (ts, seq) < (:cursor_ts, :cursor_seq)
ORDER BY ts DESC, seq DESC
LIMIT 200;
```

Note `a_service` is emitted bare where every file has it, and wrapped in
`COALESCE` only for `a_latency_ms`, where two files do not. The histogram above
the results is the same `FROM`/`WHERE` with
`SELECT time_bucket(INTERVAL '2 minutes', ts) b, level, count(*) GROUP BY 1, 2`
— which touches two columns and returns in single-digit milliseconds.
