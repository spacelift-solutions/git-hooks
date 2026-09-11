# Spacelift repository checks

Shared repository checks for local Git hooks and GitHub Actions.

Both entrypoints call `bin/run-checks`. The local hook scans staged changes,
while the Action scans the repository's Git history. Betterleaks is the first
check, but the runner is intentionally check-agnostic so more controls can be
added without changing how repositories consume it.

## GitHub Action

Add this required check to a repository:

```yaml
name: Repository checks

on:
  pull_request:
    branches:
      - main
  push:
    branches:
      - main

permissions:
  contents: read

jobs:
  checks:
    name: Spacelift repository checks
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0

      - uses: spacelift-solutions/git-hooks@v1
```

`fetch-depth: 0` is required so checks can inspect the complete history. Make
the resulting workflow a required status check in the repository's ruleset.
CI is the enforcement boundary; local hooks can always be bypassed.

### Configure a repository

Use the bootstrap script to install the workflow and protect `main`:

```sh
scripts/setup-repository spacelift-solutions/example-repository
```

It creates or updates the managed workflow, then creates or updates two
repository rulesets:

- `Require The Four Ghostman review` requires one team approval and dismisses
  approvals whenever new commits are pushed.
- `Require Spacelift repository checks` requires the shared status check while
  allowing The Four Ghostman team to bypass that check when necessary.

The Four Ghostman team can bypass either ruleset when an emergency or
bootstrap change cannot satisfy it normally.

The script refuses to replace an unmanaged workflow. Use `--dry-run` to inspect
the generated ruleset without changing the target repository. It requires an
authenticated `gh` CLI with repository administration access.
Use `--rulesets-only` when the target already has an equivalent workflow under
a different path.

## Local hook

Clone this repository once:

```sh
git clone https://github.com/spacelift-solutions/git-hooks.git \
  "${XDG_DATA_HOME:-$HOME/.local/share}/spacelift-git-hooks"
```

Then run the installer from any repository you want to protect:

```sh
"${XDG_DATA_HOME:-$HOME/.local/share}/spacelift-git-hooks/install"
```

The installer:

- installs the repository's `Brewfile` with Homebrew;
- configures a repository-local managed `pre-commit` hook; and
- runs any existing local or global `pre-commit` hook first instead of
  replacing it.

Update the local checks by pulling this repository and rerunning `install`.
The installed files are outside the working tree and are not committed.

Run the checks without installing the hook:

```sh
bin/run-checks staged
bin/run-checks repository
```

## Adding checks

Add an executable to `checks/`. The runner calls each executable with:

```text
<check> <staged|repository> <absolute-repository-path>
```

Add dependencies to `Brewfile` when a new check needs another tool. The Action
and local installer both provision it through `scripts/setup-tools`.

## Betterleaks configuration

Betterleaks automatically loads `.betterleaks.toml` from the repository being
checked. Keep repository-specific configuration and exceptions there so local
and CI behavior stays consistent.
