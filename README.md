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

Secrets are resolved **on the Swarm managers**, so do this on **every manager node**:

```bash
docker plugin install --grant-all-permissions --disable andipunz/op-sa-secret-driver:0.1.0
docker plugin set andipunz/op-sa-secret-driver:0.1.0 OP_SERVICE_ACCOUNT_TOKEN=ops_eyJ...
docker plugin enable andipunz/op-sa-secret-driver:0.1.0
```

Create the service account in 1Password (Developer → Service Accounts) with
**read-only** access to only the vaults Swarm needs.

### Settings (`docker plugin set …`)

| Variable | Default | Purpose |
|---|---|---|
| `OP_SERVICE_ACCOUNT_TOKEN` | – | Service account token (required) |
| `OP_DEFAULT_VAULT` | – | Vault used when a secret has neither `ref` nor `vault` |
| `OP_CACHE_TTL` | `0` | In-memory cache per reference (`60s`, `5m`, …); `0` = always fetch |
| `OP_TIMEOUT` | `30s` | Timeout per 1Password request |
| `OP_LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN`, `ERROR` |

The plugin must be disabled to change settings: `docker plugin disable -f …`, then `set`, then `enable`.

## Usage

### Full secret reference (recommended)

Copy the reference from 1Password (field menu → *Copy Secret Reference*):

```bash
docker secret create -d andipunz/op-sa-secret-driver:0.1.0 \
  -l ref="op://Swarm-Prod/postgres/password" db_password
docker service create --name db --secret db_password postgres:17
```

### Composed from labels

```bash
docker secret create -d andipunz/op-sa-secret-driver:0.1.0 \
  -l vault=Swarm-Prod -l item=postgres -l section=admin -l field=username db_user
```

### In a stack file

```yaml
secrets:
  db_password:
    driver: andipunz/op-sa-secret-driver:0.1.0
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

Names containing `/` can't be composed from labels. Use IDs or a `ref` instead.

## Operational notes

- **Rotation.** Values are fetched when tasks are scheduled. Running containers never
  see a change. Run `docker service update --force <svc>` after rotating in 1Password.
  With `OP_CACHE_TTL` set, wait for the TTL to expire first.
- **Rate limits.** Service accounts have hourly and daily request quotas that depend on
  your plan. Scaling a service to many replicas or redeploying big stacks can burn
  through them. `OP_CACHE_TTL=60s` collapses those bursts. `reuse=false` secrets are
  never cached.
- **Debugging.** When resolution fails, `docker service ps` only shows
  `secret … not found`. The real reason (bad reference, missing vault access, token
  problems, rate limit) is in the daemon log on the manager, e.g.
  `journalctl -u docker | grep op-sa`. Secret values are never logged.
- **Token exposure.** The token is visible in `docker plugin inspect` to anyone with
  access to the Docker socket, who is effectively root anyway. Keep the service account
  read-only and scoped to Swarm vaults.
- **Architectures.** Docker managed plugins are single-arch. Build and push one tag per
  architecture (for example `0.1.0-arm64`) if your managers are mixed.
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

## Protocol

It implements `docker.secretprovider/1.0` directly over `/run/docker/plugins/op.sock`
(`POST /Plugin.Activate`, `POST /SecretProvider.GetSecret`). The only direct
third-party dependency is the [1Password Go SDK](https://github.com/1password/onepassword-sdk-go);
see [`third_party_licenses/`](third_party_licenses/) for its transitive dependencies.

## License

[MIT](LICENSE).
