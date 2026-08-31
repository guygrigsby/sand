package sand

// The markdown files under ~/.sand are the whole contract between the two machines:
// `pull` writes them, an agent on the box edits the reply slot, `push` reads them back.
// Everything above the "## reply" heading is regenerated on every pull; the reply slot,
// `commit` and `status` belong to the agent and to push, and are preserved across pulls.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const replyHeading = "## reply"

// Meta is the YAML front matter of a thread file.
type Meta struct {
	ThreadID  string `yaml:"thread_id"`
	CommentID int64  `yaml:"comment_id"`
	URL       string `yaml:"url"`
	Path      string `yaml:"path"`
	Line      int    `yaml:"line"`
	Author    string `yaml:"author"`
	Resolved  bool   `yaml:"resolved"`
	Outdated  bool   `yaml:"outdated"`
	Status    string `yaml:"status"`
	Commit    string `yaml:"commit"`
	RepliedAt string `yaml:"replied_at,omitempty"`
	ReplyURL  string `yaml:"reply_url,omitempty"`
}

const (
	StatusPending = "pending"
	StatusSent    = "sent"
)

// Comment is one message in a review thread.
type Comment struct {
	Author    string
	CreatedAt string
	Body      string
}

// Thread is one review thread: its front matter, the conversation, the diff it hangs
// off, and the reply an agent has drafted (empty until then).
type Thread struct {
	Meta     Meta
	Comments []Comment
	DiffHunk string
	Reply    string
}

// Filename is the thread's name on disk, keyed by the comment push must reply to.
func (t Thread) Filename() string {
	return fmt.Sprintf("c-%d.md", t.Meta.CommentID)
}

// Location is the "file:line" the thread hangs off, for one-line summaries.
func (t Thread) Location() string {
	if t.Meta.Line == 0 {
		return t.Meta.Path
	}
	return t.Meta.Path + ":" + strconv.Itoa(t.Meta.Line)
}

func (t Thread) hint() string {
	return fmt.Sprintf(`<!-- Write the reply below this comment, then fill in "commit:" above with the
     short hash of the commit that fixes it. "sand comments push" posts everything
     under this heading as a threaded reply to comment %d and sets status: sent.
     Leave it empty to say nothing. -->`, t.Meta.CommentID)
}

// Render produces the markdown file for a thread.
func (t Thread) Render() (string, error) {
	fm, err := yaml.Marshal(t.Meta)
	if err != nil {
		return "", fmt.Errorf("front matter for comment %d: %w", t.Meta.CommentID, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\n%s---\n\n# %s\n\n", fm, t.Location())

	b.WriteString("## thread\n")
	for _, c := range t.Comments {
		fmt.Fprintf(&b, "\n### @%s %s\n\n%s\n", c.Author, shortDate(c.CreatedAt), quote(c.Body))
	}

	if t.DiffHunk != "" {
		hunk := strings.TrimRight(t.DiffHunk, "\n")
		f := fence(hunk)
		fmt.Fprintf(&b, "\n## diff\n\n%sdiff\n%s\n%s\n", f, hunk, f)
	}

	fmt.Fprintf(&b, "\n%s\n\n%s\n", replyHeading, t.hint())
	if t.Reply != "" {
		fmt.Fprintf(&b, "\n%s\n", t.Reply)
	}
	return b.String(), nil
}

var (
	frontMatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
	hintComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	backticks   = regexp.MustCompile("`+")
)

// fence is a backtick run long enough to hold s. A review of a markdown file (this repo's own
// CLAUDE.md, say) puts fenced code inside the diff hunk, and a fixed three-backtick fence
// closes on the first of those: the rest of the hunk then renders as prose, and the reader
// silently sees a different diff from the one the reviewer commented on.
func fence(s string) string {
	longest := 0
	for _, run := range backticks.FindAllString(s, -1) {
		longest = max(longest, len(run))
	}
	return strings.Repeat("`", max(3, longest+1))
}

// Parse reads back the parts of a thread file that the box owns: the front matter and
// the drafted reply. Comments and DiffHunk are not recovered — pull regenerates those
// from GitHub, so re-reading them would only be a chance to disagree with upstream.
func Parse(s string) (Thread, error) {
	m := frontMatter.FindStringSubmatch(s)
	if m == nil {
		return Thread{}, fmt.Errorf("no YAML front matter (file must start with ---)")
	}

	var t Thread
	if err := yaml.Unmarshal([]byte(m[1]), &t.Meta); err != nil {
		return Thread{}, fmt.Errorf("front matter: %w", err)
	}
	if t.Meta.CommentID == 0 {
		return Thread{}, fmt.Errorf("front matter has no comment_id")
	}

	// Everything after the first reply heading is the reply, so a reply that quotes the
	// heading keeps its own text. Nothing above the slot can produce a bare "## reply"
	// line to match first: comment bodies are blockquoted, diff lines are prefixed.
	body := s[len(m[0]):]
	if i := strings.Index(body, "\n"+replyHeading); i >= 0 {
		body = body[i+len("\n"+replyHeading):]
	} else if strings.HasPrefix(body, replyHeading) {
		body = body[len(replyHeading):]
	} else {
		return t, nil // no reply slot at all: nothing drafted
	}
	t.Reply = strings.TrimSpace(hintComment.ReplaceAllString(body, ""))
	return t, nil
}

// ReplaceFrontMatter rewrites only the YAML block of an existing file, leaving the body
// byte-for-byte alone. push uses it to record status without re-rendering (and so
// without inventing) the conversation the file already holds.
func ReplaceFrontMatter(orig string, m Meta) (string, error) {
	loc := frontMatter.FindStringIndex(orig)
	if loc == nil {
		return "", fmt.Errorf("no YAML front matter to replace")
	}
	fm, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("---\n%s---\n", fm) + orig[loc[1]:], nil
}

// Merge carries the box's edits onto a freshly fetched thread: the drafted reply, the
// commit the agent recorded, and whether push already sent it. Without this a second
// pull while an agent is mid-work would throw its drafts away.
func (t *Thread) Merge(old Thread) {
	t.Reply = old.Reply
	t.Meta.Commit = old.Meta.Commit
	t.Meta.RepliedAt = old.Meta.RepliedAt
	t.Meta.ReplyURL = old.Meta.ReplyURL
	if old.Meta.Status == StatusSent {
		t.Meta.Status = StatusSent
	}
}

// Sent reports whether push has already posted this reply.
func (t Thread) Sent() bool { return t.Meta.Status == StatusSent }

func quote(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// shortDate trims an ISO-8601 timestamp to the date. Anything unexpected passes through.
func shortDate(ts string) string {
	if len(ts) >= 10 && ts[4] == '-' && ts[7] == '-' {
		return ts[:10]
	}
	return ts
}
