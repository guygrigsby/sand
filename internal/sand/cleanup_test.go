package sand

import (
	"strings"
	"testing"
)

func TestCleanupDeletesOnlySandRecoveryBranchesAfterConfirmation(t *testing.T) {
	dir, _ := signRepo(t)
	for _, branch := range []string{
		"feature-before-signing-20260904120000",
		"guy-topic-before-import-20260904120100-2",
		"feature-before-signing-not-a-timestamp",
		"ordinary-branch",
	} {
		mustRun(t, dir, "git", "branch", branch)
	}

	var out strings.Builder
	cmd := cleanupCmd()
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("decline: %v", err)
	}
	if got := mustRun(t, dir, "git", "branch", "--list", "*-before-*"); strings.Count(got, "\n")+1 != 3 {
		t.Fatalf("decline deleted a branch:\n%s", got)
	}

	out.Reset()
	cmd = cleanupCmd()
	cmd.SetArgs([]string{"--yes"})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, gone := range []string{
		"feature-before-signing-20260904120000",
		"guy-topic-before-import-20260904120100-2",
	} {
		if got := mustRun(t, dir, "git", "branch", "--list", gone); got != "" {
			t.Errorf("kept recovery branch %s", gone)
		}
	}
	for _, kept := range []string{"feature-before-signing-not-a-timestamp", "ordinary-branch"} {
		if got := mustRun(t, dir, "git", "branch", "--list", kept); got == "" {
			t.Errorf("deleted non-recovery branch %s", kept)
		}
	}
	if !strings.Contains(out.String(), "Deleted 2 recovery branches") {
		t.Errorf("output did not report deletion:\n%s", out.String())
	}
}
