// Package version exposes build metadata injected via -ldflags at build time.
// Replaces the TypeScript git-commit-saver.
package version

// These are package-level vars (not consts) because they are injected at build
// time via: go build -ldflags "-X .../internal/version.Commit=... -X .../internal/version.BuildTime=...".
var (
	// Commit is the short git SHA the binary was built from.
	Commit = "unknown" //nolint:gochecknoglobals // injected via -ldflags at build time
	// BuildTime is the UTC build timestamp (RFC3339).
	BuildTime = "unknown" //nolint:gochecknoglobals // injected via -ldflags at build time
	// AppName is the application name.
	AppName = "liteend-go" //nolint:gochecknoglobals // build-time identity, overridable via -ldflags
	// AppVersion is the semantic version.
	AppVersion = "0.0.1" //nolint:gochecknoglobals // build-time identity, overridable via -ldflags
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
