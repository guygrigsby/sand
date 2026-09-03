# Config: the keys and `sand config`


`host` is the one setting with no default and the one thing a new Mac has to be told: it names
one machine on one tailnet, so a compiled-in alias is either someone else's box or a name that
resolves nowhere. `remote_dir` defaults to `~/.sand`, `harness` to `claude`, `branch_prefix` to
`$USER` and `model` to nothing at all. Set them in `~/.config/sand/config.yaml`, or with `--host` / `--remote-dir`, or
`SAND_<KEY>` in the environment, in that order of precedence. `harness` is which agent CLI
`pull` starts (from the one harness table) and `model` is what to pass it, in that harness's own
spelling. An empty model is a real answer: the harness picks, which is the only answer that does
not go stale every time a model ships. Neither has a flag, because only `pull` reads them and it
already has `--agent` for a one-off.

`sand init` is what a person runs: it asks for every key, then checks the rest of the setup and
names what is missing (see below). `sand config` prints the file, `sand config init` creates or
updates it and nothing else, `sand config set <key> <value>...` sets any key, and `sand config
get <key>` prints one effective value and nothing else, for scripts and the Makefile (`get`
applies env and the defaults; `config` only shows what the file says).

## `sand init`, the one command a new Mac needs

Setup was seven things in five places, and the seventh was always the one nobody ran: the config
file, `gh auth login`, a signing key, that same key added to the GitHub account *as a signing
key*, ssh to the box, a checkout there, and the skill written into it. Every one of them was
discovered by a command failing part way through a review round, which is the most expensive
moment to learn it.

So `init` asks the config questions and then answers the rest of the list in one run: `gh`
installed and authenticated (`ViewerLogin`, so the answer is a name and not just an exit code),
a signing key here and the same key on the account, this checkout's `origin`, ssh to the box, a
box checkout `git` can read (`boxCurrentBranch`, the same call signing uses), and the harness
`pull` would start being present there.

- **It writes two things: this Mac's config file, and the skill on the box.** Everything else it
  reports. A setup command that installs a signing key, logs a `gh` in or clones a repo on
  another machine is a setup command doing things nobody asked for, in places nobody asked
  about.
- **The prompts are generated from `Config`'s fields**, like the file rendering and `set`, so a
  new key is a key `init` asks about with no second list to update. Enter keeps what the file
  has, and an accepted default is written as empty, never as today's default: `harness: claude`
  in the file is indistinguishable from a chosen value and would freeze the default on this
  machine forever.
- **Every gap is collected, not just the first.** They are independent, and a command that
  reports one per run is a command run four times. Each prints as it is found and again in a
  numbered summary at the end, because the fixes are what happens next and scrolling back for
  them is what makes people skip one.
- **`host` is asked twice if it has to be.** It is the one key with no default and the one
  everything else needs, so an empty first answer gets a second question saying so. An
  unattended run (no tty, EOF) still writes the file and warns, rather than blocking on a
  prompt nobody will answer.
- **Re-running is the point.** It keeps every answer and re-checks everything, so it doubles as
  the "why has this stopped working" command.

`set` and `init` both render the whole file from the struct: the keys, their order and the
settable list all come from `Config`'s yaml tags (`configFields`), with the per-key comments in
`configDoc`, so a new config field is settable and documented without touching the command.
Values go through `yaml.Marshal`, because `~`, `yes` and `user@host:2222` do not all survive
being printed by hand.

- **Neither writes a defaulted value.** A `remote_dir: ~/.sand` line is indistinguishable from
  one someone chose, so writing today's default would stop tomorrow's from ever reaching this
  machine. Every key is present and empty, with its default named in the comment above it.
- **`init` is idempotent, and is also the upgrade path.** It creates the file when missing and
  otherwise re-renders it: same values, plus any key added since the file was written, plus
  this version's comments. Running it twice writes the same bytes, so a setup script never has
  to ask which case it is in. It used to refuse a file that existed, which left no command at
  all for adding a new key to an old config.
- **`init` asks for the host, and takes no answer for an answer.** `--host` wins if given; an
  unattended run (no tty, EOF, empty line) writes the file with the host unset and warns,
  rather than blocking on a prompt nobody will answer or inventing a hostname.
- **Only `Resolve` requires a host, and `Get` does not go through it.** `Get` answers per key
  off the file, the environment and the defaults. It used to call `Resolve`, which meant that
  once `host` lost its default, `sand config get harness` failed on any machine that had not
  set a host, and `make sync` reads `config get host` on exactly those machines.
