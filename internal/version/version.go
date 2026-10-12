// Package version holds the engine version, set at build time with -ldflags.
package version

// Version is overridden by the release build. "dev" marks a local build.
var Version = "0.1.0-dev"
