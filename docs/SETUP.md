# Setup and launch

How to get GinBot running locally.

## Requirements

| Tool | Version | Needed for |
|---|---|---|
| Go | see `go.mod` | everything |
| Docker + Compose | any recent | the Postgres container |
| `buf` + protoc plugins | see [Regenerating protobuf](#regenerating-protobuf) | only when editing `.proto` files |
| `goose` CLI | any recent | only for manual migration control |
| `openssl` | any recent | only for mutual TLS |

## Architecture in one paragraph

Three binaries, one **Connect** boundary. `ginbot-server` owns Postgres, migrations and cron, and
serves every service over `net/http` — Connect, gRPC and gRPC-Web all on the same endpoint, since
`connect-go` handlers speak all three. `ginbot-discord` and `ginbot-matrix` are platform clients
that never touch the database. **Start the server first**, then whichever clients you want.

The Go package is still `pkg/grpc` and the environment variables are still `GINBOT_GRPC_*`; only the
names survived the port from `google.golang.org/grpc`, which is no longer a dependency at all.

---

## 1. Database

```bash
docker compose -f docker-compose.dev.yml up -d
```

Postgres on `localhost:5432`, user `ginbot`, password `gin123`.

The container's entrypoint creates the `ginbot` database automatically (Postgres defaults the
database name to `POSTGRES_USER`). Verify:

```bash
docker compose -f docker-compose.dev.yml ps
```

> Data lives in the named volume `ginbot_dev_pgdata`, so it survives
> `docker compose down` and image updates. Use `docker compose -f docker-compose.dev.yml down -v`
> when you actually want a clean database; migrations re-run on the next boot.

## 2. Configuration

```bash
cp example.env .env
```

Then edit `.env`. Every variable is documented inline there. The minimum to get a Discord bot
talking to a local server:

```bash
DISCORD_BOT_TOKEN=<your token>
DISCORD_CLIENT_ID=<your application id>
DISCORD_OWNER_ID=<your user id>
```

Two things that catch people out:

- **Do not prefix `DISCORD_BOT_TOKEN` with `Bot `.** The config layer adds it.
- `.env` is loaded from the **working directory**, and it is gitignored. Never commit it.

Environment variables are read only in `internal/config`; nothing else calls `os.Getenv`. The same
`.env` also drives `docker-compose.prod.yml`, which interpolates the secrets and feature flags out
of it rather than declaring a second set of names — `internal/config/exampleenv_test.go` fails the
build if `example.env` and `internal/config` disagree in either direction, so the list stays whole.

## 3. Run

Run from the **repository root**. The TLS certificate paths resolve against the working
directory, so launching from elsewhere breaks mutual TLS.

```bash
# Terminal 1 — server (owns the DB, runs migrations at boot)
go run ./cmd/ginbot-server

# Terminal 2 — Discord client
go run ./cmd/ginbot-discord

# Terminal 3 — Matrix client (optional)
go run ./cmd/ginbot-matrix
```

The server applies any pending migrations on startup unless `GINBOT_DB_MIGRATIONS=false`.

In development (`GINBOT_ENV` unset or anything but `production`) the server also enables reflection,
registering both the `v1` and `v1alpha` handlers, so `grpcurl` works against it:

```bash
grpcurl -plaintext localhost:50051 list
```

Because the handlers speak the Connect protocol too, a unary procedure is also reachable with plain
`curl` and a JSON body — useful for poking at it without any gRPC tooling:

```bash
curl -sS -H 'Content-Type: application/json' -d '{}' \
  http://localhost:50051/ginbot.v1.UtilityService/Ping
```

There is a plain-HTTP liveness endpoint as well. It answers 200 while serving and 503 during
shutdown, backed by the same `db.Ping` as `UtilityService/HealthCheck` and the gRPC health protocol:

```bash
curl -i http://localhost:50051/healthz
```

The production container healthcheck reads that endpoint, but it does **not** use `curl` or `wget`
to do it — it runs the server binary itself:

```bash
ginbot-server -healthcheck    # exits 0 when healthy, non-zero otherwise
```

