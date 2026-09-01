package sand

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	flagHost      string
	flagRemoteDir string
	flagPR        string
	flagDryRun    bool
	flagAll       bool
	flagRemote    string
	flagBase      string
	flagYes       bool
	flagOtherAuth bool
	flagPush      bool
	flagAgent     string
	flagNoAgent   bool
	flagRepoDir   string
	flagLogLines  int
)

// betweenPosts is a base courtesy delay between reply POSTs. The replies endpoint sends
// notifications and is the documented way to trip GitHub's secondary rate limits, so
// replies go out one at a time, slowly, whatever else the tool parallelises.
var betweenPosts = time.Second

func Execute() {
	if err := root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sand:", err)
		os.Exit(1)
	}
}

func root() *cobra.Command {
	c := &cobra.Command{
		Use:   "sand",
		Short: "Development helper for the sandbox box",
		Long: "sand runs on the Mac and ferries work to and from the sandbox.\n\n" +
			"`sand new` starts an issue on the box. `sand comments pull` puts PR review\n" +
			"threads there for an agent, and `sand up` signs and publishes its work.",
		Version: Version(),
		// Execute prints the error itself, prefixed; cobra printing it too says it twice.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.PersistentFlags().StringVar(&flagHost, "host", "", "sandbox ssh alias or user@host (overrides config)")
	c.PersistentFlags().StringVar(&flagRemoteDir, "remote-dir", "", "base dir on the sandbox (overrides config)")
	c.AddCommand(ciCmd(), commentsCmd(), configCmd(), newCmd(), shotCmd(), signCmd(), skillCmd(),
		statusCmd(), upCmd())
	return c
}

func shotCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "shot [file]",
		Aliases: []string{"screenshot"},
		Short:   "Grab a screenshot and put it on the sandbox",
		Long: "Runs the interactive crop (screencapture -i, the cmd-shift-4 selection), sends the\n" +
			"image to <remote-dir>/" + shotDir + "/ on the box and copies that path to the clipboard,\n" +
			"so it can be pasted straight into a prompt for an agent running there.\n\n" +
			"With a file argument it sends that file instead of capturing, which is also how it\n" +
			"works anywhere there is no screen to grab.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := Resolve(flagHost, flagRemoteDir)
			if err != nil {
				return err
			}
			file := ""
			if len(args) > 0 {
				file = args[0]
			}
			return Shot(cfg, file, flagDryRun, cmd.OutOrStdout())
		},
	}
	c.Flags().BoolVar(&flagDryRun, "dry-run", false, "say what would be sent, send nothing")
	return c
}

func newCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "new <issue-number>",
		Short: "Start an issue on the sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNew(args)
		},
	}
	c.Flags().StringVar(&flagRemote, "remote", "origin", "remote to branch from")
	c.Flags().StringVar(&flagBase, "base", "main", "base branch")
	c.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be created, change nothing")
	return c
}

func upCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "up [pr-number|pr-url]",
		Aliases: []string{"push"},
		Short:   "Sign, push and post the replies: the whole Mac side in one command",
		Long: "Everything the Mac owes the loop once an agent on the box has answered the\n" +
			"threads, in order, with each step verified before the next one runs:\n\n" +
			"  1 sign      the commits on the PR's branch that are not signed yet\n" +
			"  2 push      --force-with-lease, then check the remote really moved\n" +
			"  3 verify    GitHub itself reports every commit of the PR as verified\n" +
			"  4 replies   post the drafted replies, which quote those commits\n\n" +
			"A step that fails stops the ones after it: a reply that quotes a hash nobody\n" +
			"has is the damage this order exists to prevent.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runUp(cmd, args) },
	}
	c.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	c.Flags().StringVar(&flagRemote, "remote", "origin", "remote to compare against and push to")
	c.Flags().StringVar(&flagBase, "base", "main", "base branch on that remote")
	c.Flags().BoolVarP(&flagYes, "yes", "y", false, "skip the confirmation before rewriting history")
	c.Flags().BoolVar(&flagOtherAuth, "allow-other-authors", false,
		"sign commits made by someone other than this machine's git identity")
	c.Flags().BoolVar(&flagDryRun, "dry-run", false, "say what each step would do, change nothing anywhere")
	return c
}

