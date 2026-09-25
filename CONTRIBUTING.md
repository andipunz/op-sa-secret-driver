# Contributing

Issues and PRs are welcome.

## Devcontainer

Opening this repo in VS Code (with the Dev Containers extension) or GitHub Codespaces
picks up [`.devcontainer/devcontainer.json`](.devcontainer/devcontainer.json)
automatically: Go 1.26 (matching the Dockerfile) plus the Docker CLI, talking to your
host's Docker daemon so `make test`, `make plugin`, and `docker buildx build` all work
inside it exactly as they do outside.

## Building and testing

Everything runs in a container, so a local Go install isn't required:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine sh -c \
  'gofmt -l . && go vet ./... && go test ./... -race'
```

(If you do have Go 1.26+ installed, `make test` runs the same `go vet`/`go test`.)

`make plugin` builds the rootfs and creates a local plugin from it (see the
[Build](README.md#build) section); `make plugin PLATFORM=linux/arm64` cross-builds for
another architecture from any host.

## Before opening a PR

- `gofmt -l .` reports nothing, `go vet ./...` is clean, `go test ./... -race` passes.
- Add or update tests for behavior you change — `internal/driver` and
  `internal/onepassword` both fake their external dependency (the HTTP protocol and
  the 1Password SDK client, respectively), so no real Docker daemon or 1Password
  account is needed to test most changes.
- Update [`CHANGELOG.md`](CHANGELOG.md)'s `[Unreleased]` section for anything
  user-visible.
- Update `README.md` if you change a label, setting, or documented behavior.

## What can't be tested here

No CI environment (or contributor, most likely) has a live 1Password Service Account
to test against — everything upstream of the SDK's own `Resolve` call is covered by
fakes. If you're changing `internal/onepassword`, the fake-based tests are the bar; if
you're changing how the plugin *uses* 1Password (label handling, caching, the retry
logic), those are what to add to.

## Releasing (maintainers)

1. Move `[Unreleased]` in `CHANGELOG.md` into a new `## [x.y.z] - YYYY-MM-DD` section.
2. `git tag vx.y.z && git push origin vx.y.z`.
3. CI builds and pushes the plugin to Docker Hub and opens a GitHub Release with that
   changelog section and the two platform binaries attached.
