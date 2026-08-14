// Package version reports which build of the server is running.
package version

// Version is the release this binary was built from. Release builds set it with
// -ldflags "-X github.com/snakex21/devspace-go/internal/version.Version=v2.0.3",
// and a plain go build leaves it as dev.
var Version = "dev"

// String returns the version to show on the command line and to send in the MCP
// handshake.
func String() string {
	if Version == "" {
		return "dev"
	}
	return Version
}
