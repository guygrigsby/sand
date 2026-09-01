package sand

// Failing CI is the review loop again with one end missing. The Mac can see what failed and the
// box is where the fix has to be written, so the failures travel as markdown the same way review
// threads do, into pr-<n>/ci/, and an agent on the box works them in the checkout.
//
// There is no push half. A review thread is a conversation and a reply belongs on it; a red check
// is not, and the answer to it is a commit. That commit leaves the box the way every other one
// does, through `sand up`, and CI running again on the pushed head is the evidence. So this file
// only ever reads from GitHub.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Check is one entry in the PR's check list, as `gh pr checks` reports it.
type Check struct {
	Name        string
	Workflow    string
	Bucket      string // gh's classifier: pass, fail, pending, skipping, cancel
	State       string // the raw state behind it: FAILURE, TIMED_OUT, ...
	Link        string
	Description string
	CompletedAt string

	RunID string // the Actions run id out of Link; empty for anything that is not one
}

// The buckets worth naming. gh also answers "skipping" and "cancel", which are neither a
// failure to fix nor a pass to report, so they only appear under --all.
const (
	bucketFail    = "fail"
	bucketPending = "pending"
)

// Failed reports whether this check is one to hand an agent.
func (c Check) Failed() bool { return c.Bucket == bucketFail }

// checkFields is every field `gh pr checks --json` offers. All of them, because the cost is the
// same and a field nobody reads is cheaper than a second round trip when someone wants one.
const checkFields = "bucket,completedAt,description,event,link,name,startedAt,state,workflow"

// FetchChecks lists the PR's checks, failures included, in the order GitHub reports them.
//
// `gh pr checks` exits non-zero on exactly the case this command exists for: 1 when a check has
// failed, 8 while any are still pending. So the exit status is not the answer here, stdout is,
// and the error is only reported when there is no JSON to read. ghJSON cannot be used for that
// reason.
func FetchChecks(t Target) ([]Check, error) {
	var got []struct {
		Name        string `json:"name"`
		Workflow    string `json:"workflow"`
		Bucket      string `json:"bucket"`
		State       string `json:"state"`
		Link        string `json:"link"`
		Description string `json:"description"`
		CompletedAt string `json:"completedAt"`
	}
	out, runErr := gh("pr", "checks", strconv.Itoa(t.Number), "--repo", t.Slug(), "--json", checkFields)
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		if runErr != nil {
			// No JSON and a failure: gh's own message is the useful one ("no checks
			// reported on the ... branch", not authenticated, no such PR).
			return nil, runErr
		}
		return nil, fmt.Errorf("decoding gh pr checks output: %w", err)
	}

	checks := make([]Check, 0, len(got))
	for _, c := range got {
		checks = append(checks, Check{
			Name: c.Name, Workflow: c.Workflow, Bucket: c.Bucket, State: c.State,
			Link: c.Link, Description: c.Description, CompletedAt: c.CompletedAt,
			RunID: actionsRun(c.Link),
		})
	}
	return checks, nil
}

var actionsRunRe = regexp.MustCompile(`/actions/runs/(\d+)`)

