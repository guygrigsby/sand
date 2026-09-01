package sand

// status answers "where is the work, and what do I run next" in one read-only pass over all
// three machines. It decides nothing and moves nothing: it reads, counts, and prints one
// `next:` line.
//
// It exists because the failure this loop is prone to is invisible from either machine's
// `git log`. Signing re-creates commits, so a box left on the pre-signing chain holds unsigned
// copies of commits already pushed, identical trees and different hashes, and the first thing
// that says so today is a rejected push three commands later. `sand sign` refuses that state;
// this is how to see it before running anything.
//
// Read-only means no ref this checkout is sitting on moves, and nothing is written to the box
// or to GitHub. It does fetch: from `<remote>` to refresh the remote-tracking refs every count
// below is measured against, and from the box into FETCH_HEAD, because whether a commit is a
// copy of a pushed one is a question about trees, and a hash on its own cannot answer it.

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// StatusOpts is one status run. Target is only fully populated when HasPR.
type StatusOpts struct {
	Cfg     Config
	Target  Target
	HasPR   bool
	Remote  string
	Base    string
	Box     string // the box's checkout as a git URL, empty when there is no host or no checkout
	RepoDir string // the checkout on the box, empty for the default ~/projects/<repo>
	Out     io.Writer
}

// macState is this checkout.
type macState struct {
	Branch   string
	Dirty    bool
	Commits  int // unique to the branch, over <remote>/<base>
	Unsigned int
	Ahead    int // over <remote>/<branch>
	Behind   int
	NoRemote bool // <remote>/<branch> does not exist: never pushed
	Err      error
}

// boxState is the box: its checkout, and the files sand has left there.
type boxState struct {
	Branch   string
	Head     string
	Missing  bool // no checkout at the path we looked
	Dirty    int  // tracked files with uncommitted changes, which is what blocks the realigning push
	Agent    bool // something holds the agent lock for this repo
	Commits  int  // unique to the box's branch, over <remote>/<base>
	Unsigned int
	Dups     []dupCommit // unsigned copies of commits already on the remote

	Threads, Drafted, Sent int // thread files, ones with a reply waiting to go out, ones posted
	CIFailing, CINoted     int
	files                  []threadFile // kept for the comparison against GitHub's copy

	Err     error
	FileErr error
	DupErr  error
}

// hubState is what GitHub says, which is the authority for all three of these.
type hubState struct {
	Threads    int // unresolved
	New        int // unresolved threads the box has not seen, or has an older copy of
	Failing    int
	Unverified int
	unresolved []Thread
	Err        error
}

type statusReport struct {
	mac macState
	box boxState
	hub hubState
}

// Status gathers and prints. The three groups are three independent round trips, so they run
// at once; the one fetch they share happens first, because two git fetches in one repository
// fight over the same lock for no gain.
func Status(o StatusOpts) error {
	g := gitCmd{out: io.Discard}
	var rep statusReport
	if err := g.run("fetch", "--quiet", o.Remote); err != nil {
		// Not fatal: every count below is still readable, just as of the last fetch, and a
		// status that refuses to print because the network is down is a status nobody can use.
		fmt.Fprintf(o.Out, "warning: could not fetch %s, counts are as of the last fetch: %v\n\n", o.Remote, err)
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); rep.mac = macStatus(g, o) }()
	go func() { defer wg.Done(); rep.box = boxStatus(o) }()
	go func() { defer wg.Done(); rep.hub = hubStatus(o) }()
	wg.Wait()

	// The box's branch has to be fetched to be reasoned about, and that is a git write to this
	// repository, so it waits for the group that reads it rather than racing the others.
	lineage(g, o, &rep.box)
	rep.hub.New = newOnGitHub(o, rep)

	renderStatus(o, rep)
	return nil
}

