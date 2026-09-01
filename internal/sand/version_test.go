package sand

import "testing"

// The point of Version is that something can always be named: a release says its tag, and every
// other build says what it was built from. Empty is the one answer that would make "which sand
// wrote this skill" unanswerable, which is the question it exists for.
func TestVersionNamesSomething(t *testing.T) {
	if got := Version(); got == "" {
		t.Fatal("Version() is empty")
	}

	old := version
	t.Cleanup(func() { version = old })
	version = "v9.9.9"
	if got := Version(); got != "v9.9.9" {
		t.Errorf("stamped version = %q, want the ldflags value", got)
	}
}
