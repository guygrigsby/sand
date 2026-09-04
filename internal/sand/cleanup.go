package sand

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var recoveryBranch = regexp.MustCompile(`-before-(?:signing|import)-[0-9]{14}(?:-[0-9]+)?$`)

type cleanupOpts struct {
	Yes bool
	In  io.Reader
	Out io.Writer
}

func cleanup(o cleanupOpts) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	g := gitCmd{out: o.Out}
	refs, err := g.capture("for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return err
	}
	var branches []string
	for _, branch := range strings.Split(refs, "\n") {
		if recoveryBranch.MatchString(branch) {
			branches = append(branches, branch)
			fmt.Fprintln(o.Out, branch)
		}
	}
	if len(branches) == 0 {
		fmt.Fprintln(o.Out, "No recovery branches found.")
		return nil
	}
	current, _ := g.capture("branch", "--show-current")
	if recoveryBranch.MatchString(current) {
		return fmt.Errorf("%s is checked out; switch away from it before cleanup", current)
	}
	if !o.Yes && !confirm(bufio.NewReader(o.In), o.Out, fmt.Sprintf("Delete these %d recovery branches?", len(branches))) {
		fmt.Fprintln(o.Out, "Kept recovery branches.")
		return nil
	}
	if err := g.run(append([]string{"branch", "-D", "--"}, branches...)...); err != nil {
		return fmt.Errorf("deleting recovery branches: %w", err)
	}
	fmt.Fprintf(o.Out, "Deleted %d recovery branches.\n", len(branches))
	return nil
}
