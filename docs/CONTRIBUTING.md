# Contributing

## git

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) when making commits.

## Setup

### Protocol Buffers

- Generate Protocol Buffers:
  ```
    protoc --go_out=pkg/gen --go_opt=paths=source_relative --go-grpc_out=pkg/gen --go-grpc_opt=paths=source_relative ./proto/common.proto ./proto/utility.proto ./proto/reminder.proto ./proto/trigger.proto ./proto/entertainment.proto ./proto/discord.proto ./proto/analytics.proto ./proto/user_account.proto ./proto/highlight.proto ./proto/highlight.proto
  ```

### 🚧 Building and running

- TODO

### 💾 Database migrations

- Automatically run when the server is launched. Can be disabled by setting `GINBOT_DB_MIGRATIONS=false` in `.env`.
- Run manually
  - Create a new migration: `goose create <migration_name> sql`
  - Check status: `goose -dir pkg/db/migrations  postgres "user=postgres dbname=postgres password=gin123 sslmode=disable" status`
  - Run all migrations: `goose -dir pkg/db/migrations  postgres "user=postgres dbname=postgres password=gin123 sslmode=disable" up`
  - Rollback the last migration: `goose -dir pkg/db/migrations  postgres "user=postgres dbname=postgres password=gin123 sslmode=disable" down`

### 🔬 Testing

- Test: `go test ./... -v -coverprofile "coverage.out"`
- Show coverage report: `go tool cover -html "coverage.out"`

### 📝 Generating docs

- Run `godoc -http=localhost:8080`
- Go to `http://localhost:8080/pkg/#thirdparty`

## Requirements for development

- Go 1.23+
- [Protocol Buffers Edition 2023+](https://github.com/protocolbuffers/protobuf/releases)
    - Set the `bin` directory to the `PATH`
- Go Protocol Buffers plugins
  ```
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```
- godoc: `go install golang.org/x/tools/cmd/godoc@latest`
- goose: `go install github.com/pressly/goose/v3/cmd/goose@latest`
- Docker (optional)
