package version

import "testing"

func TestStringFallsBackWhenTheBuildDidNotSetIt(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	Version = ""
	if got := String(); got != "dev" {
		t.Errorf("String() = %q, want dev", got)
	}
}

func TestStringReportsWhatTheBuildSet(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	Version = "v2.0.3"
	if got := String(); got != "v2.0.3" {
		t.Errorf("String() = %q, want v2.0.3", got)
	}
}
