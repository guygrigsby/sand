package sand

// Starting the agent is the point of pull: the files are useless until something on the box
// reads them, and making a human ssh in to type the same prompt every time is the part of
// the loop that gets skipped. So pull holds one ssh open, runs the agent in the checkout on
// the box, and streams what it does back here. The tunnel is the status channel.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
)

// agentCommand is what the box runs, minus the prompt, which is appended as one argument:
// the harness's headless invocation from the one harness table, plus the model when the
// config names one. Empty model means whatever the harness defaults to, which is the right
// default for a tool that does not track anyone's model catalogue.
//
// `sand config set harness pi` / `sand config set model <id>` moves it; `pull --agent '<cmd>'`
// bypasses the table entirely for a one-off. Anything the agent prints that is not a
// stream-json event is passed through as is, so a non-Claude harness still reports.
func agentCommand(cfg Config) ([]string, error) {
	h, err := findHarness(cfg.Harness)
	if err != nil {
		return nil, err
	}
	argv := append([]string(nil), h.run...)
	if cfg.Model == "" {
		return argv, nil
	}
	if h.modelFlag == "" {
		return nil, fmt.Errorf("%s takes no model flag; clear it with `sand config set model \"\"`", h.Name)
	}
	return append(argv, h.modelFlag, cfg.Model), nil
}

// AgentRun is one agent invocation on the box.
type AgentRun struct {
	Host    string
	Dir     string   // working dir on the box: the repo checkout, not the pulled files
	Lock    string   // lockfile on the box, held for the whole run
	Command []string // agent argv, prompt appended
	Prompt  string
	Out     io.Writer
}

// Exit codes the remote line uses to say what went wrong, since ssh gives back only a status.
//
// Deliberately outside 64-78. These were sysexits values (66, 69, 75) picked for reading well,
// and flock uses that same range for its own failures: it exits 69, EX_UNAVAILABLE, when it
// cannot exec the command it was given. So a harness missing from the box's PATH came back as
// this tool's code for "no flock", and the operator was told the checkout could not be locked
// when the lock was fine and the agent binary was simply absent. 111 and up collides with
// nothing standard: not sysexits, not the shell's 126/127, not an agent's own 0/1/2.
const (
	exitLocked     = 111
	exitNoCheckout = 112
	exitNoFlock    = 113
	exitNoHarness  = 114
)

// exitFlockExec is flock's, not this tool's: EX_UNAVAILABLE, what it exits when execvp on the
// command fails. Named so the switch below can say what it means instead of printing 69.
const exitFlockExec = 69

// remotePath is prepended to PATH on the box before anything is looked up. `ssh box '<cmd>'`
// runs a non-interactive, non-login shell, which on a zsh box means .zshrc is never sourced and
// PATH is whatever the shell compiles in: /bin:/usr/bin and friends. Every agent CLI installs
// itself under $HOME, so without this the harness is invisible over ssh however well it works
// in a terminal there. A guess at the standard three, not a fix for every layout; the pre-flight
// below says so plainly when it is not enough.
const remotePath = `PATH="$HOME/.local/bin:$HOME/bin:$HOME/go/bin:$PATH"`

