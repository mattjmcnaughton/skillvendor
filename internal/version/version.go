// Package version exposes build-time version metadata.
package version

// Version is set to the semantic-release version for release builds via
// `go build -ldflags -X`. Local development builds keep the default value.
var Version = "dev"
