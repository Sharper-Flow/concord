// Package version contains the build version shown by the CLI.
package version

// Development is the version shown by an unstamped development build.
const Development = "dev"

// Value is replaced by release tooling with -ldflags -X for versioned artifacts.
var Value = Development