// RunAgent runs the agent on the box with the pulled threads to work on, streaming its
// output. It blocks until the agent exits.
//
// Under flock, because two agents in one checkout is corruption, not a race to lose: they edit
// the same working tree, commit over each other and answer the same thread file twice. And it
// is easy to cause. `pull` is one command per PR, two PRs share a repo, and the operator who
// thinks a run has stalled re-runs it, which is exactly what every message in this tool tells
// them to do.
func RunAgent(r AgentRun) error {
	if len(r.Command) == 0 {
		return fmt.Errorf("no agent command: set the harness with `sand config set harness <name>`")
	}
	if r.Lock == "" {
		return fmt.Errorf("no lockfile named for the agent run") // caller bug, not an operator's
	}

	quoted := make([]string, 0, len(r.Command)+1)
	for _, f := range append(append([]string(nil), r.Command...), r.Prompt) {
		quoted = append(quoted, shellQuote(f))
	}
	// One remote line. Fail loudly if the checkout is not where we think, since an agent
	// started in the wrong directory edits the wrong tree, and fail closed if there is no
	// flock: running unlocked is the thing being prevented. The harness is checked here too,
	// before the lock, because flock's own answer to a binary it cannot exec is an exit code
	// this side cannot tell apart from anything else and a message that mentions flock.
	lock := remoteQuote(r.Lock)
	remote := fmt.Sprintf("%s; "+
		"command -v flock >/dev/null 2>&1 || "+
		"{ echo \"sand: no flock on $(hostname); install util-linux\" >&2; exit %d; }; "+
		"command -v %s >/dev/null 2>&1 || "+
		"{ echo \"sand: %s is not on PATH over ssh on $(hostname) (PATH=$PATH)\" >&2; exit %d; }; "+
		"mkdir -p %s 2>/dev/null; "+
		"cd %s 2>/dev/null || { echo \"sand: no checkout at %s on $(hostname)\" >&2; exit %d; }; "+
		"exec flock -n -E %d %s %s",
		remotePath,
		exitNoFlock,
		quoted[0], r.Command[0], exitNoHarness,
		remoteQuote(path.Dir(r.Lock)),
		remoteQuote(r.Dir), r.Dir, exitNoCheckout,
		exitLocked, lock, strings.Join(quoted, " "))

	cmd := exec.Command(sshBin(), r.Host, remote)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = r.Out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ssh %s: %w", r.Host, err)
	}
	streamAgent(stdout, r.Out)
	if err := cmd.Wait(); err != nil {
		switch exitCode(err) {
		case exitLocked:
			return fmt.Errorf("an agent is already working in %s:%s (lock %s); "+
				"let it finish, or re-run with --no-agent and talk to that one",
				r.Host, r.Dir, r.Lock)
		case exitNoFlock:
			return fmt.Errorf("cannot lock %s:%s, so no agent was started", r.Host, r.Dir)
		case exitNoHarness:
			return fmt.Errorf("%s is not on PATH over ssh on %s, so no agent was started: "+
				"an ssh command gets none of an interactive shell's PATH, so put it in "+
				"~/.zshenv or ~/.profile there, or link it into ~/.local/bin, or point "+
				"`sand config set harness <name>` at one that is installed",
				r.Command[0], r.Host)
		case exitFlockExec:
			// The pre-flight found it, so this is the binary itself: no execute bit, a bad
			// interpreter line, the wrong architecture. flock's own message is on stderr.
			return fmt.Errorf("%s is on %s but could not be started (see the flock message above)",
				r.Command[0], r.Host)
		}
		return fmt.Errorf("agent on %s: %w", r.Host, err)
	}
	return nil
}

// exitCode is the remote command's status, or -1 when there is none: ssh itself failing, or
// the agent dying on a signal.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// agentLock is the lockfile for a checkout, next to the pulled threads because that directory
// exists on the box already and is the tool's own. Keyed by repo rather than by the checkout
// path: `--repo-dir` can point two runs of one repo at different trees, and treating those as
// one is over-locking, which costs a wait, where under-locking costs a corrupted tree.
func agentLock(remoteDir, repo string) string {
	return path.Join(remoteDir, "locks", segment(repo)+".lock")
}

// streamAgent turns Claude Code's stream-json into readable progress, and passes anything
// else through untouched so a different agent command still reports.
func streamAgent(in io.Reader, out io.Writer) {
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // tool results get big
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var ev agentEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
			fmt.Fprintln(out, line)
			continue
		}
		ev.print(out)
	}
	if err := s.Err(); err != nil {
		fmt.Fprintf(out, "  (lost the agent's output: %v)\n", err)
	}
}

