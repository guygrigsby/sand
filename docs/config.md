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

`sand config` prints the file, `sand config init` creates or updates it, `sand config set <key>
<value>...` sets any key, and `sand config get <key>` prints one effective value and nothing
else, for scripts and the Makefile (`get` applies env and the defaults; `config` only shows what
the file says).

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
