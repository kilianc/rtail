// rTail v2 — see docs/proposal-logging-system.md.
//
// The major-version suffix is deliberate: v2 lives on its own branch, and Go
// requires the module path to carry /v2 once a v2.x.x tag exists. Declaring it
// now means the eventual tag needs no import rewrite anywhere.
//
// P0 has no third-party dependencies at all. DuckDB (cgo) and parquet-go
// arrive with the storage and query engines in P1/P2.
module github.com/kilianc/rtail/v2

go 1.24
