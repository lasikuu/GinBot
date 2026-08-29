# Contributing

How a change gets from an idea to a commit here. For **getting the thing running** — installing
tools, starting the database, launching the three binaries, regenerating protobuf, mutual TLS — see
**[SETUP.md](SETUP.md)**. That document is authoritative and this one does not repeat it.

## Before you start

- **[AGENTS.md](../AGENTS.md)** is the conventions document, and it is authoritative. Read the
  section covering whatever you are about to touch; several of its notes exist because the obvious
  approach was tried and cost a day.
- **[docs/adr/README.md](adr/README.md)** records the decisions that are already settled. They are
  binding context, not history. Do not relitigate one without superseding it.

## The loop

1. Make the change.
2. Run the [verification gate](#verification) — all of it, including `-race`.
3. Write an [ADR](#architecture-decisions) if the change decided something hard to reverse.
4. Update [CHANGELOG.md](CHANGELOG.md) if the change is user-visible.
5. Commit in small logical groups, with [Conventional Commits](#commits).

## Verification

The gate is in [SETUP.md § Verification](SETUP.md#verification) — `go build`,
`go vet -structtag=false`, `gofmt -l`, `go test -race`, plus the
[integration suite](SETUP.md#integration-tests) behind the `integration` build tag.

Two things that have bitten more than once:

- **`gofmt -l` exits 0 even when it lists files.** Check the output is empty; do not trust the exit
  code.
- **`-race` is not optional.** The reverse action stream is concurrent on both ends.

CI additionally runs `golangci-lint` (pinned, configured by `.golangci.yml`), regenerates the protos
and fails on any diff under `pkg/gen`, checks `buf lint` and `buf breaking`, and asserts that the
`protoc-gen-connect-go` and `protovalidate` pins match `go.mod`. Run the codegen locally before
pushing a `.proto` change — see [SETUP.md § Regenerating protobuf](SETUP.md#regenerating-protobuf) —
or that job fails on version noise alone.

Coverage is printed, not gated. A threshold invites tests written to move the number.

## Tests

- Table-driven `_test.go` files next to the code they cover.
- Assert wire errors with `connect.CodeOf`, never by string matching.
- Handler tests go through `pkg/grpc/server/harness_test.go`, which boots a real Connect server
  over `httptest` with the production interceptor chain — so a handler test exercises clearance,
  validation and origin the way a deployed server does.
- Anything needing a live Postgres goes behind the `integration` build tag.
- A migration's `Down` block must be tested **with data present**, not against an empty schema.

Coverage report locally:

```bash
go test -race ./... -coverprofile coverage.out
go tool cover -html coverage.out
```

## Database migrations

Create them with `goose create <name> sql` **inside `pkg/db/migrations`**, or the `//go:embed`
filesystem will not pick them up. Manual `goose` invocations, the connection string and the enum
`COMMENT ON COLUMN` rule are in [SETUP.md § Migrations](SETUP.md#migrations).

Never edit a migration that has already been applied.

## Protocol Buffers

Sources live in `proto/ginbot/v1`; the generated Go under `pkg/gen` is **committed** and must never
be hand-edited. Regenerate with `buf`, never a bare `protoc` — the `protovalidate` schema is a pinned
BSR dependency and `protoc` cannot resolve it.

```bash
cd proto && buf generate
```

The pinned tool versions and the reasons behind them are in
[SETUP.md § Regenerating protobuf](SETUP.md#regenerating-protobuf).

`buf breaking` is enabled at the `FILE` category. The schema is settled
([ADR-0035](adr/0035-buf-breaking-is-enabled.md)), so a breaking change is now a deliberate act
requiring an ADR, not something to be absorbed in passing.

The generated API is **opaque**: no exported struct fields. Construct with
`pb.FooReq_builder{...}.Build()`, read with `GetX()`, test presence with `HasX()`, and pass pointers
for optional scalars and enums. You cannot `Scan` a database row into a protobuf message — that is
what `internal/model` exists for.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), scoped to the area touched:

```
feat(server): stream GetFile in chunks instead of inlining the file
fix(client): escalate reconnect backoff on refusal
chore(proto): regenerate
```

Append `!` for a breaking change (`feat(proto)!: ...`).

Commit in small logical groups rather than one commit per feature, and stage deliberately — file by
file, never `git add -A`. The commit body is where the *why* goes; `git log --oneline -20` shows the
register expected.

## Architecture decisions

Write an ADR when a decision is **hard to reverse**, or when a future reader would ask "why is it
done this way": datastore and schema strategy, architectural boundaries, wire formats and encoding
contracts, dependencies with licensing or security weight, rejecting an obvious option, or knowingly
accepting a limitation.

Not for bug fixes, refactors with no behavioural change, or naming.

Copy `docs/adr/.adr-template.md` to `NNNN-short-slug.md`, keep it to roughly one screen, state the
downsides honestly, and add it to the table in [docs/adr/README.md](adr/README.md). **Never edit an
accepted ADR to change its decision** — supersede it.

## Documentation

- [CHANGELOG.md](CHANGELOG.md) — user-visible changes, [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.
- [SETUP.md](SETUP.md) — anything about building, running or configuring.
- [AGENTS.md](../AGENTS.md) — conventions and the traps behind them.

Godoc for the packages themselves:

```bash
go install golang.org/x/tools/cmd/godoc@latest
godoc -http=localhost:8080
```
