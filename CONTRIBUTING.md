# Contributing

## Choose a first issue

Browse [the ecosystem starter tasks](https://github.com/plutoframework/pluto/blob/main/CONTRIBUTING.md#starter-tasks).
Each task should have a narrow scope, an observable outcome and clear checks.
Search existing issues before opening another. For a proposal, explain the user
problem and smallest useful change before writing a broad implementation.

## Work in the right layer

- Read this repository's `AGENTS.md` when present and the linked reference docs.
- Keep Go standard-library boundaries and optional adapters explicit.
- Use the CLI for application scaffolds; maintainers fix generator source, rebuild
  and test disposable consumers instead of patching generated output.
- Keep handlers thin, authority server-owned and authored files protected.
- Use configured 80-column expanded formatting. Put a blank line above standalone
  comments except at file line one; preserve required compiler/tool directives.
- Keep staged capabilities off by default and test both flag states.

## Validate the change

Run from this repository's root:

```sh
mise exec golangci-lint@2.15.0 -- ./scripts/check.sh
```

Use the repository's additional race, bounded fuzz/mutation and integration checks
for the behavior changed. Real-service checks require isolated services; a skip is
not a pass. For docs-only changes, check links and execute changed examples. Never
suppress checks to hide a failure; report missing prerequisites and untested edges.

## Send a focused contribution

Explain the problem, resulting behavior, validation and any compatibility or
migration needs. Include a regression for a reproduced bug where practical. UI
changes need exercised loading, empty, error, success and accessibility states;
async actions must acknowledge immediately and handle uncertain outcomes honestly.

Do not commit secrets, production data, local credentials or unrelated changes.
Follow any repository security policy and use GitHub's private vulnerability
reporting when enabled. Do not post undisclosed exploit details in a public issue;
if no private route is offered, ask maintainers how to report privately without
including sensitive details. Publishing a patch is not an independent audit.

## Maintainer commands

Run these from the library repository root; no application `pluto.toml` is needed:

```sh
make fmt    # apply formatting
make vet    # check formatting, lint and build without fixes
make check  # standard checks, including tests
make test   # tests only
```

Use the Go version in `go.mod`, Python 3.11+ and either the pinned
`golangci-lint` on PATH or mise (see `mise.toml`). The existing scripts select
the linter and cover the library modules, including optional integrations.
Run `make test TEST_ARGS=-race` for race detection.

Keep running the documented fuzz, mutation and service integration checks when
the change requires them; `make check` does not replace those checks.

Go checks enforce a blank line before each standalone comment block except at
file line one, including comments at the start of a code block. Consecutive
comment lines stay together; inline comments, generated files and compiler/linter
directives are preserved. The formatter applies comment spacing after the Go
formatters; checks compare that combined result so gofumpt cannot undo this rule.
The stdlib-only helper in `scripts/commentspacing` is synchronized from
`pluto/scripts/commentspacing`; run `go test ./scripts/commentspacing` after
changing it. Test fixtures under `testdata` are excluded from formatting.
