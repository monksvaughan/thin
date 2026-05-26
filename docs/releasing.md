# Releasing thin

Thin uses semantic version tags, GoReleaser, GitHub Releases, and a Homebrew tap.

## Versioning

Use SemVer-style tags:

```bash
v0.1.0
v0.1.1
v0.2.0
v1.0.0
```

For now, before a stable public API/CLI contract, prefer `v0.x.y`:

- Patch release: bug fixes, docs, small internal improvements.
- Minor release: new flags, behavior changes, new supported providers/routes.
- Major release: after `v1.0.0`, incompatible CLI/config/behavior changes.

The binary exposes build metadata:

```bash
thin version
```

Release builds populate `version`, `commit`, and `date` via Go linker flags.

## One-time setup

### Main repository

The release workflow runs when a `v*` tag is pushed. It creates GitHub Release
artifacts using the built-in `GITHUB_TOKEN`.

### Homebrew tap

GoReleaser updates:

```text
github.com/monksvaughan/homebrew-tap/Casks/thin.rb
```

Create a GitHub fine-grained personal access token that can write to the
`monksvaughan/homebrew-tap` repository, then add it to the `thin` repository as
an Actions secret named:

```text
HOMEBREW_TAP_GITHUB_TOKEN
```

The default `GITHUB_TOKEN` usually cannot push to a different repository, which
is why this separate token is needed.

## Local checks before tagging

```bash
go test ./...
goreleaser check
goreleaser release --snapshot --clean
```

Or, if `make` is available:

```bash
make release-check
```

Snapshot artifacts are written to `dist/` and are not published.

## Create a release

Make sure `main` is clean and pushed, then tag and push:

```bash
git checkout main
git pull --ff-only
git status

git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

GitHub Actions will build binaries, publish the GitHub Release, and update the
Homebrew tap.

## Install via Homebrew

After the release workflow updates the tap:

```bash
brew tap monksvaughan/tap
brew install thin
thin version
```

To upgrade later:

```bash
brew update
brew upgrade thin
```