// runUp is the second half of the review loop in one command. It is the order that matters:
// signing rewrites hashes, the replies quote them, so signing has to be finished and pushed
// and confirmed by GitHub before a single reply goes out.
func runUp(cmd *cobra.Command, args []string) error {
	cfg, target, create, err := setupUp(args)
	if err != nil {
		return err
	}
	branch := target.Branch
	if branch == "" {
		return fmt.Errorf("gh did not say which branch %s#%d comes from", target.Slug(), target.Number)
	}

	var description []byte
	if create {
		if description, err = loadPRDescription(cfg, target); err != nil {
			return err
		}
		fmt.Printf("Issue:   %s#%d %q\n", target.Slug(), target.Number, target.Title)
	} else {
		fmt.Printf("PR:      %s#%d %q\n", target.Slug(), target.Number, target.Title)
	}
	fmt.Printf("Branch:  %s → %s/%s\n", branch, flagRemote, flagBase)
	if create {
		fmt.Printf("PR body: %s:%s/pr-description.md\n", cfg.Host, target.issuePath(cfg.RemoteDir))
	} else {
		fmt.Printf("Replies: %s:%s\n", cfg.Host, target.RemotePath(cfg.RemoteDir))
	}
	if flagDryRun {
		fmt.Println("dry run: nothing gets rewritten, pushed or posted")
	}

	fmt.Println("\n1/4 sign")
	boxURL, boxHost, boxDir := thisRepoOnBox()
	res, err := Sign(SignOpts{
		Branch:            branch,
		Remote:            flagRemote,
		Base:              flagBase,
		Yes:               flagYes,
		AllowOtherAuthors: flagOtherAuth,
		Box:               boxURL,
		BoxHost:           boxHost,
		BoxDir:            boxDir,
		// sign pushes what it rewrote, and a fully-signed branch the remote is behind; step 2
		// is what proves the remote agrees, and pushes only if something declined to.
		Push:   !flagDryRun,
		DryRun: flagDryRun,
		In:     cmd.InOrStdin(),
		Out:    os.Stdout,
	})
	if err != nil {
		return err
	}
	if res.Cancelled {
		fmt.Println("\nstopped at step 1: nothing signed, so nothing posted")
		return nil
	}
	fmt.Printf("  %s: %d commit(s), %d signed now, %d already signed and unmoved\n",
		branch, res.Total, res.Rewritten, res.Kept)
	// A rewrite that reached GitHub but not the box is the state that ends in two lineages,
	// so it gets a line of its own rather than being left in the signing output above.
	if res.Rewritten > 0 && res.Pushed && !res.BoxAligned {
		warn("the box is still on the pre-signing branch; the reasons are above and the next round " +
			"will refuse to sign until it is realigned")
	}

	fmt.Println("\n2/4 push")
	if err := ensurePushed(branch); err != nil {
		return err
	}

	if create {
		fmt.Println("\n3/4 open and verify on GitHub")
		if flagDryRun {
			fmt.Printf("  dry run: would open a PR for %s from %s\n", target.Title, branch)
			return nil
		}
		if target, err = createPullRequest(target, description); err != nil {
			return err
		}
		fmt.Printf("  opened %s\n", target.URL)
	} else {
		fmt.Println("\n3/4 verify on GitHub")
	}
	unverified, err := UnverifiedCommits(target)
	if err != nil {
		return fmt.Errorf("asking GitHub about the signatures: %w", err)
	}
	switch {
	case len(unverified) == 0:
		fmt.Printf("  GitHub reports every commit of %s#%d as verified\n", target.Slug(), target.Number)
	case flagDryRun:
		warn(fmt.Sprintf("GitHub currently reports %d commit(s) as unverified; step 1 is what fixes that",
			len(unverified)))
	default:
		for _, c := range unverified {
			fmt.Printf("  %s %q — %s\n", short(c.SHA), c.Subject, c.Reason)
		}
		return fmt.Errorf("%d commit(s) are signed here but not verified by GitHub, so no reply was posted: "+
			"the signing key is probably not on the GitHub account (Settings → SSH and GPG keys, as a signing key)",
			len(unverified))
	}

	if create {
		fmt.Println("\n4/4 replies\n  no review replies yet")
		return nil
	}
	fmt.Println("\n4/4 replies")
	return runPush(args)
}

// ensurePushed gets the branch to the remote and proves it landed, rather than trusting that
// a push that printed nothing did anything. A branch the remote does not have yet is pushed
// without a lease: there is nothing there to overwrite.
func ensurePushed(branch string) error {
	g := gitCmd{out: os.Stdout}
	local, err := g.capture("rev-parse", branch)
	if err != nil {
		return err
	}
	ref := flagRemote + "/" + branch
	remote, _ := g.capture("rev-parse", "--verify", "--quiet", ref)

	if remote == local {
		fmt.Printf("  %s is already at %s\n", ref, short(local))
		return nil
	}
	if flagDryRun {
		switch remote {
		case "":
			fmt.Printf("  dry run: would push %s to %s as a new branch\n", short(local), flagRemote)
		default:
			fmt.Printf("  dry run: would push %s over %s at %s\n", short(local), ref, short(remote))
		}
		return nil
	}

	args := []string{"push", "--force-with-lease", flagRemote, branch}
	if remote == "" {
		args = []string{"push", "--set-upstream", flagRemote, branch}
	}
	if err := g.run(args...); err != nil {
		return fmt.Errorf("pushing %s to %s failed, so nothing was posted: %w", branch, flagRemote, err)
	}
	after, err := g.capture("rev-parse", "--verify", "--quiet", ref)
	if err != nil || after != local {
		return fmt.Errorf("push reported success but %s is at %q, not %s", ref, after, short(local))
	}
	fmt.Printf("  %s → %s (was %s)\n", ref, short(local), cmp.Or(short(remote), "absent"))
	return nil
}

func signCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "sign [branch]",
		Short: "Sign the commits a sandbox branch adds, on the Mac",
		Long: "Imports the branch with aif, then re-creates every commit unique to it with a\n" +
			"signature, preserving the commit graph including merges. Verifies that every\n" +
			"rewritten commit is signed and that none went missing before offering to push.\n" +
			"Leaves a recovery branch behind either way.\n\n" +
			"Branch defaults to the current one, and is refused if it is main, master,\n" +
			"develop, trunk or release/*.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			boxURL, boxHost, boxDir := thisRepoOnBox()
			o := SignOpts{
				Remote:            flagRemote,
				Base:              flagBase,
				Yes:               flagYes,
				Push:              flagPush,
				DryRun:            flagDryRun,
				In:                cmd.InOrStdin(),
				Out:               cmd.OutOrStdout(),
				AllowOtherAuthors: flagOtherAuth,
				Box:               boxURL,
				BoxHost:           boxHost,
				BoxDir:            boxDir,
			}
			if len(args) > 0 {
				o.Branch = args[0]
			}
			_, err := Sign(o)
			return err
		},
	}
	c.Flags().StringVar(&flagRemote, "remote", "origin", "remote to compare against and push to")
	c.Flags().StringVar(&flagBase, "base", "main", "base branch on that remote")
	c.Flags().BoolVarP(&flagYes, "yes", "y", false, "skip the confirmation before rewriting")
	c.Flags().BoolVar(&flagOtherAuth, "allow-other-authors", false,
		"sign commits made by someone other than this machine's git identity")
	c.Flags().BoolVar(&flagPush, "push", false, "push with --force-with-lease once verified, without asking")
	c.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be signed, rewrite nothing and push nothing")
	return c
}

func statusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status [pr-number|pr-url]",
		Short: "Where the work is, on all three machines, and what to run next",
		Long: "Reads this Mac, the box and GitHub at once and prints one `next:` line.\n\n" +
			"Changes nothing anywhere. It does fetch: from the remote, so every count is\n" +
			"measured against current refs, and from the box into FETCH_HEAD, because\n" +
			"whether its commits are copies of pushed ones is a question about trees.\n\n" +
			"A branch with no open PR is fine; the GitHub half is simply skipped.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, target, hasPR, err := setupStatus(args)
			if err != nil {
				return err
			}
			boxURL, _, _ := thisRepoOnBox()
			return Status(StatusOpts{
				Cfg: cfg, Target: target, HasPR: hasPR,
				Remote: flagRemote, Base: flagBase,
				Box: boxURL, RepoDir: flagRepoDir,
				Out: cmd.OutOrStdout(),
			})
		},
	}
	c.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	c.Flags().StringVar(&flagRemote, "remote", "origin", "remote to measure against")
	c.Flags().StringVar(&flagBase, "base", "main", "base branch on that remote")
	c.Flags().StringVar(&flagRepoDir, "repo-dir", "", "the repo checkout on the box (default ~/projects/<repo>)")
	return c
}

// setupStatus is setup for a command that must survive there being no PR. Every other command
// here acts on one and can insist; status is the one you run to find out what state you are in,
// including "the branch exists and nothing has been opened for it yet".
func setupStatus(args []string) (Config, Target, bool, error) {
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return cfg, Target{}, false, err
	}
	arg, err := prArg(args)
	if err != nil {
		return cfg, Target{}, false, err
	}
	if arg != "" {
		target, err := ResolveTarget(arg)
		if err != nil {
			return cfg, Target{}, false, err
		}
		return cfg, target, true, target.LoadURL()
	}
	target, found, err := currentBranchPR()
	return cfg, target, found, err
}

func skillCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "skill",
		Short: "Install the box-side agent skill",
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Write the skill and link the agent harnesses at it",
		Long: "Run on the box. Writes the skill this binary carries to ~/" + canonicalSkillPath + "\n" +
			"and links every agent harness installed there at it, so one file serves all of\n" +
			"them and a re-install after an upgrade updates all of them. No checkout needed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runSkillInstall() },
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the skill this binary carries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := cmd.OutOrStdout().Write(skillDoc)
			return err
		},
	}
	c.AddCommand(install, show)
	return c
}

func runSkillInstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	got, err := InstallSkill(home)
	if got.Path != "" {
		state := "unchanged"
		if got.Updated {
			state = "written"
		}
		// The version, because the skill is text out of one particular binary: when the box
		// side and the tool disagree, this line is what says which `sand` wrote the file.
		fmt.Printf("%s (%s, from sand %s)\n", got.Path, state, Version())
	}
	for _, l := range got.Links {
		fmt.Println(l, "->", got.Path)
	}
	for _, h := range got.Absent {
		fmt.Printf("%s not installed here (no ~/%s), not linked\n", h.Name, h.marker)
	}
	return err
}

func commentsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "comments",
		Aliases: []string{"comment"},
		Short:   "Move PR review comments between GitHub and the sandbox",
	}

	pull := &cobra.Command{
		Use:   "pull [pr-number|pr-url]",
		Short: "Fetch review comments for a PR and put them on the sandbox",
		Long: "Fetches the unresolved inline review threads (plus review summary bodies as\n" +
			"context) for a PR and writes them to <remote-dir>/<owner>/<repo>/pr-<n>/ on the\n" +
			"sandbox, one markdown file per thread. Safe to re-run: replies already drafted on\n" +
			"the box are preserved.\n\n" +
			"Then starts an agent on the box in that repo's checkout to work the threads, and\n" +
			"holds the ssh open, streaming what it does. Ctrl-C stops watching and stops the\n" +
			"agent; the files stay, so a re-run picks up where it left off. --no-agent leaves\n" +
			"the files for a human or an agent already sitting on the box.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runPull(args) },
	}
	pull.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	pull.Flags().BoolVar(&flagAll, "all", false, "include threads already resolved on GitHub")
	pull.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be written, send nothing")
	pull.Flags().BoolVar(&flagNoAgent, "no-agent", false, "write the files and stop, starting nothing on the box")
	pull.Flags().StringVar(&flagAgent, "agent", "", "run this command on the box instead of the configured harness")
	pull.Flags().StringVar(&flagRepoDir, "repo-dir", "", "checkout on the box to run it in (default ~/projects/<repo>)")

	push := &cobra.Command{
		Use:   "push [pr-number|pr-url]",
		Short: "Post drafted replies from the sandbox back to GitHub",
		Long: "Reads the thread files back off the sandbox and posts every non-empty reply as a\n" +
			"threaded reply to the review comment it answers, then marks it sent so a second\n" +
			"push is a no-op.\n\n" +
			"The box had no key when it committed, so each reply's `commit:` hash is checked\n" +
			"against the pushed branch first and re-pointed at the signed commit that replaced\n" +
			"it. Refuses to post at all while GitHub reports any commit of the PR unverified.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runPush(args) },
	}
	push.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	push.Flags().StringVar(&flagRemote, "remote", "origin", "remote whose copy of the branch the commit hashes must match")
	push.Flags().BoolVar(&flagDryRun, "dry-run", false, "print the replies that would be posted, post nothing")

	c.AddCommand(pull, push)
	return c
}

func ciCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ci",
		Short: "Move failing CI checks to the sandbox",
	}

	pull := &cobra.Command{
		Use:   "pull [pr-number|pr-url]",
		Short: "Fetch a PR's failing checks and put them on the sandbox",
		Long: "Asks GitHub which of the PR's checks failed, fetches the failed steps of each\n" +
			"Actions run and writes one markdown file per failing check to\n" +
			"<remote-dir>/<owner>/<repo>/pr-<n>/ci/ on the sandbox. Safe to re-run: notes\n" +
			"already written on the box are preserved, and a check that has gone green keeps\n" +
			"its file, updated to say so.\n\n" +
			"Then starts an agent on the box in that repo's checkout, under the same lock as\n" +
			"`comments pull`, and streams what it does. There is no `ci push`: a red check is\n" +
			"answered by a commit, not by a comment, so `sand up` publishes the fix and the\n" +
			"next CI run is the verdict.\n\n" +
			"A check that is not a GitHub Actions run (Buildkite and anything else posting a\n" +
			"commit status) gets a file with its state and link and no log: this machine has\n" +
			"no client for it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runCIPull(args) },
	}
	pull.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	pull.Flags().BoolVar(&flagAll, "all", false, "list the passing and pending checks in index.md too")
	pull.Flags().IntVar(&flagLogLines, "log-lines", defaultLogLines, "lines of each failed log to keep, from the end")
	pull.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be written, send nothing")
	pull.Flags().BoolVar(&flagNoAgent, "no-agent", false, "write the files and stop, starting nothing on the box")
	pull.Flags().StringVar(&flagAgent, "agent", "", "run this command on the box instead of the configured harness")
	pull.Flags().StringVar(&flagRepoDir, "repo-dir", "", "checkout on the box to run it in (default ~/projects/<repo>)")

	c.AddCommand(pull)
	return c
}

func runCIPull(args []string) error {
	cfg, target, err := setup(args)
	if err != nil {
		return err
	}
	if err := target.LoadURL(); err != nil {
		return err
	}

	checks, err := FetchChecks(target)
	if err != nil {
		return err
	}
	failing := map[string]Check{}
	var others []Check
	for _, c := range checks {
		if c.Failed() {
			failing[c.Name] = c
			continue
		}
		others = append(others, c)
	}

	ciPath := target.CIPath(cfg.RemoteDir)

	// Read what is on the box first, so notes written there survive a re-pull.
	existingDir, err := fetchDir(cfg.Host, ciPath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(existingDir)
	onBox, err := loadCIFiles(existingDir)
	if err != nil {
		return err
	}

	files := make([]CIFailure, 0, len(failing)+len(onBox))
	for _, c := range checks {
		if c.Failed() {
			files = append(files, ciFile(target, c, flagLogLines))
		}
	}
	// A file already on the box whose check is no longer failing is refreshed rather than
	// left alone: the transport adds and never deletes, so leaving it would have the agent
	// reading `bucket: fail` about a check that is green, with a log from a run that has
	// been superseded. The merge below gives it its notes and its commit back.
	current := byName(checks)
	for _, old := range onBox {
		if _, still := failing[old.Meta.Check]; still {
			continue
		}
		files = append(files, greenAgain(old, current[old.Meta.Check]))
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Meta.Check < files[j].Meta.Check })

	outDir, err := os.MkdirTemp("", "sand-ci-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(outDir)

	byCheck := map[string]CIFailure{}
	for _, f := range onBox {
		byCheck[f.Meta.Check] = f
	}
	var pending, noted int
	for i := range files {
		f := &files[i]
		if old, ok := byCheck[f.Meta.Check]; ok {
			f.Merge(old)
		}
		if f.Meta.Bucket == bucketFail {
			if f.Fixed() || f.Notes != "" {
				noted++
			} else {
				pending++
			}
		}
		body, err := f.Render()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, f.Filename()), []byte(body), 0o644); err != nil {
			return err
		}
	}
	listed := others
	if !flagAll {
		listed = nil
	}
	index := RenderCIIndex(target, files, listed, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(outDir, "index.md"), []byte(index), 0o644); err != nil {
		return err
	}

	fmt.Printf("%s#%d %q: %d failing check(s) of %d — %d to fix, %d already worked on\n",
		target.Slug(), target.Number, target.Title, len(failing), len(checks), pending, noted)
	for _, f := range files {
		fmt.Printf("  %-28s %-10s %-8s %s\n", f.Filename(), f.Meta.Bucket, f.Meta.Status, f.Meta.Check)
	}
	if len(failing) == 0 {
		fmt.Println("nothing failing" + hintAll(others))
	}

	start := !flagNoAgent && pending > 0
	var run AgentRun
	if start {
		if run, err = agentRun(cfg, target, ciPrompt(target, ciPath, pending)); err != nil {
			return err
		}
	}

	if flagDryRun {
		fmt.Printf("dry run: would write the above to %s:%s\n", cfg.Host, ciPath)
		if start {
			fmt.Printf("dry run: would run in %s:%s\n  %s <prompt>\n",
				cfg.Host, run.Dir, strings.Join(run.Command, " "))
		}
		return nil
	}
	if err := sendDir(cfg.Host, outDir, ciPath); err != nil {
		return err
	}
	fmt.Printf("→ %s:%s (start at index.md)\n", cfg.Host, ciPath)
	if !start {
		if !flagNoAgent && pending == 0 {
			fmt.Println("nothing to fix, so no agent started")
		}
		return nil
	}

	fmt.Printf("\nstarting the agent in %s:%s, Ctrl-C to stop it\n\n", cfg.Host, run.Dir)
	agentErr := RunAgent(run)
	// Either way: an agent that died halfway may still have fixed most of them, and which
	// ones is the difference between re-running and reading the files by hand.
	if err := reportCIProgress(cfg, ciPath); err != nil {
		warn(fmt.Sprintf("could not read the checks back: %v", err))
	}
	return agentErr
}

