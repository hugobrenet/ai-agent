package buildinfo

import "testing"

func TestVersionIsNeverEmpty(t *testing.T) {
	if Version() == "" {
		t.Fatal("empty version")
	}
}

func TestVersionIsTheOneTheBuildSet(t *testing.T) {
	defer func(previous string) { version = previous }(version)
	version = "v1.2.3"
	if got := Version(); got != "v1.2.3" {
		t.Fatalf("version = %q", got)
	}
}
