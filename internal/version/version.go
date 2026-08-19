// Package version reports which build of the server is running.
package version

import "runtime/debug"

// Version is the release this binary was built from. Release builds set it with
// -ldflags "-X github.com/snakex21/devspace-go/internal/version.Version=v2.0.3",
// and a plain go build leaves it as dev.
var Version = "dev"

// dev is what a build with no stamped release calls itself.
const dev = "dev"

// shortRevision is how much of a commit hash to show, matching the abbreviation
// git prints by default.
const shortRevision = 7

// readBuildInfo is a variable so that a test can describe a build rather than
// depend on how the test binary itself happened to be compiled: Go does not
// promise to stamp version control details into everything it produces.
var readBuildInfo = debug.ReadBuildInfo

// String returns the version to show on the command line and to send in the MCP
// handshake.
//
// A stamped build reports its tag. An unstamped one used to report a bare dev,
// which cannot tell apart the several unstamped builds that tend to exist at
// once: one from a checkout, one copied out of a CI artifact, one left over from
// last week. Go records the commit in binaries built from a checkout, so an
// unstamped build now reports that instead, as "dev (0b2bd2a)", or as
// "dev (0b2bd2a, modified)" when the tree still held uncommitted changes.
func String() string {
	if Version != "" && Version != dev {
		return Version
	}
	if info, ok := readBuildInfo(); ok {
		if built := describeBuild(info.Settings); built != "" {
			return dev + " (" + built + ")"
		}
	}
	return dev
}

// describeBuild summarises the version control details Go stamps into a binary.
// It returns an empty string when there is no revision to report, which is the
// case for a build from an unpacked archive, from the module cache, or one made
// with -buildvcs=false.
func describeBuild(settings []debug.BuildSetting) string {
	var revision, modified string
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return ""
	}
	if len(revision) > shortRevision {
		revision = revision[:shortRevision]
	}
	if modified == "true" {
		return revision + ", modified"
	}
	return revision
}
