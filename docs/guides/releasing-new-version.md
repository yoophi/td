# Releasing the yoophi/td Fork

## Distribution

Pushing a `vX.Y.Z` tag runs [release.yml](../../.github/workflows/release.yml).
The publication guard requires the tag to match live `origin/main`, an exact
changelog entry, and successful Go CI on that commit. GoReleaser publishes
macOS/Linux amd64/arm64 archives and checksums to
[yoophi/td releases](https://github.com/yoophi/td/releases).

The source-build formula lives in
[yoophi/homebrew-tap](https://github.com/yoophi/homebrew-tap/blob/main/Formula/td.rb).
Its **Update td formula** workflow checks stable fork releases hourly and supports
manual dispatch. It downloads the tagged source, computes SHA256, updates the
formula, and commits as `yoophi <yoophi@gmail.com>` using the tap's built-in
Actions token. There is no `HOMEBREW_TAP_TOKEN` prerequisite in the td repository.
This workflow does not write to the upstream `marcus/homebrew-tap`.

Before the first published fork release, the formula is HEAD-only:

```bash
brew install --HEAD yoophi/tap/td
```

HEAD follows the fork's `main`. Merge and push the intended changes there before
installing. Inherited upstream tags alone do not activate a stable formula.
After a stable fork release and successful formula update:

```bash
brew install yoophi/tap/td
brew update && brew upgrade yoophi/tap/td
```

Both upstream and fork formulae install `td`; uninstall the previously installed
formula before switching taps. Preserve project `.todos` directories. The fork
formula also installs `gh`, needed by GitHub Issues storage.

## Release procedure

1. Merge intended changes into `main`. Run `make test` and `make lint`.
2. Choose a new, unused `vX.Y.Z` tag. Add and commit a matching
   `## [vX.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md`.
3. Push `main`, then release from a clean checkout:

```bash
git push origin main
make release VERSION=vX.Y.Z
```

`make release` checks pushed main, dispatches missing Go CI when needed, runs
tests, creates an annotated tag, and pushes it. The release workflow independently
rechecks main and CI. Local CI checks default to `GH_REPO=yoophi/td`, avoiding the
GitHub CLI's possible upstream repository default for a fork.

4. Verify publication:

```bash
gh run list --repo yoophi/td --workflow release.yml --limit 1
gh run watch <run-id> --repo yoophi/td --exit-status
gh release view vX.Y.Z --repo yoophi/td
```

5. Wait for the tap's hourly check, or request an immediate update:

```bash
gh workflow run update-td.yml --repo yoophi/homebrew-tap -f version=vX.Y.Z
gh run list --repo yoophi/homebrew-tap --workflow update-td.yml --limit 1
gh run watch <tap-run-id> --repo yoophi/homebrew-tap --exit-status
```

GitHub schedules can be delayed. Manual dispatch is the direct update path; it
uses the operator's existing `gh` login and does not store that credential in CI.
Drafts, prereleases, invalid tags, missing source archives and downgrades do not
update the formula. No published releases is a successful no-op, preserving the
HEAD-only formula. If a published release update fails, inspect the tap workflow
log and rerun it after fixing the cause; a successful binary release alone does
not confirm a successful formula update.

6. Verify the source formula and installed version:

```bash
gh api repos/yoophi/homebrew-tap/commits --jq '.[0].commit.author.email'
brew update
brew upgrade yoophi/tap/td
td --version
```

The formula author/committer email must be `yoophi@gmail.com`.

## Update notifications

`td version` checks `https://api.github.com/repos/yoophi/td/releases/latest` and
recommends `brew update && brew upgrade yoophi/tap/td`. The Go module path remains
`github.com/marcus/td` for upstream compatibility; using `go install` with that
path would install upstream rather than this fork.

Results are cached for six hours in `~/.config/td/yoophi_version_cache.json`,
separately from upstream's cache. Development builds (`dev`, including Homebrew
HEAD builds) skip release checks.

## Local validation without publishing

```bash
make test
./scripts/test-release-tag.sh
actionlint .github/workflows/release.yml
# Optional binary release dry-run:
goreleaser release --snapshot --clean
```

In a checkout of `yoophi/homebrew-tap` (Python 3.11+):

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/update_td.py --dry-run
ruby -c Formula/td.rb
brew style Formula/td.rb
```

None of these commands creates a release tag or publishes a td release.
