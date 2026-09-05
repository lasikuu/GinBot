# Changelog

All notable changes of this project will be documented in this file.

> The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres
> to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The wire layer moved from `google.golang.org/grpc` to [Connect](https://connectrpc.com), and the
schema took every breaking change it had been deferring while nothing outside this repository
consumed it. That window is now closed: `buf breaking` runs in CI and the schema is settled.

**All of this is breaking.** Server and both clients must be deployed together; there is no
compatibility path from any earlier build. Stored data is unaffected — `instance_meta` and
`destination_meta` keep their shape and their jsonb keys, deliberately.

### Changed

- **The transport is Connect over `net/http`.** `google.golang.org/grpc` is no longer a dependency,
  directly or transitively. The handlers serve the Connect, gRPC and gRPC-Web protocols on one
  endpoint, so a unary procedure is now reachable with plain `curl` and a JSON body.
- **The proto package is `ginbot.v1`** (was `ginbot.proto`), with sources under `proto/ginbot/v1/`
  and generated Go under `pkg/gen/ginbot/v1/`.
- **Caller identity travels as prefixed HTTP headers** — `ginbot-platform-enum`, `ginbot-user-id`,
  `ginbot-instance-uid`, `ginbot-destination-uid` — including on the reverse stream, which used to
  take its platform from the request body.
- **`TriggerService/GetFile` streams.** It returns one metadata chunk followed by content chunks of
  at most 1 MiB instead of a whole file inline, so no handler buffers an entire file. Every service
  is now capped at the same 4 MiB per message; `TriggerService`'s raised cap is gone.
- Eight vestigial identity fields were deleted from request messages, and thirteen RPCs that
  returned `google.protobuf.Empty` now return named response messages.
- The reverse stream's action payload is a typed `oneof` rather than a `google.protobuf.Struct`.
- Errors are `connect.Code*` rather than gRPC status codes. `Unavailable` now reaches a Discord user
  as a distinct message instead of a generic "Something went wrong."

### Added

- **Authorisation covers streaming RPCs.** Every interceptor — recovery, validation, clearance,
  origin — runs on streams as well as unary calls, so `OpenClientActionStream` is guarded for the
  first time and a streamed `GetFile` is refused before a single byte is sent.
- A reflection-driven test fails the build when a mounted procedure is missing from the clearance
  requirements map, which is default-open.
- protovalidate rules on the trigger and instance schemas, including a bound on how many instances
  one `CreateTrigger` may name.
- `GET /healthz` alongside the gRPC health protocol and `UtilityService/HealthCheck`, all three
  backed by one database probe. `docker-compose.prod.yml` uses it, and both clients now wait for the
  server to report healthy rather than merely for its container to start.
- Bounded, graceful shutdown: open reverse streams are released first, the health endpoint reports
  503 for a few seconds so a prober can observe it, then connections drain. A second interrupt skips
  the wait.
- Transport defaults on the platform clients: keepalive, a default deadline for calls that supply
  none, and automatic retry of `Unavailable` on an allowlist of read-only procedures.
- Mutual TLS certificates are mounted on all three services in production compose, so enabling
  `GINBOT_GRPC_TLS` needs no deployment-file edit.
- **Container images are published to the GitHub Container Registry** — `ghcr.io/lasikuu/`
  `ginbot-server`, `ginbot-discord` and `ginbot-matrix`, for `linux/amd64` and `linux/arm64`.
  A push to `main` publishes `:main`, `:edge` and `:sha-<commit>`; a `v*.*.*` tag publishes
  `:1.2.3`, `:1.2` and `:latest`. Every digest carries a provenance attestation and an SBOM, so
  `gh attestation verify oci://... --repo lasikuu/GinBot` can tie an image back to the commit that
  produced it. Deploying no longer means compiling on the target host. See
  [ADR 0036](adr/0036-images-are-published-to-ghcr.md), including why `:main` and `:edge` are not
  release tags.
- A `.dockerignore`. The build context is the repository root and the Dockerfile copies all of it,
  so `.env`, `cert/*.pem` and the `/rin` export were previously baked into any image built by hand.
- A roll from the die button names whoever clicked it, in a code span after the number. The reply is
  an ordinary channel message with no "used /doubles" attribution of its own, so nothing else said
  who rolled.
- **A fired trigger can be taken back.** Whoever's message fired it has seven seconds to answer
  `no`, `ei` or `del` in the same channel; the bot's response and the undo message are both deleted.
  Only that author, only that channel, only once. Deleting the undo message needs
  `MANAGE_MESSAGES`; without it that delete simply fails and the response is removed anyway. This
  is Discord-only and the fire is still counted in `triggerstats` — see
  [ADR 0037](adr/0037-the-trigger-undo-window-is-discord-only.md).
- Colloquial Finnish chat aliases for the digit rolls: `??tuplilla` and `??tuplil` alongside
  `??tuplat`, and the same for `triples`, `quads`, `quints` and `sexts`. Discord allows one name per
  locale, so these are chat-only; the slash commands are unchanged.

### Fixed

- A reverse-stream handler could block forever on a client message that never came, so the server
  could not shut down cleanly and was killed after the grace period.
- A failed send left a client holding a registry slot and a full buffer for a stream it could no
  longer be written to.
- Reconnect backoff now escalates when the server refuses a stream, instead of resetting and
  retrying once a second for as long as the refusal lasts.
- A panic in a client action handler took the whole client process down; it now costs one delivery.
- The Matrix client never started its action stream, so it received no server-pushed actions.
- A failed Discord session close called `os.Exit`, skipping the log flush.
- `ListTriggers` no longer forces the Discord client into an extra round trip purely to learn the
  caller's own id.
- `GINBOT_CERTS_PATH` is now actually used; it was parsed into config and ignored.
- **`docker-compose.prod.yml` reads the database password from `GINBOT_DB_PASSWORD`, not
  `POSTGRES_PASSWORD`.** `example.env` only ever documented `GINBOT_DB_PASSWORD`, so `cp example.env
  .env` followed by the prod stack failed with `POSTGRES_PASSWORD is required` while the password
  that was set went unread. The secret now has one name — the `psql` service still maps it onto the
  `POSTGRES_PASSWORD` its image requires. `cmd/ginbot-server/compose_test.go` pins that only that one
  name is interpolated.
- **Trigger media now persists in production.** `ginbot-server` wrote blobs to `./storage` inside
  the container's writable layer, so every image update discarded them while the `file` rows
  referencing them survived in Postgres, leaving file-replying triggers broken. It now mounts a
  named `ginbot_prod_storage` volume at `GINBOT_STORAGE_PATH`, pinned by a compose test.
- `example.env` was missing eleven variables `internal/config` reads — `GINBOT_STORAGE_PATH`,
  `DISCORD_MESSAGE_CONTENT`, `GINBOT_REPOST` and the eight `GINBOT_REPOST_*` knobs. It is now
  complete, and `internal/config/exampleenv_test.go` fails the build if it drifts from the code in
  either direction. `docker-compose.prod.yml` also passes these through, and documents that video
  and animated-GIF repost fingerprinting needs an ffmpeg the published images do not carry
  ([ADR 0006](adr/0006-ffmpeg-as-a-subprocess.md)).
- Stale operational docs: `docker-compose.prod.yml` runs published GHCR images and has no `build:`
  block, so `up --build` no longer applies; the dev compose keeps its data in a named volume. Both
  `docs/SETUP.md` and the compose file's own comments said otherwise.
- A missing `.env` no longer reports `error loading environment vars` on startup. No image carries
  one and containers configure from the environment, so every containerised binary logged it on
  every start. A `.env` that exists but cannot be read is still reported.

## [0.1.0] - 2025-01-04

### Added

- Initial protocol buffers for the core features of the bot
