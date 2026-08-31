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
// 66 and 75 are EX_NOINPUT and EX_TEMPFAIL, which is close enough to what they mean here.
const (
	exitNoCheckout = 66
	exitLocked     = 75
	exitNoFlock    = 69
)

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
	// flock: running unlocked is the thing being prevented.
	lock := remoteQuote(r.Lock)
	remote := fmt.Sprintf("command -v flock >/dev/null 2>&1 || "+
		"{ echo \"sand: no flock on $(hostname); install util-linux\" >&2; exit %d; }; "+
		"mkdir -p %s 2>/dev/null; "+
		"cd %s 2>/dev/null || { echo \"sand: no checkout at %s on $(hostname)\" >&2; exit %d; }; "+
		"exec flock -n -E %d %s %s",
		exitNoFlock,
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

// checkoutDir is where the agent runs: the box keeps its checkouts in ~/projects/<repo>.
func checkoutDir(t Target) string { return path.Join("~/projects", segment(t.Repo)) }
