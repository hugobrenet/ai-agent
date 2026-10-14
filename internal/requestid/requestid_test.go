package requestid

import (
	"regexp"
	"testing"
)

func TestNewRequestIDsAreDistinctLowercaseHex(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(first) || first == second {
		t.Fatalf("request IDs %q and %q", first, second)
	}
}

func TestContextCarriesTheRequestID(t *testing.T) {
	if FromContext(t.Context()) != "" {
		t.Fatal("a context without request ID returned one")
	}
	if got := FromContext(With(t.Context(), "id")); got != "id" {
		t.Fatalf("request ID = %q", got)
	}
}