// agentEvent is the part of a stream-json event worth showing. Unknown types print
// nothing: the stream carries init, usage and tool-result noise nobody is waiting on.
type agentEvent struct {
	Type    string `json:"type"`
	Result  string `json:"result"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func (e agentEvent) print(out io.Writer) {
	switch e.Type {
	case "assistant":
		for _, c := range e.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					fmt.Fprintf(out, "  %s\n", t)
				}
			case "tool_use":
				fmt.Fprintf(out, "  · %s %s\n", c.Name, toolHint(c.Input))
			}
		}
	case "result":
		if t := strings.TrimSpace(e.Result); t != "" {
			fmt.Fprintf(out, "\n%s\n", t)
		}
	}
}

// toolHint is the one field of a tool call that says which thing it touched.
func toolHint(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "command", "path", "pattern", "prompt"} {
		if v, ok := m[k].(string); ok && v != "" {
			return firstLine(v, 100)
		}
	}
	return ""
}

func firstLine(s string, max int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// agentPrompt is what the agent is told. Short on purpose: the skill on the box carries the
// rules, and repeating them here is how the two drift apart. It names the directory and the
// PR because those are the parts the skill cannot know.
func agentPrompt(t Target, prDir string, threads int) string {
	return fmt.Sprintf(
		"Use the sand skill. %d unresolved review thread(s) for %s#%d (%q) are pulled to %s "+
			"on this box; you are in the repo checkout they are about. Work through every thread "+
			"whose status is pending: fix the code here, prove bugs with a failing test first, run "+
			"make check, commit, then write your reply under `## reply` and the commit's short hash "+
			"in `commit:` in that thread's file. Do not run sand or gh, and do not push: the Mac "+
			"signs and posts. Finish by listing which threads you answered and which you left, "+
			"with the reason.",
		threads, t.Slug(), t.Number, t.Title, prDir)
}

func prPrompt(t Target, issueDir, remote, base string) string {
	return fmt.Sprintf(
		"Use the sand and voice skills. Draft the pull request for issue %s#%d from branch %s in this "+
			"checkout. Confirm that branch is checked out and stop without writing a draft if it is not. "+
			"For all writing, use the voice skill's pr-description register: load "+
			"~/.claude/voice/rules.md and ~/.claude/voice/voice.md, then its matching corpus samples. "+
			"If the skill or either file is missing, stop without writing a draft. Read the issue at "+
			"%s/issue.md, inspect the complete branch diff and commit history against %s/%s, then write "+
			"a one-line title to %s/pr-title.txt and the body as proper GitHub Markdown to "+
			"%s/pr-description.md. Write only the title and body to those files, without the voice skill's "+
			"register label or sample-source report. Preserve useful code fences. Explain what changed, "+
			"include material risks and end with Fixes: #%d. Do not edit code, commit, run sand or gh, or push.",
		t.Slug(), t.Number, t.Branch, issueDir, remote, base, issueDir, issueDir, t.Number)
}

// ciPrompt is agentPrompt for failing checks: the same shape, and short for the same reason.
// It says not to push because that is the one instruction an agent given a red build will
// otherwise act on, and the box has no key to push with.
func ciPrompt(t Target, ciDir string, failing int) string {
	return fmt.Sprintf(
		"Use the sand skill. %d failing CI check(s) for %s#%d (%q) are pulled to %s on this "+
			"box; you are in the repo checkout they are about. Work through every check whose "+
			"status is pending: read its log, reproduce the failure here, fix it, run make "+
			"check, commit, then write what you changed under `## notes` and the commit's short "+
			"hash in `commit:` in that check's file, and set `status: fixed`. Do not run sand or "+
			"gh, and do not push: the Mac signs and pushes, and CI runs again on what it pushed. "+
			"Finish by listing which checks you fixed and which you left, with the reason.",
		failing, t.Slug(), t.Number, t.Title, ciDir)
}

// checkoutDir is where the agent runs: the box keeps its checkouts in ~/projects/<repo>.
func checkoutDir(t Target) string { return path.Join("~/projects", segment(t.Repo)) }