// actionsRun is the workflow run id in a check's link, or empty for a check that is not a
// GitHub Actions run. Buildkite, CircleCI and anything else posting a commit status gives a
// link to its own service, which this machine has no client for: those checks travel as their
// name, state and link, and the file says so rather than pretending to a log.
func actionsRun(link string) string {
	if m := actionsRunRe.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

// Log size caps. A failed job's log runs to megabytes of setup noise, and the part that says
// why it failed is at the end; the whole thing on the box is a file nothing can read and a tar
// nobody wants. The link in the front matter is there for the rest.
const (
	defaultLogLines = 300
	maxLogBytes     = 128 << 10
)

// FailedLog is the log of the failed steps of an Actions run, tailed to maxLines, with the
// number of lines dropped. A run still in progress, or one gh cannot get logs for, is not an
// error worth stopping a pull over: the caller writes the file without a log and says why.
func FailedLog(t Target, runID string, maxLines int) (log string, dropped int, err error) {
	out, err := gh("run", "view", runID, "--repo", t.Slug(), "--log-failed")
	if strings.TrimSpace(out) == "" {
		if err == nil {
			err = fmt.Errorf("gh run view %s --log-failed printed nothing", runID)
		}
		return "", 0, err
	}
	log, dropped = tail(out, maxLines)
	return log, dropped, nil
}

// tail keeps the last n lines, and then the last maxLogBytes bytes of those: one line of
// base64 or a minified bundle in a stack trace is enough to blow a line-only cap.
func tail(s string, n int) (string, int) {
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	lines := strings.Split(s, "\n")
	dropped := 0
	if n > 0 && len(lines) > n {
		dropped = len(lines) - n
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxLogBytes {
		cut := len(out) - maxLogBytes
		// Start at a line boundary, so the first line shown is a whole one.
		if i := strings.IndexByte(out[cut:], '\n'); i >= 0 {
			cut += i + 1
		}
		dropped += strings.Count(out[:cut], "\n")
		out = out[cut:]
	}
	return out, dropped
}

// CIMeta is the front matter of a CI failure file. `status` and `commit` are the box's to
// write, like a thread's reply slot; everything else is regenerated by every pull.
type CIMeta struct {
	Check    string `yaml:"check"`
	Workflow string `yaml:"workflow,omitempty"`
	Bucket   string `yaml:"bucket"`
	State    string `yaml:"state,omitempty"`
	Link     string `yaml:"link"`
	RunID    string `yaml:"run_id,omitempty"`
	PulledAt string `yaml:"pulled_at"`
	Status   string `yaml:"status"`
	Commit   string `yaml:"commit"`
}

// StatusFixed is what an agent writes when it believes the check will pass now. Nothing
// verifies it here: the next CI run does, which is why there is no third state.
const StatusFixed = "fixed"

const notesHeading = "## notes"

// CIFailure is one check as it lives on the box: its front matter, the tail of its log, and a
// notes slot for the agent.
type CIFailure struct {
	Meta    CIMeta
	Log     string
	Dropped int    // lines the tail cut off the front of Log
	LogNote string // why there is no log, when there is none
	Notes   string
}

// Filename is keyed by the check's name rather than by a run id: the same check failing again
// on the next run has to land on the same file, or the notes from the last round have nothing
// to merge onto.
func (f CIFailure) Filename() string { return "ci-" + segment(f.Meta.Check) + ".md" }

// Fixed reports whether the box has claimed this one is dealt with.
func (f CIFailure) Fixed() bool { return f.Meta.Status == StatusFixed }

func (f CIFailure) hint() string {
	return `<!-- Write what you changed below this comment, and put the fixing commit's short
     hash in "commit:" above. Set "status: fixed" when you believe the check will
     pass now. Nothing here is posted to GitHub: the next CI run is the answer,
     and ` + "`sand up`" + ` on the Mac is what publishes the commit. -->`
}

// Render produces the markdown file for one failing check.
func (f CIFailure) Render() (string, error) {
	fm, err := yaml.Marshal(f.Meta)
	if err != nil {
		return "", fmt.Errorf("front matter for check %q: %w", f.Meta.Check, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\n%s---\n\n# CI: %s\n\n", fm, f.Meta.Check)
	fmt.Fprintf(&b, "- state: %s (%s)\n", cmp.Or(f.Meta.State, f.Meta.Bucket), f.Meta.Bucket)
	if f.Meta.Workflow != "" {
		fmt.Fprintf(&b, "- workflow: %s\n", f.Meta.Workflow)
	}
	fmt.Fprintf(&b, "- link: %s\n", f.Meta.Link)

	switch {
	case f.Log != "":
		b.WriteString("\n## log\n\n")
		if f.Dropped > 0 {
			fmt.Fprintf(&b, "Last lines of the failed steps; %d earlier line(s) cut. "+
				"The whole log is at the link above.\n\n", f.Dropped)
		} else {
			b.WriteString("The failed steps of the run.\n\n")
		}
		fence := fence(f.Log)
		fmt.Fprintf(&b, "%stext\n%s\n%s\n", fence, f.Log, fence)
	default:
		fmt.Fprintf(&b, "\n## log\n\n%s\n", cmp.Or(f.LogNote, "No log was fetched."))
	}

	fmt.Fprintf(&b, "\n%s\n\n%s\n", notesHeading, f.hint())
	if f.Notes != "" {
		fmt.Fprintf(&b, "\n%s\n", f.Notes)
	}
	return b.String(), nil
}

// ParseCIFailure reads back the parts of a CI file the box owns: the front matter and the
// notes. The log is not recovered, for the same reason a thread's conversation is not — pull
// refetches it, and re-reading it here would only be a chance to disagree with GitHub.
func ParseCIFailure(s string) (CIFailure, error) {
	fm, body, err := splitFrontMatter(s)
	if err != nil {
		return CIFailure{}, err
	}
	var f CIFailure
	if err := yaml.Unmarshal([]byte(fm), &f.Meta); err != nil {
		return CIFailure{}, fmt.Errorf("front matter: %w", err)
	}
	if f.Meta.Check == "" {
		return CIFailure{}, fmt.Errorf("front matter has no check name")
	}
	f.Notes = lastSectionAfter(body, notesHeading)
	return f, nil
}

// Merge carries the box's work onto a freshly fetched check: the notes, the commit and a
// status of fixed. Without it a re-pull while an agent is mid-work throws its notes away.
func (f *CIFailure) Merge(old CIFailure) {
	f.Notes = old.Notes
	f.Meta.Commit = old.Meta.Commit
	if old.Fixed() {
		f.Meta.Status = StatusFixed
	}
}

// RenderCIIndex is the ci/index.md: what the PR is, what failed, and what to do about it.
// Regenerated on every pull, so nothing an agent writes belongs here.
//
// files holds every check with a file here, green ones included: a check that has gone green
// keeps its file, because the transport adds and never deletes, and a file left saying `fail`
// after the check passed is worse than one saying so. others is the rest of the check list,
// context only, and empty unless --all asked for it.
func RenderCIIndex(t Target, files []CIFailure, others []Check, pulledAt string) string {
	var failing, green []CIFailure
	for _, f := range files {
		if f.Meta.Bucket == bucketFail {
			failing = append(failing, f)
		} else {
			green = append(green, f)
		}
	}
	fixed := 0
	for _, f := range failing {
		if f.Fixed() {
			fixed++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# CI for %s#%d — %s\n\n", t.Slug(), t.Number, t.Title)
	fmt.Fprintf(&b, "- url: %s\n- branch: `%s`\n- pulled: %s\n", t.URL, t.Branch, pulledAt)
	fmt.Fprintf(&b, "- failing: %d (%d still to fix, %d marked fixed)\n",
		len(failing), len(failing)-fixed, fixed)

	b.WriteString("\nFix the code in the repo checkout, reproduce the failure locally, run `make check`,\n" +
		"commit, then write what you did under `## notes` in that check's file and set\n" +
		"`status: fixed`. Nothing here is posted to GitHub: the fix travels as the commit, and\n" +
		"`sand up` on the Mac is what publishes it. CI running again is the answer.\n")

	b.WriteString("\n## failing\n\n")
	if len(failing) == 0 {
		b.WriteString("Nothing.\n")
	} else {
		b.WriteString("| file | check | state | status |\n|---|---|---|---|\n")
		for _, f := range failing {
			fmt.Fprintf(&b, "| [%s](%s) | %s | %s | %s |\n",
				f.Filename(), f.Filename(), f.Meta.Check,
				cmp.Or(f.Meta.State, f.Meta.Bucket), f.Meta.Status)
		}
	}

	if len(green) > 0 {
		b.WriteString("\n## not failing any more\n\nPulled while they were red. Nothing to do.\n\n")
		b.WriteString("| file | check | bucket |\n|---|---|---|\n")
		for _, f := range green {
			fmt.Fprintf(&b, "| [%s](%s) | %s | %s |\n",
				f.Filename(), f.Filename(), f.Meta.Check, cmp.Or(f.Meta.Bucket, "gone"))
		}
	}

	if len(others) > 0 {
		b.WriteString("\n## the rest\n\nContext only, no file and nothing to do.\n\n")
		b.WriteString("| check | bucket | state |\n|---|---|---|\n")
		for _, c := range others {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, c.Bucket, cmp.Or(c.State, "-"))
		}
	}
	return b.String()
}