func macStatus(g gitCmd, o StatusOpts) macState {
	var s macState
	branch, err := g.capture("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return macState{Branch: "(detached)", Err: err}
	}
	s.Branch = branch
	s.Dirty = g.checkClean() != nil

	base := o.Remote + "/" + o.Base
	if g.refExists(base) {
		commits, err := g.branchCommits("HEAD", base)
		if err != nil {
			s.Err = err
			return s
		}
		dirty, _ := splitBySigning(commits)
		s.Commits, s.Unsigned = len(commits), len(dirty)
	}

	ref := o.Remote + "/" + branch
	if !g.refExists(ref) {
		s.NoRemote = true
		return s
	}
	counts, err := g.capture("rev-list", "--left-right", "--count", ref+"...HEAD")
	if err != nil {
		s.Err = err
		return s
	}
	if f := strings.Fields(counts); len(f) == 2 {
		s.Behind, _ = strconv.Atoi(f[0])
		s.Ahead, _ = strconv.Atoi(f[1])
	}
	return s
}

// boxProbe is one remote line, printing key=value. The dirty count is boxDirty, shared with the
// pre-flight in signing. flock takes the lock only to drop it, which is how to ask whether an
// agent holds it without waiting for one.
const boxProbe = `cd %s 2>/dev/null || { echo checkout=missing; exit 0; }
echo "branch=$(git symbolic-ref --quiet --short HEAD || echo '(detached)')"
echo "head=$(git rev-parse --short HEAD 2>/dev/null)"
` + boxDirty + `
if command -v flock >/dev/null 2>&1 && [ -e %s ]; then
  if flock -n %s true 2>/dev/null; then echo agent=no; else echo agent=yes; fi
else echo agent=no; fi`

func boxStatus(o StatusOpts) boxState {
	var s boxState
	if o.Cfg.Host == "" {
		s.Err = fmt.Errorf("no host configured")
		return s
	}

	dir := cmp.Or(o.RepoDir, checkoutDir(o.Target))
	lock := remoteQuote(agentLock(o.Cfg.RemoteDir, o.Target.Repo))
	probe := fmt.Sprintf(boxProbe, remoteQuote(dir), lock, lock)
	out, err := exec.Command(sshBin(), o.Cfg.Host, probe).Output()
	if err != nil {
		s.Err = fmt.Errorf("ssh %s: %w", o.Cfg.Host, err)
		return s
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "checkout":
			s.Missing = v == "missing"
		case "branch":
			s.Branch = v
		case "head":
			s.Head = v
		case "dirty":
			s.Dirty, _ = strconv.Atoi(v)
		case "agent":
			s.Agent = v == "yes"
		}
	}
	if !o.HasPR {
		return s // no PR, so no pr-<n> directory to count
	}
	countBoxFiles(o, &s)
	return s
}

// countBoxFiles reads the pulled files back the way every other command does, with the real
// parsers rather than a grep over ssh: a second implementation of "is this reply pending" is a
// second answer to it. One trip fetches both, since ci/ is a subdirectory of the PR's own.
func countBoxFiles(o StatusOpts, s *boxState) {
	dir, err := fetchDir(o.Cfg.Host, o.Target.RemotePath(o.Cfg.RemoteDir))
	if err != nil {
		s.FileErr = err
		return
	}
	defer os.RemoveAll(dir)

	files, _, err := loadThreadFiles(dir)
	if err != nil {
		s.FileErr = err
		return
	}
	s.Threads, s.files = len(files), files
	for _, f := range files {
		switch {
		case f.thread.Sent():
			s.Sent++
		case f.thread.Reply != "":
			s.Drafted++
		}
	}

	checks, err := loadCIFiles(filepath.Join(dir, "ci"))
	if err != nil {
		s.FileErr = err
		return
	}
	for _, c := range checks {
		if c.Meta.Bucket != bucketFail {
			continue
		}
		s.CIFailing++
		if c.Fixed() || c.Notes != "" {
			s.CINoted++
		}
	}
}

