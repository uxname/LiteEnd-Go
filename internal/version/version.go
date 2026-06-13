// Package version exposes build metadata injected via -ldflags at build time.
// Replaces the TypeScript git-commit-saver.
package version

// These are set with: go build -ldflags "-X .../internal/version.Commit=... -X .../internal/version.BuildTime=..."
var (
	// Commit is the short git SHA the binary was built from.
	Commit = "unknown"
	// BuildTime is the UTC build timestamp (RFC3339).
	BuildTime = "unknown"
	// AppName is the application name.
	AppName = "liteend-go"
	// AppVersion is the semantic version.
	AppVersion = "0.0.1"
)

// Info bundles build metadata for the debug resolver.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
}

// Get returns the current build info.
func Get() Info {
	return Info{Name: AppName, Version: AppVersion, Commit: Commit, BuildTime: BuildTime}
}