// hintAll is the difference between "the PR is green" and "gh answered with nothing", which
// look the same from a table with no rows in it.
func hintAll(others []Check) string {
	switch {
	case len(others) == 0:
		return ": gh reported no checks at all for this PR"
	case flagAll:
		return fmt.Sprintf(": %d other check(s), listed in index.md", len(others))
	}
	return fmt.Sprintf(": %d other check(s), --all lists them", len(others))
}

// ciFile is one failing check as a file, log fetched when there is one to fetch. A log that
// cannot be read is a note in the file rather than an error: the other checks still have
// theirs, and the link is in the front matter either way.
func ciFile(t Target, c Check, logLines int) CIFailure {
	f := CIFailure{Meta: CIMeta{
		Check: c.Name, Workflow: c.Workflow, Bucket: c.Bucket, State: c.State,
		Link: c.Link, RunID: c.RunID,
		PulledAt: time.Now().Format(time.RFC3339), Status: StatusPending,
	}}
	if c.RunID == "" {
		f.LogNote = "This check is not a GitHub Actions run, so the Mac has no way to fetch its " +
			"log. Open the link above."
		return f
	}
	log, dropped, err := FailedLog(t, c.RunID, logLines)
	if err != nil {
		warn(fmt.Sprintf("no log for %q: %v", c.Name, err))
		f.LogNote = fmt.Sprintf("The log could not be fetched (%v). Open the link above.", err)
		return f
	}
	f.Log, f.Dropped = log, dropped
	return f
}

// greenAgain refreshes a file whose check has stopped failing. c is the zero Check when the
// check is not in the list at all any more, which happens when a workflow is renamed or
// removed: the file says that rather than quietly keeping the old verdict.
func greenAgain(old CIFailure, c Check) CIFailure {
	f := CIFailure{Meta: old.Meta} // the notes come back through Merge, like every other file's
	f.Meta.Bucket, f.Meta.State = c.Bucket, c.State
	f.Meta.PulledAt = time.Now().Format(time.RFC3339)
	if c.Name == "" {
		f.LogNote = "GitHub no longer reports this check on the PR at all."
		return f
	}
	if c.Link != "" {
		f.Meta.Link, f.Meta.RunID = c.Link, c.RunID
	}
	f.LogNote = fmt.Sprintf("This check is not failing any more (%s). Nothing to do.", c.Bucket)
	return f
}

func byName(checks []Check) map[string]Check {
	out := make(map[string]Check, len(checks))
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

// loadCIFiles reads the CI files in dir, warning about and skipping any it cannot parse. One
// mangled file is not worth losing the notes in the others over.
func loadCIFiles(dir string) ([]CIFailure, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "ci-*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []CIFailure
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := ParseCIFailure(string(raw))
		if err != nil {
			warn(fmt.Sprintf("%s: %v (skipped)", filepath.Base(p), err))
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// reportCIProgress says which checks came back with a fix recorded and which were left.
func reportCIProgress(cfg Config, ciPath string) error {
	dir, err := fetchDir(cfg.Host, ciPath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	files, err := loadCIFiles(dir)
	if err != nil {
		return err
	}

	var fixed, left int
	fmt.Println()
	for _, f := range files {
		if f.Meta.Bucket != bucketFail {
			continue
		}
		switch {
		case f.Fixed() || f.Notes != "":
			fixed++
			fmt.Printf("  worked %-28s %s\n", f.Meta.Check, f.Meta.Commit)
		default:
			left++
			fmt.Printf("  left   %-28s\n", f.Meta.Check)
		}
	}
	fmt.Printf("%d worked on, %d left\n", fixed, left)
	if fixed > 0 {
		fmt.Println("next: sand up (signs and pushes the fixes; CI re-runs on the new head)")
	}
	return nil
}

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Show the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(ConfigPath())
			if os.IsNotExist(err) {
				return fmt.Errorf("%s does not exist yet (try `sand config init`)", ConfigPath())
			} else if err != nil {
				return err
			}
			fmt.Printf("# %s\n%s", ConfigPath(), b)
			return nil
		},
	}
	init := &cobra.Command{
		Use:   "init",
		Short: "Create the config file, or bring an existing one up to date",
		Long: "Writes every key this version knows, with its comment, keeping every value the\n" +
			"file already holds, so running it twice writes the same file twice and a config\n" +
			"written by an older sand gains the keys added since.\n\n" +
			"Asks for the sandbox host when neither --host nor the file already names one.\n" +
			"There is no default for it: it names one machine on one tailnet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			p, err := InitConfig(flagHost, cmd.InOrStdin(), out)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "wrote", p)
			// An unattended run answers no prompt, so say what is still missing rather
			// than let the next command be the one to discover it.
			if v, _ := Get("host"); v == "" {
				warn("no host yet: `sand config set host <alias>` before anything that talks to the box")
			}
			return nil
		},
	}
	get := &cobra.Command{
		Use:   "get <key>",
		Short: "Print one effective config value",
		Long: "Prints the value this tool would use for a key — the file, with env and the\n" +
			"built-in defaults applied — and nothing else, so a script can read it. Keys: " +
			strings.Join(ConfigKeys(), ", ") + ".",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := Get(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}
	set := &cobra.Command{
		Use:   "set <key> <value> [<key> <value>...]",
		Short: "Set config values, creating the file if needed",
		Long: "Sets any of: " + strings.Join(ConfigKeys(), ", ") + ".\n\n" +
			"Rewrites the whole file from the values it holds, so the comments stay and every\n" +
			"key it does not mention keeps what it had. An unknown key is an error.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 || len(args)%2 != 0 {
				return fmt.Errorf("want key/value pairs, got %d argument(s)", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var pairs [][2]string
			for i := 0; i < len(args); i += 2 {
				pairs = append(pairs, [2]string{args[i], args[i+1]})
			}
			p, err := Set(pairs)
			if err != nil {
				return err
			}
			for _, kv := range pairs {
				fmt.Printf("%s: %s\n", kv[0], kv[1])
			}
			fmt.Println("wrote", p)
			return nil
		},
	}
	c.AddCommand(init, get, set)
	return c
}

// prArg takes the PR from either the positional argument or --pr; they mean the same
// thing and only one can win.
func prArg(args []string) (string, error) {
	if len(args) > 0 && flagPR != "" && args[0] != flagPR {
		return "", fmt.Errorf("PR given twice, as %q and --pr %q", args[0], flagPR)
	}
	if len(args) > 0 {
		return args[0], nil
	}
	return flagPR, nil
}

func setup(args []string) (Config, Target, error) {
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return cfg, Target{}, err
	}
	arg, err := prArg(args)
	if err != nil {
		return cfg, Target{}, err
	}
	target, err := ResolveTarget(arg)
	return cfg, target, err
}

