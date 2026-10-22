// rtail v2.
//
// The major-version suffix is deliberate: Go requires the module path to carry
// /v2 once a v2.x.x tag exists, and it does. Declaring it up front meant the
// tags needed no import rewrite anywhere.
//
// The direct dependencies, and what each one is for:
//
//   parquet-go          the storage format
//   modernc/sqlite      the catalog — the cgo-free SQLite, since the catalog
//                       is not a hot path and did not need to be a second
//                       cgo dependency
//   go-duckdb           search and SQL. The one cgo dependency, and the
//                       reason the server is not a static binary: it links
//                       glibc, the image is Debian slim rather than
//                       distroless, and every platform is built on its own
//                       runner
//   otlp, protobuf      the OpenTelemetry logs receiver
//   klauspost/compress  zstd request bodies on ingest
//   json5               the client's JSON5 lines
//
// The client imports json5 alone, so it is still CGO_ENABLED=0 and static.
module github.com/kilianc/rtail/v2

go 1.25.0

require (
	github.com/klauspost/compress v1.18.0
	github.com/marcboeker/go-duckdb/v2 v2.4.3
	github.com/parquet-go/parquet-go v0.32.0
	github.com/titanous/json5 v1.0.0
	go.opentelemetry.io/proto/otlp v1.11.0
	google.golang.org/protobuf v1.36.11
	modernc.org/sqlite v1.56.0
)

require (
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/apache/arrow-go/v18 v18.4.1 // indirect
	github.com/duckdb/duckdb-go-bindings v0.1.21 // indirect
	github.com/duckdb/duckdb-go-bindings/darwin-amd64 v0.1.21 // indirect
	github.com/duckdb/duckdb-go-bindings/darwin-arm64 v0.1.21 // indirect
	github.com/duckdb/duckdb-go-bindings/linux-amd64 v0.1.21 // indirect
	github.com/duckdb/duckdb-go-bindings/linux-arm64 v0.1.21 // indirect
	github.com/duckdb/duckdb-go-bindings/windows-amd64 v0.1.21 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/google/flatbuffers v25.2.10+incompatible // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/marcboeker/go-duckdb/arrowmapping v0.0.21 // indirect
	github.com/marcboeker/go-duckdb/mapping v0.0.21 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/parquet-go/bitpack v1.0.0 // indirect
	github.com/parquet-go/jsonlite v1.0.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/twpayne/go-geom v1.6.1 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	golang.org/x/exp v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260625142307-59b4966ccb57 // indirect
	golang.org/x/tools v0.47.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
