package sand

import (
	"fmt"
	"strings"
)

// RenderIndex is the per-PR index.md: what the PR is, the review summary bodies as
// read-only context, and a table pointing at the thread files. Regenerated on every
// pull, so nothing an agent writes belongs here.
func RenderIndex(t Target, threads []Thread, reviews []Review, pulledAt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s#%d — %s\n\n", t.Slug(), t.Number, t.Title)
	fmt.Fprintf(&b, "- url: %s\n- branch: `%s`\n- author: @%s\n- pulled: %s\n",
		t.URL, t.Branch, t.Author, pulledAt)

	pending, sent := 0, 0
	for _, th := range threads {
		if th.Sent() {
			sent++
		} else {
			pending++
		}
	}
	fmt.Fprintf(&b, "- threads: %d (%d pending, %d replied)\n", len(threads), pending, sent)

	b.WriteString("\nReply by editing the `## reply` section of a thread file below, and filling in\n" +
		"its `commit:` field. Then, on the Mac: `sand comments push`.\n")

	if len(reviews) > 0 {
		b.WriteString("\n## review summaries\n\nRead-only: GitHub has no threaded reply for a review body.\n")
		for _, r := range reviews {
			fmt.Fprintf(&b, "\n### @%s %s %s\n\n%s\n\n%s\n",
				r.Author, r.State, shortDate(r.SubmittedAt), quote(r.Body), r.URL)
		}
	}

	b.WriteString("\n## threads\n\n")
	if len(threads) == 0 {
		b.WriteString("None.\n")
		return b.String()
	}
	b.WriteString("| file | status | from | file:line |\n|---|---|---|---|\n")
	for _, th := range threads {
		flags := th.Meta.Status
		if th.Meta.Outdated {
			flags += ", outdated"
		}
		if th.Meta.Resolved {
			flags += ", resolved"
		}
		fmt.Fprintf(&b, "| [%s](%s) | %s | @%s | %s |\n",
			th.Filename(), th.Filename(), flags, th.Meta.Author, th.Location())
	}
	return b.String()
}