// lineage answers the question status exists for: does the box's branch carry unsigned copies
// of commits already on the remote. It needs the commit objects, so the box's branch is fetched
// into FETCH_HEAD, which is the one ref a fetch writes and is not one anything here is sitting
// on. A box that cannot be reached or a branch nothing has is reported, not guessed at.
func lineage(g gitCmd, o StatusOpts, s *boxState) {
	if o.Box == "" || s.Branch == "" || s.Missing || s.Err != nil {
		return
	}
	base := o.Remote + "/" + o.Base
	if !g.refExists(base) {
		s.DupErr = fmt.Errorf("no %s here to measure the box's branch against", base)
		return
	}
	if err := g.run("fetch", "--quiet", o.Box, s.Branch); err != nil {
		s.DupErr = err
		return
	}
	commits, err := g.branchCommits("FETCH_HEAD", base)
	if err != nil {
		s.DupErr = err
		return
	}
	dirty, _ := splitBySigning(commits)
	s.Commits, s.Unsigned = len(commits), len(dirty)
	if s.Dups, err = duplicatedOnRemote(g, dirty, o.Remote+"/"+s.Branch, base); err != nil {
		s.DupErr = err
	}
}

func hubStatus(o StatusOpts) hubState {
	var s hubState
	if !o.HasPR {
		return s
	}

	var wg sync.WaitGroup
	var threadErr, checkErr, verifyErr error
	wg.Add(3)
	go func() {
		defer wg.Done()
		t := o.Target // Fetch fills the target in, and this copy is the one that gets written to
		threads, _, err := Fetch(&t, func(string) {})
		if threadErr = err; err != nil {
			return
		}
		for _, th := range threads {
			if !th.Meta.Resolved {
				s.unresolved = append(s.unresolved, th)
			}
		}
		s.Threads = len(s.unresolved)
	}()
	go func() {
		defer wg.Done()
		checks, err := FetchChecks(o.Target)
		if checkErr = err; err != nil {
			return
		}
		for _, c := range checks {
			if c.Failed() {
				s.Failing++
			}
		}
	}()
	go func() {
		defer wg.Done()
		bad, err := UnverifiedCommits(o.Target)
		if verifyErr = err; err != nil {
			return
		}
		s.Unverified = len(bad)
	}()
	wg.Wait()

	s.Err = cmp.Or(threadErr, checkErr, verifyErr)
	return s
}

// newOnGitHub counts the unresolved threads the box has not seen. A thread with no file there
// is new; a thread whose file would be regenerated differently has a comment on it since the
// pull. The comparison renders through the same merge pull does, so what counts as changed here
// is exactly what a re-pull would rewrite, and nothing else.
func newOnGitHub(o StatusOpts, rep statusReport) int {
	if !o.HasPR || rep.hub.Err != nil || rep.box.FileErr != nil || rep.box.Err != nil {
		return 0
	}
	onBox := make(map[int64]threadFile, len(rep.box.files))
	for _, f := range rep.box.files {
		onBox[f.thread.Meta.CommentID] = f
	}
	n := 0
	for _, th := range rep.hub.unresolved {
		f, ok := onBox[th.Meta.CommentID]
		if !ok {
			n++
			continue
		}
		th.Merge(f.thread)
		body, err := th.Render()
		if err != nil || body != f.raw {
			n++
		}
	}
	return n
}

