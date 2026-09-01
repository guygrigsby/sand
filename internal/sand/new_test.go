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

// Unset means this machine's user, so a coworker who never touches the config still gets
// their own name on their branches.
func TestBranchPrefixDefaultsToTheUser(t *testing.T) {
	configHome(t)
	t.Setenv("USER", "kim")
	t.Setenv("LOGNAME", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchPrefix != "kim" {
		t.Errorf("branch_prefix = %q, want the $USER default", c.BranchPrefix)
	}
	if _, err := Set([][2]string{{"branch_prefix", "kim/sand"}}); err != nil {
		t.Fatal(err)
	}
	if v, err := Get("branch_prefix"); err != nil || v != "kim/sand" {
		t.Fatalf("get branch_prefix = %q, %v", v, err)
	}
}
