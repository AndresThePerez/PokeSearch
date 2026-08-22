// Package version carries the build identity stamped into the binary at link
// time. The defaults are what a `go run`/`go build` without -ldflags reports;
// the Docker build and CI overwrite them.
//
//	go build -ldflags "\
//	  -X github.com/AndresThePerez/pokesearch/internal/version.Version=v1.2.3 \
//	  -X github.com/AndresThePerez/pokesearch/internal/version.Commit=abc1234 \
//	  -X github.com/AndresThePerez/pokesearch/internal/version.Built=2026-08-22T12:00:00Z"
//
// These values are served by GET /api/meta so every bug report, Courier run
// and screenshot can be pinned to the exact build that produced it.
package version

var (
	Version = "dev"
	Commit  = "none"
	Built   = "unknown"
)
