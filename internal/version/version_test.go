package version

import (
	"runtime/debug"
	"testing"
)

// stubBuildInfo makes String read the build a test describes, rather than
// whatever the toolchain happened to stamp into this test binary.
func stubBuildInfo(t *testing.T, settings ...debug.BuildSetting) {
	t.Helper()
	original := readBuildInfo
	t.Cleanup(func() { readBuildInfo = original })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: settings}, true
	}
}

// pinVersion sets the linker stamped version for the length of one test.
func pinVersion(t *testing.T, value string) {
	t.Helper()
	original := Version
	t.Cleanup(func() { Version = original })
	Version = value
}

// revision is a full length hash, so the tests also cover the abbreviation.
const revision = "0b2bd2ae0f4c8133c89023e5c39c432048123a03"

func TestStringFallsBackWhenTheBuildDidNotSetIt(t *testing.T) {
	pinVersion(t, "")
	stubBuildInfo(t)

	if got := String(); got != "dev" {
		t.Errorf("String() = %q, want dev", got)
	}
}

func TestStringReportsWhatTheBuildSet(t *testing.T) {
	pinVersion(t, "v2.0.3")

	if got := String(); got != "v2.0.3" {
		t.Errorf("String() = %q, want v2.0.3", got)
	}
}

func TestAnUnstampedBuildNamesTheCommitItCameFrom(t *testing.T) {
	pinVersion(t, "dev")
	stubBuildInfo(t,
		debug.BuildSetting{Key: "vcs.revision", Value: revision},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
	)

	if got, want := String(), "dev (0b2bd2a)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestAnUnstampedBuildSaysWhenTheTreeWasDirty(t *testing.T) {
	pinVersion(t, "dev")
	stubBuildInfo(t,
		debug.BuildSetting{Key: "vcs.revision", Value: revision},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
	)

	if got, want := String(), "dev (0b2bd2a, modified)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestATaggedBuildNamesItsTagRatherThanItsCommit(t *testing.T) {
	pinVersion(t, "v0.3.0")
	stubBuildInfo(t,
		debug.BuildSetting{Key: "vcs.revision", Value: revision},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
	)

	if got := String(); got != "v0.3.0" {
		t.Errorf("String() = %q, want v0.3.0: a release should report its tag, not the commit under it", got)
	}
}

func TestABuildWithNoRevisionStaysPlainDev(t *testing.T) {
	pinVersion(t, "dev")
	stubBuildInfo(t, debug.BuildSetting{Key: "vcs.modified", Value: "true"})

	if got := String(); got != "dev" {
		t.Errorf("String() = %q, want dev: there is no revision to name", got)
	}
}

func TestARevisionShorterThanTheAbbreviationIsNotTrimmed(t *testing.T) {
	pinVersion(t, "dev")
	stubBuildInfo(t, debug.BuildSetting{Key: "vcs.revision", Value: "abc12"})

	if got, want := String(), "dev (abc12)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestBuildInformationThatIsUnavailableIsNotFatal(t *testing.T) {
	pinVersion(t, "dev")
	original := readBuildInfo
	t.Cleanup(func() { readBuildInfo = original })
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }

	if got := String(); got != "dev" {
		t.Errorf("String() = %q, want dev", got)
	}
}