func warn(msg string) { fmt.Fprintln(os.Stderr, "sand: warning:", msg) }

// thisRepoOnBox is the box's checkout of this repo, twice over: as the git URL signing pushes the
// history it rewrote to, and as the ssh host and path the pre-flight asks questions over. Both,
// because a push URL cannot carry a question and splitting one back apart guesses at where the
// host ends. The box keeps every repo at ~/projects/<repo> (checkoutDir), and both forms are
// relative to the login home, so they land in the same place.
//
// The repo name comes from this checkout's directory rather than from `gh`: signing is the one
// command in here that needs nothing from GitHub, and asking it for a name this machine already
// knows would make a missing token break history rewriting. All three are empty when there is no
// configured host or this is not a checkout, which alignBox reports rather than guessing at a box.
func thisRepoOnBox() (url, host, dir string) {
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return "", "", ""
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", ""
	}
	dir = "projects/" + segment(filepath.Base(strings.TrimSpace(string(top))))
	return cfg.Host + ":" + dir, cfg.Host, dir
}

// agentRun is the agent both pulls start: same box, same checkout, same lock, only the prompt
// differs. One place, because the lock is the part that must not vary — two pulls that named
// it differently would each think they were the only agent in the tree.
func agentRun(cfg Config, target Target, prompt string) (AgentRun, error) {
	argv := strings.Fields(flagAgent)
	if len(argv) == 0 {
		var err error
		if argv, err = agentCommand(cfg); err != nil {
			return AgentRun{}, err
		}
	}
	return AgentRun{
		Host:    cfg.Host,
		Dir:     cmp.Or(flagRepoDir, checkoutDir(target)),
		Lock:    agentLock(cfg.RemoteDir, target.Repo),
		Command: argv,
		Prompt:  prompt,
		Out:     os.Stdout,
	}, nil
}

func runPull(args []string) error {
	cfg, target, err := setup(args)
	if err != nil {
		return err
	}

	threads, reviews, err := Fetch(&target, warn)
	if err != nil {
		return err
	}
	if !flagAll {
		kept := threads[:0]
		skipped := 0
		for _, t := range threads {
			if t.Meta.Resolved {
				skipped++
				continue
			}
			kept = append(kept, t)
		}
		threads = kept
		if skipped > 0 {
			fmt.Printf("skipped %d resolved thread(s); --all includes them\n", skipped)
		}
	}
	sort.SliceStable(threads, func(i, j int) bool {
		if threads[i].Meta.Path != threads[j].Meta.Path {
			return threads[i].Meta.Path < threads[j].Meta.Path
		}
		return threads[i].Meta.Line < threads[j].Meta.Line
	})

	remotePath := target.RemotePath(cfg.RemoteDir)

	// Read what is already on the box so drafts survive a re-pull.
	existingDir, err := fetchDir(cfg.Host, remotePath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(existingDir)
	onBox, _, err := loadThreadFiles(existingDir)
	if err != nil {
		return err
	}
	existing := byCommentID(onBox)

	outDir, err := os.MkdirTemp("", "sand-pull-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(outDir)

	var news, updated, replied int
	for i := range threads {
		t := &threads[i]
		if old, ok := existing[t.Meta.CommentID]; ok {
			t.Merge(old)
			if t.Sent() {
				replied++
			} else {
				updated++
			}
		} else {
			news++
		}
		body, err := t.Render()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, t.Filename()), []byte(body), 0o644); err != nil {
			return err
		}
	}
	index := RenderIndex(target, threads, reviews, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(outDir, "index.md"), []byte(index), 0o644); err != nil {
		return err
	}

	fmt.Printf("%s#%d %q: %d thread(s) — %d new, %d updated, %d already replied\n",
		target.Slug(), target.Number, target.Title, len(threads), news, updated, replied)
	for _, t := range threads {
		fmt.Printf("  %-14s %-28s @%-16s %s\n", t.Filename(), t.Location(), t.Meta.Author, t.Meta.Status)
	}

	// An agent with nothing pending would read the files, find them all answered and
	// spend a turn saying so.
	start := !flagNoAgent && len(threads)-replied > 0
	var run AgentRun
	if start {
		if run, err = agentRun(cfg, target, agentPrompt(target, remotePath, len(threads)-replied)); err != nil {
			return err
		}
	}

	if flagDryRun {
		fmt.Printf("dry run: would write the above to %s:%s\n", cfg.Host, remotePath)
		if start {
			fmt.Printf("dry run: would run in %s:%s\n  %s <prompt>\n",
				cfg.Host, run.Dir, strings.Join(run.Command, " "))
		}
		return nil
	}
	if err := sendDir(cfg.Host, outDir, remotePath); err != nil {
		return err
	}
	fmt.Printf("→ %s:%s (start at index.md)\n", cfg.Host, remotePath)
	if !start {
		if flagNoAgent {
			return nil
		}
		fmt.Println("nothing pending, so no agent started")
		return nil
	}

	fmt.Printf("\nstarting the agent in %s:%s, Ctrl-C to stop it\n\n", cfg.Host, run.Dir)
	agentErr := RunAgent(run)
	// Report either way: the agent may have answered most of the threads before it died,
	// and knowing which is the difference between re-running and reading them by hand.
	if err := reportProgress(cfg, target, remotePath); err != nil {
		warn(fmt.Sprintf("could not read the threads back: %v", err))
	}
	return agentErr
}