That is not a stylistic choice. With `GINBOT_GRPC_TLS=true` the listener is TLS-only and demands a
client certificate from every peer, which no HTTP client in the runtime image can present. Running
the binary means the probe picks up `GINBOT_GRPC_TLS`, `GINBOT_GRPC_PORT` and `GINBOT_CERTS_PATH`
from the same environment the server binds from, and loads certificates through the same code path
the platform clients use — so a healthy container under TLS also tells you mutual TLS itself is
working. It dials loopback, which is why `docker-compose.prod.yml` binds the server to `0.0.0.0`.

Both clients shut down cleanly on `Ctrl-C` (SIGINT) or SIGTERM. So does the server — it flips
`/healthz` to 503, waits a few seconds so a prober can see it, then drains. A second SIGINT skips
that wait.

## 4. Try it

Slash commands are registered **globally** on startup, which can take a few minutes to appear the
first time.

| Platform | Command | Does |
|---|---|---|
| Discord | `/healthcheck` | server health |
| Discord | `/doubles`, `/triples` | random 2- and 3-digit roll, with a re-roll button |
| Discord | `/number [lower] [upper]` | random number in `[lower, upper)`, default `[0, 10)` |
| Matrix | `!healthcheck` | server health |

Set `DISCORD_REMOVE_COMMANDS=true` to delete the registered commands on shutdown, which is useful
while iterating on command definitions.

---

## Verification

The four commands CI runs:

```bash
go build ./...
go vet -structtag=false ./...    # plain `go vet` fails on the generated opaque structs in pkg/gen
gofmt -l cmd internal pkg        # must print nothing
go test -race ./...
```

> `gofmt -l` **exits 0 even when it lists files**. Check that the output is empty, do not rely on
> the exit code.

`-race` is not optional here. The reverse action stream is concurrent on both ends, and several
tests drive it.

### Integration tests

These need a live Postgres and are behind a build tag, so `go test ./...` skips them:

```bash
docker compose -f docker-compose.dev.yml up -d
go test -tags=integration -race -count=1 ./...
```

They read the same `GINBOT_DB_*` variables as the server and run migrations themselves.
`-count=1` matters: without it Go can replay a cached pass without touching the database.

---

## Migrations

Migrations live in `pkg/db/migrations` and are `//go:embed`-ed into the server binary, so the
compiled server carries its own schema history.

Create a new one with the `goose` CLI so the embedded filesystem picks it up:

```bash
cd pkg/db/migrations
goose create <name> sql
```

Manual control:

```bash
export GOOSE_DRIVER=postgres
export GOOSE_DBSTRING="host=localhost port=5432 user=ginbot dbname=ginbot password=gin123 sslmode=disable"

goose -dir pkg/db/migrations status
goose -dir pkg/db/migrations up
goose -dir pkg/db/migrations down
```

Every migration must have a working `Down` block — CI does not check this, but reversibility is
expected. Test it with data present, not just against empty tables.

Proto enums are stored as integers. When a migration touches an enum column, keep its
`COMMENT ON COLUMN` in sync with `proto/ginbot/v1/platform.proto`.

---

## Regenerating protobuf

Generated Go is **committed** under `pkg/gen/ginbot/v1`. Regenerate it; never hand-edit it.

Codegen runs through `buf`, not a bare `protoc`. Install it and the two plugins at the pinned
versions — CI compares against output produced by these, so a different version creates spurious
diffs:

```bash
go install github.com/bufbuild/buf/cmd/buf@v1.72.0
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.1
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.20.0
export PATH=$PATH:$(go env GOPATH)/bin
```

`protoc-gen-connect-go`'s version must equal the `connectrpc.com/connect` requirement in `go.mod`.
CI asserts it, against the installed plugin rather than against the `go install` line above. The
generated files guard the runtime with a `connect.IsAtLeastVersion...` constant, so a plugin newer
than the module is a compile error — but a module newer than the plugin is silent, which is what the
check is for. Move both together.

Then, from `proto/` — that directory is the module root, so `buf` needs no include flags and no
file list:

