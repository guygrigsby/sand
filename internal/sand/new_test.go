package sand

import "testing"

// The branch name is the handoff between `new` and `up`: one writes it, the other reads the
// issue number back out. It was `guy/` in both, compiled in, which is the one thing in here
// that made the tool refuse to work for anybody else.
func TestIssueBranchRoundTripsUnderAnyPrefix(t *testing.T) {
	for _, prefix := range []string{"kim", "guy", "kim/wip", "", "/kim/"} {
		branch := issueBranch(prefix, 1532, "Instance dormancy: don't leak the fd!")
		want := branchPrefix(prefix) + "1532-instance-dormancy-don-t-leak-the-fd"
		if branch != want {
			t.Errorf("issueBranch(%q) = %q, want %q", prefix, branch, want)
		}
		n, ok := issueNumberFromBranch(prefix, branch)
		if !ok || n != 1532 {
			t.Errorf("issueNumberFromBranch(%q, %q) = %d, %v", prefix, branch, n, ok)
		}
	}

	// Someone else's branch is not this machine's issue: reading 1532 out of it would send
	// `up` off to open a PR against a branch it did not make.
	if _, ok := issueNumberFromBranch("kim", "guy/1532-instance-dormancy"); ok {
		t.Error("read an issue number out of another prefix's branch")
	}
	for _, branch := range []string{"kim/main", "kim/no-number-here", "kim/1532", "kim/0-zero"} {
		if _, ok := issueNumberFromBranch("kim", branch); ok {
			t.Errorf("%q read as an issue branch", branch)
		}
	}
}

// Unset is not a name: guessing $USER made one Mac's branches `guygrigsby/...` while the
// branches were actually `guy/...`, and `up` could no longer read its own handoff.
func TestBranchPrefixIsRequired(t *testing.T) {
	configHome(t)
	t.Setenv("USER", "kim")
	t.Setenv("LOGNAME", "kim")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchPrefix != "" {
		t.Errorf("branch_prefix = %q, want no default", c.BranchPrefix)
	}
	if v, err := Get("branch_prefix"); err != nil || v != "" {
		t.Fatalf("get branch_prefix = %q, %v; want empty", v, err)
	}
	if _, err := Set([][2]string{{"branch_prefix", "kim/sand"}}); err != nil {
		t.Fatal(err)
	}
	if v, err := Get("branch_prefix"); err != nil || v != "kim/sand" {
		t.Fatalf("get branch_prefix = %q, %v", v, err)
	}
}
