# op-sa-secret-driver

A Docker Swarm **secret driver plugin** that pulls secrets from 1Password using a
**Service Account** token. There's no 1Password Connect server to deploy,
operate or keep highly available.

It uses the official [1Password Go SDK](https://github.com/1password/onepassword-sdk-go),
talks directly to 1Password.com, and is a single static binary on a `scratch` rootfs.

```
docker service create ──► Swarm manager ──► plugin (op.sock) ──► 1Password SDK ──► 1Password.com
                                  │                                   (service account token)
                                  └──► value sent to the worker, mounted at /run/secrets/<name>
```

## How it differs from the Connect-based drivers

| | Connect drivers (opsd, op-connect-secret-driver) | this driver |
|---|---|---|
| Needs | Connect API + sync containers, credentials file, `OP_CONNECT_HOST`/`OP_CONNECT_TOKEN` | one `ops_…` token |
| Availability | Connect must be up on every manager's network | 1Password.com reachable from managers |
| Rate limits | none (local cache in Connect) | service account quotas, so enable `OP_CACHE_TTL` for large fan-outs |
| Access scope | Connect token's vaults | service account's vaults (read-only recommended) |

## Install

Secrets are resolved **on the Swarm managers**, so do this on **every manager node**.
Create the service account in 1Password (Developer → Service Accounts) with
**read-only** access to only the vaults Swarm needs.

### Standard: token as a file, not a plugin setting (recommended)

`docker plugin set OP_SERVICE_ACCOUNT_TOKEN=...` stores the token in the plugin's
settings, which anyone able to run `docker plugin inspect` can read back in plain text.
Put the token in a file on each manager instead (mode `0400`, owned by root — the same
way you'd protect any other credential file) and point the plugin at it:

```bash
docker plugin install --grant-all-permissions --disable ghcr.io/andipunz/op-sa-secret-driver:0.1.0
docker plugin set ghcr.io/andipunz/op-sa-secret-driver:0.1.0 token.source=/etc/docker/op-token
docker plugin set ghcr.io/andipunz/op-sa-secret-driver:0.1.0 OP_SERVICE_ACCOUNT_TOKEN_FILE=/run/secrets/op-service-account-token
docker plugin enable ghcr.io/andipunz/op-sa-secret-driver:0.1.0
```

`/run/secrets/op-service-account-token` is the mount's fixed destination inside the
plugin; `token.source` is the only thing you set, to the real path on the host. The
plugin re-reads this file each time it needs a fresh SDK session, so rotating the token
on disk (e.g. via your config management, or a `docker plugin set token.source=...` to a
new file) takes effect without restarting the plugin.

#### Why not a Docker/Swarm secret?

You can't `docker secret create` the token and hand it to the plugin the normal way:
Swarm secrets are mounted into service **tasks** (`/run/secrets/<name>` inside a
container started by `docker service create`), and the plugin v2 config schema
(`config.json`) that managed plugins like this one use has no `secrets` field —
only `env` and `mounts`. It's also circular: this plugin is *how* Swarm secrets get
their values, so it can't itself depend on the Swarm secrets mechanism to receive its
own bootstrap credential. The bind-mounted file above is the closest equivalent Docker
gives a plugin, and is the standard way managed plugins that need a credential (e.g.
the official Vault plugins) handle this.

### Quick start (token as a plugin setting)

Simpler, but the token is then readable via `docker plugin inspect`:

```bash
docker plugin install --grant-all-permissions --disable ghcr.io/andipunz/op-sa-secret-driver:0.1.0
docker plugin set ghcr.io/andipunz/op-sa-secret-driver:0.1.0 OP_SERVICE_ACCOUNT_TOKEN=ops_eyJ...
docker plugin enable ghcr.io/andipunz/op-sa-secret-driver:0.1.0
```

### Settings (`docker plugin set …`)

| Variable | Default | Purpose |
|---|---|---|
| `OP_SERVICE_ACCOUNT_TOKEN` | – | Service account token. Ignored if `OP_SERVICE_ACCOUNT_TOKEN_FILE` is set |
| `OP_SERVICE_ACCOUNT_TOKEN_FILE` | – | Path to the token file (see "Standard" above); one of the two is required |
| `OP_DEFAULT_VAULT` | – | Vault used when a secret has neither `ref` nor `vault` |
| `OP_CACHE_TTL` | `0` | In-memory cache per reference (`60s`, `5m`, …); `0` = always fetch |
| `OP_TIMEOUT` | `30s` | Timeout per 1Password request |
| `OP_LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN`, `ERROR` |

The plugin must be disabled to change settings: `docker plugin disable -f …`, then `set`, then `enable`.

## Usage

### Full secret reference (recommended)

Copy the reference from 1Password (field menu → *Copy Secret Reference*):

```bash
docker secret create -d ghcr.io/andipunz/op-sa-secret-driver:0.1.0 \
  -l ref="op://Swarm-Prod/postgres/password" db_password
docker service create --name db --secret db_password postgres:17
```

### Composed from labels

```bash
docker secret create -d ghcr.io/andipunz/op-sa-secret-driver:0.1.0 \
  -l vault=Swarm-Prod -l item=postgres -l section=admin -l field=username db_user
```

### In a stack file

```yaml
secrets:
  db_password:
    driver: ghcr.io/andipunz/op-sa-secret-driver:0.1.0
    labels:
      ref: "op://Swarm-Prod/postgres/password"
```

See [`examples/stack.yml`](examples/stack.yml).

### Labels

| Label | Default | Meaning |
|---|---|---|
| `ref` | – | Full `op://vault/item[/section]/field` reference. Wins over the labels below |
| `vault` | `OP_DEFAULT_VAULT` | Vault name or ID |
| `item` | the Docker secret name | Item name or ID |
| `section` | – | Section name or ID |
| `field` | `password` | Field name or ID |
| `attribute` | – | Reference attribute, e.g. `otp` for the current TOTP code, `type` |
| `encoding` | raw | `base64` decodes the stored text first, for binary files such as keystores |
| `reuse` | `true` | `false` sets `DoNotReuse`: Swarm calls the driver for every task, bypassing the cache |

Names containing `/` or `?` can't be composed from labels. Use IDs or a `ref` instead.

## Operational notes

- **Rotation.** Values are fetched when tasks are scheduled. Running containers never
  see a change. Run `docker service update --force <svc>` after rotating in 1Password.
  With `OP_CACHE_TTL` set, wait for the TTL to expire first.
- **Rate limits.** Service accounts have hourly and daily request quotas that depend on
  your plan. Concurrent requests for the same reference (e.g. a service scaled to many
  replicas scheduled at once) always share one 1Password call. Across separate bursts —
  a redeploy, a rolling update — `OP_CACHE_TTL=60s` collapses those too. `reuse=false`
  secrets are never cached, but concurrent requests for one are still coalesced.
- **Debugging.** When resolution fails, `docker service ps` only shows
  `secret … not found`. The real reason (bad reference, missing vault access, token
  problems, rate limit) is in the daemon log on the manager, e.g.
  `journalctl -u docker | grep op-sa`. Secret values are never logged.
- **Token exposure.** Anyone with access to the Docker socket is effectively root
  anyway, but use `OP_SERVICE_ACCOUNT_TOKEN_FILE` (above) rather than the plain
  `OP_SERVICE_ACCOUNT_TOKEN` setting to avoid also leaking the token to `docker plugin
  inspect` output, backup tooling, or anyone who greps `dockerd`'s on-disk plugin state.
  Either way, keep the service account read-only and scoped to Swarm vaults.
- **Architectures.** Docker managed plugins are single-arch. Build and push one tag per
  architecture (for example `0.1.0-arm64`) if your managers are mixed — `make plugin
  PLATFORM=linux/arm64 TAG=0.1.0-arm64` cross-compiles for it from any host, no
  arm64 machine or QEMU required. The CI pipeline (below) does this for both
  architectures on every release automatically.
- **Egress.** Managers need HTTPS to `*.1password.com` (or `*.1password.eu` /
  `*.1password.ca`, depending on your account region). The plugin uses host networking.

## Build

```bash
make test
make plugin              # builds the rootfs image and runs `docker plugin create`
make enable TOKEN=ops_…  # local testing
make push                # push to your registry (set PLUGIN=registry/name)
```

`go.sum` is committed; run `go mod tidy` again only after changing dependencies.

## CI / releasing

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push and PR:
`gofmt`, `go vet`, `go test -race`, `govulncheck`, and a build-only check of the
plugin image for both `linux/amd64` and `linux/arm64`.

Pushing a tag matching `v*.*.*` (e.g. `v0.2.0`) additionally:

- builds and pushes the plugin to the [GitHub Container Registry](https://ghcr.io) as
  `ghcr.io/andipunz/op-sa-secret-driver:<version>-amd64` and `...-arm64` (Docker managed
  plugins are single-arch — see above);
- creates a GitHub Release with the matching [`CHANGELOG.md`](CHANGELOG.md) section as
  its notes, attaching the two binaries and a `checksums.txt`. Update the changelog
  (move `[Unreleased]` into a new `[x.y.z] - YYYY-MM-DD` section) before tagging, or
  the release notes will be empty.

Both use the workflow's own `GITHUB_TOKEN` — no registry secrets to set up. The token
needs `packages: write`, which the workflow requests explicitly (via `permissions:` on
the `publish` job), so it works regardless of the repository's default token
permissions under *Settings → Actions → General → Workflow permissions*.

**Before the first release**, GHCR creates the package as *private* on its first push.
Docker plugin *installs* (unlike `docker pull`) don't prompt for a registry login, so an
unauthenticated `docker plugin install` against a private package fails outright. Make
the package public once it exists: on GitHub, the repo's right sidebar → *Packages* →
`op-sa-secret-driver` → package *Settings* → *Change visibility*.

Update `PLUGIN_REPO` in the workflow (and the `andipunz/` references in this README) if
you're not publishing under that namespace.

Docker plugin distribution uses the standard registry v2 API — verified locally against
a throwaway `registry:2` container (create → push → reinstall round-tripped cleanly) —
but this project's own GHCR push hasn't happened yet since it hasn't been tagged. Watch
the first `publish` run.

## Protocol

It implements `docker.secretprovider/1.0` directly over `/run/docker/plugins/op.sock`
(`POST /Plugin.Activate`, `POST /SecretProvider.GetSecret`). The only direct
third-party dependency is the [1Password Go SDK](https://github.com/1password/onepassword-sdk-go);
see [`third_party_licenses/`](third_party_licenses/) for its transitive dependencies.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to build, test, and release. See
[`CHANGELOG.md`](CHANGELOG.md) for what's changed between versions.

## License

[MIT](LICENSE).