func renderStatus(o StatusOpts, rep statusReport) {
	w := o.Out
	if o.HasPR {
		fmt.Fprintf(w, "%s#%d %s\n%s\n\n", o.Target.Slug(), o.Target.Number, o.Target.Title, o.Target.URL)
	} else {
		fmt.Fprintf(w, "%s: no open PR for %s\n\n", o.Target.Slug(), cmp.Or(o.Target.Branch, "this branch"))
	}

	m := rep.mac
	fmt.Fprintf(w, "mac     %s\n", m.Branch)
	if m.Err != nil {
		fmt.Fprintf(w, "        error: %v\n", m.Err)
	} else {
		fmt.Fprintf(w, "        %s, %d over %s/%s (%d unsigned), %s\n",
			cleanly(m.Dirty), m.Commits, o.Remote, o.Base, m.Unsigned, aheadBehind(o, m))
	}

	b := rep.box
	switch {
	case b.Err != nil:
		fmt.Fprintf(w, "box     error: %v\n", b.Err)
	case b.Missing:
		fmt.Fprintf(w, "box     no checkout at %s:%s\n", o.Cfg.Host, cmp.Or(o.RepoDir, checkoutDir(o.Target)))
	default:
		fmt.Fprintf(w, "box     %s %s\n", b.Branch, b.Head)
		fmt.Fprintf(w, "        %d uncommitted, %s\n", b.Dirty, agently(b.Agent))
		if b.DupErr != nil {
			fmt.Fprintf(w, "        could not compare its branch to %s: %v\n", o.Remote, b.DupErr)
		} else {
			fmt.Fprintf(w, "        %d over %s/%s (%d unsigned)\n", b.Commits, o.Remote, o.Base, b.Unsigned)
		}
		for _, d := range b.Dups {
			fmt.Fprintf(w, "        %s is an unsigned copy of %s on %s: %q\n",
				short(d.SHA), short(d.Twin), d.On, d.Subject)
		}
		if o.HasPR {
			if b.FileErr != nil {
				fmt.Fprintf(w, "        could not read the pulled files: %v\n", b.FileErr)
			} else {
				fmt.Fprintf(w, "        threads: %d (%d drafted, %d sent), ci: %d failing (%d worked on)\n",
					b.Threads, b.Drafted, b.Sent, b.CIFailing, b.CINoted)
			}
		}
	}

	h := rep.hub
	switch {
	case !o.HasPR:
	case h.Err != nil:
		fmt.Fprintf(w, "github  error: %v\n", h.Err)
	default:
		fmt.Fprintf(w, "github  %d unresolved thread(s), %d of them not on the box\n", h.Threads, h.New)
		fmt.Fprintf(w, "        %d check(s) failing, %s\n", h.Failing, verifiedly(h.Unverified))
	}

	fmt.Fprintf(w, "\nnext: %s\n", nextStep(o, rep))
}

func cleanly(dirty bool) string {
	if dirty {
		return "uncommitted changes"
	}
	return "clean"
}

func agently(running bool) string {
	if running {
		return "an agent is working here"
	}
	return "no agent running"
}

func verifiedly(unverified int) string {
	if unverified == 0 {
		return "every commit verified"
	}
	return fmt.Sprintf("%d commit(s) GitHub will not call verified", unverified)
}

func aheadBehind(o StatusOpts, m macState) string {
	if m.NoRemote {
		return fmt.Sprintf("no %s/%s yet", o.Remote, m.Branch)
	}
	return fmt.Sprintf("%d ahead / %d behind %s/%s", m.Ahead, m.Behind, o.Remote, m.Branch)
}

// nextStep is the line the command exists for. The order is what makes it one line rather than
// a list: the two states that stop everything come first, then the work that is ready to
// publish, then the work that has to be brought over. Anything below the first true case is
// still true, and is what the next run will say.
func nextStep(o StatusOpts, rep statusReport) string {
	pr := ""
	if o.HasPR {
		pr = " " + strconv.Itoa(o.Target.Number)
	}
	b, m, h := rep.box, rep.mac, rep.hub

	switch {
	case len(b.Dups) > 0:
		return fmt.Sprintf("sand sign --push, which will refuse and print the recovery: "+
			"%d commit(s) on the box are unsigned copies of commits already pushed", len(b.Dups))
	case b.Agent:
		return "nothing: an agent is working on the box, and a second one in that tree is not a race to lose"
	case b.Dirty > 0:
		return fmt.Sprintf("nothing here: the box has %d uncommitted file(s), and signing cannot "+
			"hand the branch back until they are committed there", b.Dirty)
	case b.Drafted > 0 || b.CINoted > 0 || b.Unsigned > 0 || m.Unsigned > 0 || m.Ahead > 0:
		return "sand up" + pr
	case h.New > 0:
		return "sand comments pull" + pr
	case h.Failing > 0:
		return "sand ci pull" + pr
	}
	return "nothing"
}
