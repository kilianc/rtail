// rTail v2 — see docs/proposal-logging-system.md.
//
// The major-version suffix is deliberate: v2 lives on its own branch, and Go
// requires the module path to carry /v2 once a v2.x.x tag exists. Declaring it
// now means the eventual tag needs no import rewrite anywhere.
//
// Two direct dependencies, both deliberately pure Go, so the server is still a
// static binary and the container image is still distroless:
//
//   parquet-go     the storage format
//   modernc/sqlite the catalog — the cgo-free SQLite, since the catalog is not
//                  a hot path and a static binary is worth more than the
//                  marginal speed of the cgo driver
//
// That changes at P2. DuckDB is cgo, and bringing it in means a glibc base
// image and a per-platform build matrix.
module github.com/kilianc/rtail/v2

go 1.25.0

require (
	github.com/parquet-go/parquet-go v0.32.0
	modernc.org/sqlite v1.56.0
)

require (
	github.com/andybalholm/brotli v1.1.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/parquet-go/bitpack v1.0.0 // indirect
	github.com/parquet-go/jsonlite v1.0.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/twpayne/go-geom v1.6.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
