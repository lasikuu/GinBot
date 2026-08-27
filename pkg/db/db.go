package db

import (
	"context"
	"embed"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

//go:embed migrations
var embedMigrations embed.FS

// *pgx.Conn represents a single connection to the database and is not concurrency safe.
// Using *pgxpool.Pool for a concurrency safe connection pool.
// https://pkg.go.dev/github.com/jackc/pgx/v5#hdr-Connection_Pool
var dbpool *pgxpool.Pool

// db allows access to the database connection pool
func db() *pgxpool.Pool {
	return dbpool
}

// InitDB initializes the database connection pool
func InitDB() {
	// Built via net/url rather than string concatenation so that passwords
	// containing reserved characters (@ : / ?) are escaped rather than
	// corrupting the URI.
	dbURI := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.Options.DB.Username, config.Options.DB.Password),
		Host:   net.JoinHostPort(config.Options.DB.Host, strconv.Itoa(int(config.Options.DB.Port))),
		Path:   config.Options.DB.Name,
	}

	var err error
	dbpool, err = pgxpool.New(context.Background(), dbURI.String())
	if err != nil {
		log.Z.Fatal("failed to connect to database.", zap.Error(err))
	}
}

// EnsureLatestVersion ensures that the database is at the latest version by running all migrations.
func EnsureLatestVersion() {
	if !config.Options.DB.Migrations {
		log.Z.Warn("database migrations are disabled.")
		return
	}

	// For embedding the migrations in the binary.
	goose.SetBaseFS(embedMigrations)

	if err := goose.SetDialect("postgres"); err != nil {
		log.Z.Fatal("failed setting DB dialect", zap.String("err", err.Error()))
	}

	// It is necessary to use the stdlib.OpenDBFromPool function to convert the *pgxpool.Pool to *sql.DB for goose.
	sqlDB := stdlib.OpenDBFromPool(db())
	defer func() {
		if err := sqlDB.Close(); err != nil {
			log.Z.Warn("failed to close migration database handle", zap.Error(err))
		}
	}()

	if err := goose.Up(sqlDB, "migrations"); err != nil {
		log.Z.Fatal("failed to apply new migrations", zap.Error(err))
	}
}

// CloseDB closes the database connection
func CloseDB() {
	db().Close()
}

// Ping reports whether the connection pool can currently reach Postgres. It
// backs cmd/ginbot-server's health surfaces (UtilityService/HealthCheck, the
// gRPC health protocol and GET /healthz) — added here rather than composed at
// the call site because dbpool is package-private by design, so answering
// "is the database reachable" is only ever askable from inside this package.
func Ping(ctx context.Context) error {
	return db().Ping(ctx)
}