```bash
cd proto
buf generate
```

Two things land, from the two plugins:

```
pkg/gen/ginbot/v1/*.pb.go                        # protoc-gen-go: messages, enums, builders
pkg/gen/ginbot/v1/ginbotv1connect/*.connect.go   # protoc-gen-connect-go: clients, handlers, Procedure constants
```

The `ginbotv1connect` name comes from the explicit `;ginbotv1` suffix on every `go_package`. Without
it the Go package name is inferred from the import path's last element and the stubs land in
`v1connect/` instead; `proto/buf.gen.yaml` records why that was not left implicit.

Commit the regenerated files alongside the `.proto` change. CI fails if they drift, and the
comparison is exact: `buf` reports no compiler version to the plugins, so the
`protoc (unknown)` line in every generated header is identical on every machine. Generating with a
bare `protoc` instead stamps its own version there and will show up as a diff in every generated
file.

`buf` also compiles the protos itself, so a local `protoc` is no longer a requirement at all.

### The schema is frozen

`proto/buf.yaml` enables `buf breaking` at the `FILE` category, and CI runs it on every event. The
schema settled at the end of the Connect port; before that, breaking the wire was free because
nothing outside this repository consumed it, and the port spent that window deliberately. It is now
closed — see [ADR 0035](adr/0035-buf-breaking-is-enabled.md).

Check your change locally before pushing:

```bash
cd proto
buf lint
buf breaking --against '../.git#ref=refs/remotes/origin/main,subdir=proto'
```

`FILE` rather than `WIRE`: the wire categories only catch what changes the bytes, but `pkg/gen` is
committed and every call site compiles against it, so renaming a field is wire-compatible and still
breaks the build across `cmd/`, `pkg/discord` and `pkg/matrix` at once. Adding a field and
deprecating an RPC both still pass.

If you genuinely need a breaking change, **reserve both the number and the name**, the way
`OpenClientActionStreamReq` does:

```proto
message OpenClientActionStreamReq {
  reserved 1;
  // Editions require reserved names as identifiers, not string literals.
  reserved platform_enum;
}
```

Reserving the number stops a later field silently reusing it with a different type; reserving the
name stops the old name coming back meaning something else.

### The protovalidate dependency