// reportProgress says which threads came back answered, and what to run next. This is the
// half of "hold the tunnel open" that matters after the agent has stopped talking.
func reportProgress(cfg Config, target Target, remotePath string) error {
	dir, err := fetchDir(cfg.Host, remotePath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	files, _, err := loadThreadFiles(dir)
	if err != nil {
		return err
	}

	var answered, left int
	fmt.Println()
	for _, tf := range files {
		t := tf.thread
		switch {
		case t.Sent():
			continue
		case t.Reply != "":
			answered++
			fmt.Printf("  answered %-14s %-28s %s\n", t.Filename(), t.Location(), t.Meta.Commit)
		default:
			left++
			fmt.Printf("  left     %-14s %s\n", t.Filename(), t.Location())
		}
	}
	fmt.Printf("%d answered, %d left\n", answered, left)
	if answered > 0 {
		branch := cmp.Or(target.Branch, "<branch>")
		fmt.Printf("next: sand up %d (signs %s, pushes it, then posts the replies)\n",
			target.Number, branch)
	}
	return nil
}

func runPush(args []string) error {
	cfg, target, err := setup(args)
	if err != nil {
		return err
	}
	if err := target.LoadURL(); err != nil {
		return err
	}
	remotePath := target.RemotePath(cfg.RemoteDir)

	dir, err := fetchDir(cfg.Host, remotePath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	files, failed, err := loadThreadFiles(dir)
	if err != nil {
		return err
	}
	if len(files)+failed == 0 {
		return fmt.Errorf("nothing at %s:%s — run `sand comments pull` first", cfg.Host, remotePath)
	}

	// Every reply quotes the commit that fixed the thread, and signing the branch rewrites
	// those hashes, so posting first publishes hashes that are about to stop existing.
	// Only worth checking when there is something to post.
	g := gitCmd{out: os.Stdout}
	branchRef := ""
	// What this account has already posted, per thread. The box's `status: sent` is a
	// cache of this and can be lost (see the posted map below), so GitHub is asked.
	posted := map[int64][]string{}
	if pendingReplies(files) > 0 {
		if err := requireSignedCommits(target); err != nil {
			if !flagDryRun {
				return err
			}
			warn(err.Error())
		}
		if p, err := alreadyPosted(target); err != nil {
			// Fail closed. Posting without knowing what is already there is how the same
			// reply goes out twice, and every subscriber gets notified twice.
			if !flagDryRun {
				return fmt.Errorf("cannot tell which replies are already on %s#%d, so nothing was posted: %w",
					target.Slug(), target.Number, err)
			}
			warn(fmt.Sprintf("cannot check for replies already posted: %v", err))
		} else {
			posted = p
		}
		if target.Branch != "" {
			branchRef = flagRemote + "/" + target.Branch
			// The hashes are checked against the pushed branch, so a stale tracking ref
			// would condemn perfectly good commits. Offline, this fails and the checks
			// below degrade to "cannot say".
			_, _ = g.capture("fetch", "--quiet", flagRemote)
		}
	}

	var sent, skipped, recovered int
	for _, tf := range files {
		f, raw, t := tf.path, tf.raw, tf.thread
		if t.Reply == "" || t.Sent() {
			skipped++
			continue
		}

		name := filepath.Base(f)

		// Posted last time, but the box never found out: the marking write is the step
		// after the POST, and anything from a dropped ssh to a Ctrl-C lands in between.
		// Re-marking it is the whole recovery; posting it again is what we are avoiding.
		if alreadySaid(posted[t.Meta.CommentID], t.Reply) {
			fmt.Printf("%s: this reply is already on the thread, marking it sent\n", name)
			if err := markSent(f, raw, &t, ""); err != nil {
				warn(fmt.Sprintf("%s: %v", name, err))
			}
			recovered++
			continue
		}
		if t.Meta.Commit == "" {
			warn(fmt.Sprintf("%s: no commit recorded, posting the reply without one", name))
		} else if branchRef != "" {
			// The box had no key, so what the agent wrote down is the hash of an unsigned
			// commit that signing has since replaced.
			switch h, state := commitOnBranch(g, t.Meta.Commit, branchRef); state {
			case commitMoved:
				fmt.Printf("%s: signing moved %s to %s\n", name, t.Meta.Commit, h)
				t.Meta.Commit = h
			case commitGone:
				warn(fmt.Sprintf("%s: commit %s is not on %s and nothing there matches it; "+
					"left pending rather than posting a dead link (re-pull to let the agent recheck)",
					name, t.Meta.Commit, branchRef))
				failed++
				continue
			case commitAmbiguous:
				warn(fmt.Sprintf("%s: %s matches more than one commit on %s (%s); left pending rather "+
					"than quoting a guess (say which in `commit:` and re-run)",
					name, t.Meta.Commit, branchRef, h))
				failed++
				continue
			case commitUnknown:
				warn(fmt.Sprintf("%s: cannot check %s against %s from this checkout, posting it as recorded",
					name, t.Meta.Commit, branchRef))
			}
		}
		body := composeReply(target, t)
		if flagDryRun {
			fmt.Printf("--- %s → comment %d (%s)\n%s\n", name, t.Meta.CommentID, t.Location(), body)
			sent++
			continue
		}

		if sent > 0 {
			time.Sleep(betweenPosts)
		}
		url, err := Reply(target, t.Meta.CommentID, body)
		if err != nil {
			// One bad comment id or one throttle must not cost the rest of the batch.
			warn(fmt.Sprintf("%s: %v (left pending, safe to re-run)", name, err))
			failed++
			continue
		}
		sent++
		fmt.Printf("posted %s (%s) → %s\n", name, t.Location(), url)

		if err := markSent(f, raw, &t, url); err != nil {
			// The reply is out. Losing this write is survivable now: the next run reads
			// the thread off GitHub and marks it rather than posting it again.
			warn(fmt.Sprintf("%s: posted, but could not mark it sent locally: %v", name, err))
		}
	}

	if flagDryRun {
		fmt.Printf("dry run: %d reply(ies) would be posted, %d skipped\n", sent, skipped)
		return nil
	}
	fmt.Printf("%d posted, %d skipped, %d failed", sent, skipped, failed)
	if recovered > 0 {
		fmt.Printf(", %d already posted and re-marked", recovered)
	}
	fmt.Println()
	if sent+recovered > 0 {
		// Carry the markings back, so the usual case costs no GitHub calls next time. This
		// failing is no longer a double-post: it is a slower next run.
		if err := sendDir(cfg.Host, dir, remotePath); err != nil {
			warn(fmt.Sprintf("could not mark replies sent on %s (%v); nothing was posted twice, "+
				"the next run reads the thread from GitHub and re-marks them", cfg.Host, err))
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d reply(ies) failed", failed)
	}
	return nil
}

// alreadyPosted asks GitHub what this account has already replied on each thread of the PR.
//
// The box's `status: sent` is only a cache of this. It is written after the POST, so every
// way of dying in between (dropped ssh, rebooted box, full disk, Ctrl-C in a batch that
// sleeps a second between replies) leaves a reply posted and recorded as pending, and the
// message telling the operator to re-run then posts it again. GitHub is the copy that
// cannot be lost, so it is the one that decides.
func alreadyPosted(t Target) (map[int64][]string, error) {
	viewer, err := ViewerLogin()
	if err != nil {
		return nil, err
	}
	threads, _, err := Fetch(&t, func(string) {}) // warnings belong to pull, not here
	if err != nil {
		return nil, err
	}
	return PostedReplies(threads, viewer), nil
}

// markSent records the reply in the thread file's front matter, leaving the body untouched.
// An empty url means the reply was found already on GitHub rather than posted just now, so
// there is no new comment to link.
func markSent(path, raw string, t *Thread, url string) error {
	t.Meta.Status = StatusSent
	if t.Meta.RepliedAt == "" {
		t.Meta.RepliedAt = time.Now().Format(time.RFC3339)
	}
	if url != "" {
		t.Meta.ReplyURL = url
	}
	out, err := ReplaceFrontMatter(raw, t.Meta)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

func pendingReplies(files []threadFile) int {
	n := 0
	for _, tf := range files {
		if tf.thread.Reply != "" && !tf.thread.Sent() {
			n++
		}
	}
	return n
}

// requireSignedCommits fails when GitHub does not report every commit of the PR as
// verified, naming the offenders and the command that fixes them.
func requireSignedCommits(t Target) error {
	unverified, err := UnverifiedCommits(t)
	if err != nil {
		return fmt.Errorf("checking commit signatures: %w", err)
	}
	if len(unverified) == 0 {
		return nil
	}
	branch := t.Branch
	if branch == "" {
		branch = "<branch>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d commit(s) on %s are not signed:", len(unverified), branch)
	const show = 5
	for i, c := range unverified {
		if i == show {
			fmt.Fprintf(&b, "\n  ... and %d more", len(unverified)-show)
			break
		}
		fmt.Fprintf(&b, "\n  %s %q — %s", short(c.SHA), c.Subject, c.Reason)
	}
	fmt.Fprintf(&b, "\nsign them first, then push the replies:\n  sand sign %s", branch)
	return fmt.Errorf("%s", b.String())
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// composeReply is what actually lands on GitHub: the agent's words, then the commit
// that fixed it. A reply with no commit reference is half an answer, so the link is
// part of the format rather than something to remember.
func composeReply(t Target, th Thread) string {
	body := strings.TrimSpace(th.Reply)
	if c := strings.TrimSpace(th.Meta.Commit); c != "" {
		body += fmt.Sprintf("\n\nFixed in [`%s`](%s)", c, t.CommitURL(c))
	}
	return body
}

// threadFile is a thread as it exists on the box: where it is, what it says, and what
// parsed out of it. push needs the raw text to rewrite only the front matter.
type threadFile struct {
	path   string
	raw    string
	thread Thread
}

// loadThreadFiles reads the thread files in dir, in name order, warning about and
// skipping any it cannot parse — bad reports the number skipped. Neither caller should
// die over one mangled file, but push counts them: a file it cannot read may be holding
// a reply that will now never go out.
func loadThreadFiles(dir string) (files []threadFile, bad int, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "c-*.md"))
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(paths)
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, bad, err
		}
		t, err := Parse(string(raw))
		if err != nil {
			warn(fmt.Sprintf("%s: %v (skipped)", filepath.Base(p), err))
			bad++
			continue
		}
		files = append(files, threadFile{path: p, raw: string(raw), thread: t})
	}
	return files, bad, nil
}

// byCommentID keys thread files by the comment they reply to, for the merge on re-pull.
func byCommentID(files []threadFile) map[int64]Thread {
	out := make(map[int64]Thread, len(files))
	for _, f := range files {
		out[f.thread.Meta.CommentID] = f.thread
	}
	return out
}
