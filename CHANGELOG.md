# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versioning follows
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-09-25

### Added

- Docker Swarm `secretprovider` plugin resolving `op://` references via the official
  1Password Go SDK, authenticated with a Service Account token — no 1Password Connect
  server required.
- `OP_SERVICE_ACCOUNT_TOKEN_FILE` plus a bind mount as the recommended way to supply
  the token, so it never appears in `docker plugin inspect`; the file is re-read on
  every reconnect, so token rotation doesn't require restarting the plugin.
- In-memory caching (`OP_CACHE_TTL`) with coalescing of concurrent identical lookups,
  so a service scaled to many replicas (or a large stack redeploy) costs at most one
  1Password API call per reference instead of one per task.
- Cross-compiling, digest-pinned Dockerfile producing a static binary on a `scratch`
  rootfs for both `linux/amd64` and `linux/arm64` from a single host, no QEMU needed.
- CI (GitHub Actions): `gofmt`/`go vet`/`go test -race`/`govulncheck` on every push and
  PR, plus a Docker Hub publish of both architectures on tagged releases.
- MIT license; third-party license notices for the SDK's transitive dependencies.

[Unreleased]: https://github.com/andipunz/op-sa-secret-driver/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/andipunz/op-sa-secret-driver/releases/tag/v0.1.0
