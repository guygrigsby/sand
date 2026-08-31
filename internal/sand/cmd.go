package sand

import (
	"fmt"
	"os"
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
	flagPush      bool
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
			"Today that means PR review comments: `sand comments pull` puts them on the box\n" +
			"as markdown for an agent to address, `sand comments push` posts the replies back\n" +
			"to the exact review comments they answer.",
		// Execute prints the error itself, prefixed; cobra printing it too says it twice.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.PersistentFlags().StringVar(&flagHost, "host", "", "sandbox ssh alias or user@host (overrides config)")
	c.PersistentFlags().StringVar(&flagRemoteDir, "remote-dir", "", "base dir on the sandbox (overrides config)")
	c.AddCommand(commentsCmd(), configCmd(), signCmd(), skillCmd())
	return c
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
			o := SignOpts{
				Remote: flagRemote,
				Base:   flagBase,
				Yes:    flagYes,
				Push:   flagPush,
				In:     cmd.InOrStdin(),
				Out:    cmd.OutOrStdout(),
			}
			if len(args) > 0 {
				o.Branch = args[0]
			}
			return Sign(o)
		},
	}
	c.Flags().StringVar(&flagRemote, "remote", "origin", "remote to compare against and push to")
	c.Flags().StringVar(&flagBase, "base", "main", "base branch on that remote")
	c.Flags().BoolVarP(&flagYes, "yes", "y", false, "skip the confirmation before rewriting")
	c.Flags().BoolVar(&flagPush, "push", false, "push with --force-with-lease once verified, without asking")
	return c
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
		fmt.Println(got.Path, "("+state+")")
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
			"the box are preserved.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runPull(args) },
	}
	pull.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	pull.Flags().BoolVar(&flagAll, "all", false, "include threads already resolved on GitHub")
	pull.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be written, send nothing")

	push := &cobra.Command{
		Use:   "push [pr-number|pr-url]",
		Short: "Post drafted replies from the sandbox back to GitHub",
		Long: "Reads the thread files back off the sandbox and posts every non-empty reply as a\n" +
			"threaded reply to the review comment it answers, then marks it sent so a second\n" +
			"push is a no-op.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runPush(args) },
	}
	push.Flags().StringVar(&flagPR, "pr", "", "PR number or URL (default: the PR for the current branch)")
	push.Flags().BoolVar(&flagDryRun, "dry-run", false, "print the replies that would be posted, post nothing")

	c.AddCommand(pull, push)
	return c
}

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Show the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := os.ReadFile(ConfigPath())
			if os.IsNotExist(err) {
				return fmt.Errorf("%s does not exist yet (try `sand config init --host <alias>`)", ConfigPath())
			} else if err != nil {
				return err
			}
			fmt.Printf("# %s\n%s", ConfigPath(), b)
			return nil
		},
	}
	init := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := WriteDefault(flagHost)
			if err != nil {
				return err
			}
			fmt.Println("wrote", p)
			return nil
		},
	}
	c.AddCommand(init)
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

	if flagDryRun {
		fmt.Printf("dry run: would write the above to %s:%s\n", cfg.Host, remotePath)
		return nil
	}
	if err := sendDir(cfg.Host, outDir, remotePath); err != nil {
		return err
	}
	fmt.Printf("→ %s:%s (start at index.md)\n", cfg.Host, remotePath)
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

	var posted, skipped int
	for _, tf := range files {
		f, raw, t := tf.path, tf.raw, tf.thread
		if t.Reply == "" || t.Sent() {
			skipped++
			continue
		}

		body := composeReply(target, t)
		if t.Meta.Commit == "" {
			warn(fmt.Sprintf("%s: no commit recorded, posting the reply without one", filepath.Base(f)))
		}
		if flagDryRun {
			fmt.Printf("--- %s → comment %d (%s)\n%s\n", filepath.Base(f), t.Meta.CommentID, t.Location(), body)
			posted++
			continue
		}

		if posted > 0 {
			time.Sleep(betweenPosts)
		}
		url, err := Reply(target, t.Meta.CommentID, body)
		if err != nil {
			// One bad comment id or one throttle must not cost the rest of the batch.
			warn(fmt.Sprintf("%s: %v (left pending, safe to re-run)", filepath.Base(f), err))
			failed++
			continue
		}
		posted++
		fmt.Printf("posted %s (%s) → %s\n", filepath.Base(f), t.Location(), url)

		t.Meta.Status = StatusSent
		t.Meta.RepliedAt = time.Now().Format(time.RFC3339)
		t.Meta.ReplyURL = url
		out, err := ReplaceFrontMatter(raw, t.Meta)
		if err != nil {
			warn(fmt.Sprintf("%s: posted, but could not mark it sent: %v", filepath.Base(f), err))
			continue
		}
		if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
			return err
		}
	}

	if flagDryRun {
		fmt.Printf("dry run: %d reply(ies) would be posted, %d skipped\n", posted, skipped)
		return nil
	}
	fmt.Printf("%d posted, %d skipped, %d failed\n", posted, skipped, failed)
	if posted > 0 {
		// Mark them sent on the box too, so a re-run does not post twice.
		if err := sendDir(cfg.Host, dir, remotePath); err != nil {
			return fmt.Errorf("replies posted, but marking them sent on %s failed: %w", cfg.Host, err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d reply(ies) failed", failed)
	}
	return nil
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