`instance.proto`, `reminder.proto`, `repost.proto`, `reverse.proto`, `trigger.proto` and
`user.proto` import `buf/validate/validate.proto`. That
schema is **not** in this repository — `proto/buf.yaml` declares it as a dependency and
`proto/buf.lock` pins the exact [BSR](https://buf.build/bufbuild/protovalidate) commit, which
`buf` fetches and caches. Codegen therefore needs network access the first time (or a warm
`buf` cache).

The pinned commit must stay in step with the `buf.build/gen/go/bufbuild/protovalidate/...`
requirement in `go.mod`; that module version embeds the same commit's 12-character prefix. CI
asserts it. To move the pin:

```bash
cd proto && buf dep update      # rewrites buf.lock
go get buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go@latest
```

Then regenerate and check the two commit IDs agree. See
[ADR 0028](adr/0028-protovalidate-schema-is-a-pinned-buf-dependency.md).

### Working with the opaque API

The protos use `edition = "2023"` with `api_level = API_OPAQUE`, so generated structs have **no
exported fields**:

```go
// Construct
msg := pb.GetUserReq_builder{Id: &id}.Build()

// Read
id := msg.GetId()

// Test presence
if msg.HasId() { ... }
```

Optional scalars and enums in builders take **pointers** (`platform.Enum()`, `&localVar`). This is
also why database rows are scanned into `internal/model` structs rather than directly into
protobuf messages — pgx cannot scan into an opaque struct.

---

## Mutual TLS (optional)

Off by default, and plaintext is a supported configuration rather than a degraded one — see
[ADR 0032](adr/0032-mtls-is-mounted-and-optional.md). To enable it:

```bash
cd cert && ./generator.sh && cd ..
```

Then in `.env`:

```bash
GINBOT_GRPC_TLS=true
GINBOT_CERTS_PATH=cert      # optional; `cert` is the default
```

The server requires and verifies a client certificate, and both ends require TLS 1.3.

`GINBOT_CERTS_PATH` is relative to the **working directory**, so with the default a binary must be
launched from the repository root.

**`cert/server-ext.conf` must list every hostname a client dials**, because
`auth.ClientTLSConfig` sets neither `ServerName` nor `InsecureSkipVerify` — deliberately, with a
test pinning it. A missing SAN fails the handshake with an error that says nothing about the cause.
It currently covers `ginbot-server` (the Compose service name) and `localhost`; add yours and
regenerate if you dial the server by any other name.

`cert/*.pem` and `cert/*.srl` are gitignored. **Never commit them.**

Only `ginbot-server` needs `server-cert.pem` and `server-key.pem`; the clients need
`client-cert.pem` and `client-key.pem`. Both need `ca-cert.pem`. One shared client certificate
covers both platform clients, so mTLS currently authenticates the fleet rather than an individual
process — [ADR 0012](adr/0012-trusted-platform-clients-assert-identity.md) records what that does
and does not buy.

---

## Production compose

Not needed for local development; this is what
`docker compose -f docker-compose.prod.yml pull && docker compose -f docker-compose.prod.yml up -d`
actually does.

Each binary runs its own published image (see [Published images](#published-images)). There are no
`build:` blocks, so `up --build` does nothing; add one in an override file to run a locally compiled
image instead. `psql` and `ginbot-server` always start; the two clients sit behind Compose profiles,
so set `COMPOSE_PROFILES` in `.env`:

```bash
COMPOSE_PROFILES=discord          # server + discord
COMPOSE_PROFILES=discord,matrix   # server + both clients
```

The one variable the stack refuses to start without is **`GINBOT_DB_PASSWORD`**. The `psql` service
maps it onto the `POSTGRES_PASSWORD` its image insists on, so the secret is spelled once, under the
name `example.env` documents and `internal/config` reads.

Six things about that file are easy to get wrong and are commented in place:

- **`GINBOT_GRPC_HOST` means two different things.** It is both the address the server binds and
  the address the clients dial. The shared anchor sets it to `ginbot-server` for the clients, and
  the server service overrides it to `0.0.0.0` for itself — binding a service name binds only that
  container's own eth0 address and leaves loopback unbound, which is what the healthcheck probes.
- **The clients wait for the server's healthcheck**, not merely for its container to start
  (`depends_on: condition: service_healthy`). `stop_grace_period` on the server exceeds its own
  drain plus shutdown timeout, or the runtime would SIGKILL it before `db.CloseDB` and `log.Sync`
  run.
- **`./cert` is mounted read-only on all three services** unconditionally, so enabling mutual TLS is
  `cd cert && ./generator.sh` plus `GINBOT_GRPC_TLS=true` in `.env`, with no compose edit at either
  step. The mount is harmless while TLS is off — nothing reads it. The server needs the *client*
  key pair too, because its healthcheck is an mTLS client of itself.
- **The healthcheck runs the server binary, not `wget`.** Under TLS the listener requires a client
  certificate, which no HTTP client in the image can present; since the clients gate on
  `service_healthy`, a probe that cannot pass under TLS stops the whole stack rather than just
  mislabelling one container.
- **`GINBOT_STORAGE_PATH` must be on a volume**, and is — `ginbot_prod_storage` at `/app/storage` on
  the server alone. Trigger media is persistent state: the server fetches and stores the bytes while
  the `file` row pointing at them lives in Postgres, so leaving the default `./storage` in the
  container's writable layer does not merely lose files on the next image update, it leaves
  surviving rows referencing blobs that are gone.
- **Repost detection needs ffmpeg for video and animated GIF, and the images do not carry one.**
  `GINBOT_REPOST` is passed through for both binaries, but [ADR 0006](adr/0006-ffmpeg-as-a-subprocess.md)
  keeps ffmpeg a system dependency of whoever deploys rather than something GinBot redistributes.
  Its absence degrades those formats to exact-hash-only; still images are unaffected and nothing
  crashes. Point `GINBOT_REPOST_FFMPEG_PATH` at a mounted binary, or build a derived image.

`cmd/ginbot-server/compose_test.go` asserts those relationships against the actual file, so a change
that breaks one of them fails `go test ./...` rather than only failing on deploy.

---

## Published images

`docker-compose.prod.yml` runs these images directly — it has no `build:` blocks.
`.github/workflows/publish.yml` builds the three Dockerfile stages for `linux/amd64` and
`linux/arm64` and pushes them to the GitHub Container Registry:

```
ghcr.io/lasikuu/ginbot-server
ghcr.io/lasikuu/ginbot-discord
ghcr.io/lasikuu/ginbot-matrix
```

| Trigger | Tags |
| --- | --- |
| push to `main` | `:main`, `:edge`, `:sha-<full commit>` |
| tag `v1.2.3` | `:1.2.3`, `:1.2`, `:latest`, `:sha-<full commit>` |
| pull request | built for both architectures, pushed nowhere |

`:main` and `:edge` are **not** release tags. The publish workflow is triggered by the same push
that triggers CI and does not wait for it, so an image can exist for a commit whose tests failed —
[ADR 0036](adr/0036-images-are-published-to-ghcr.md) records why that trade was taken. Deploy a
`v*.*.*` tag, or better a digest.

Every pushed digest carries a provenance attestation and an SBOM, both as manifests in the image
index and as a Sigstore attestation recorded against the repository:

```bash
gh attestation verify oci://ghcr.io/lasikuu/ginbot-server:latest --repo lasikuu/GinBot
docker buildx imagetools inspect ghcr.io/lasikuu/ginbot-server:latest
```

`docker-compose.prod.yml` references these images by their `:latest` tag and carries no `build:`
block, so the normal flow is `docker compose ... pull` then `up -d`, never `--build`. Pin a
`v*.*.*` tag or a digest for anything you actually deploy. To run a locally compiled image instead,
add a `build:` block back in a compose override file.

`cmd/ginbot-migrate` has no image on purpose: the server applies the embedded goose migrations at
boot unless `GINBOT_DB_MIGRATIONS=false`, so that binary is an operator tool rather than a deployed
component.

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| `failed to connect to database` | The container is not up, or `GINBOT_DB_*` in `.env` disagrees with `docker-compose.dev.yml`. |
| `failed to read ca pem` | `GINBOT_GRPC_TLS=true` but certificates were never generated, or you launched from outside the repository root. |
| `x509: certificate is valid for …, not …` | The host you dial has no SAN in `cert/server-ext.conf`. Add it and regenerate; there is no verification bypass. |
| Slash commands do not appear | They register globally, which propagates slowly. Wait, or restart the Discord client. |
| A command replies "application did not respond" | The server is not running, or the client cannot reach it. Check terminal 1. |
| `ginbot-platform-enum is required` | An RPC was called without caller identity headers. Clients attach them via `pkg/grpc/callermeta`. |
| `caller is not registered` | The RPC needs a `user_account` row and the caller has none yet. |
| Every unary call works but reminders never arrive | HTTP/2 was not negotiated, so the bidi reverse stream silently fell back to HTTP/1.1, where it cannot work. Plaintext HTTP/2 needs `h2c` on the server and `AllowHTTP` on the client transport — both explicit. |
| `resource_exhausted` on a large request | Every service is capped at 4 MiB per message. Trigger files are not sent as one message; `GetFile` streams them in 1 MiB chunks. |
| `too many client action streams` | 64 reverse streams are already registered. The refused client backs off and reattaches on its own. |
| Integration tests fail to connect | Postgres is not up, or the `GINBOT_DB_*` variables are not exported into the test environment. |
| `go vet` errors inside `pkg/gen` | You ran plain `go vet ./...`. Use `-structtag=false`. |
| `buf breaking` fails in CI but passes locally | A pull-request checkout has no local `refs/heads/main`. Compare against `refs/remotes/origin/main` instead. |

