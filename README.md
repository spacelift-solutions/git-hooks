# Spacelift repository checks

Shared repository checks for local Git hooks and GitHub Actions.

Both entrypoints call `bin/run-checks`. The local hook scans staged changes,
while the Action scans the repository's Git history. Gitleaks is the first
check, but the runner is intentionally check-agnostic so more controls can be
added without changing how repositories consume it.

## GitHub Action

Add this required check to a repository:

```yaml
name: Repository checks

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

jobs:
  checks:
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

- provisions the pinned tool versions into the target repository's Git
  metadata;
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

Add any pinned dependency setup to `scripts/setup-tools`. The Action and local
installer both use that setup path, keeping tool versions aligned.

## Gitleaks configuration

Gitleaks automatically loads `.gitleaks.toml` and `.gitleaksignore` from the
repository being checked. Keep repository-specific exceptions there so local
and CI behavior stays consistent.
