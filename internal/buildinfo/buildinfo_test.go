package buildinfo

import "testing"

func TestVersionIsNeverEmpty(t *testing.T) {
	if Version() == "" {
		t.Fatal("empty version")
	}
}
