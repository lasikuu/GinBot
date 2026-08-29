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

// *pgx.Conn is a single connection and is not concurrency safe; a pool is.
// https://pkg.go.dev/github.com/jackc/pgx/v5#hdr-Connection_Pool
var dbpool *pgxpool.Pool

func db() *pgxpool.Pool {
	return dbpool
}

func InitDB() {
	// net/url escapes passwords containing reserved characters (@ : / ?).
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

func EnsureLatestVersion() {
	if !config.Options.DB.Migrations {
		log.Z.Warn("database migrations are disabled.")
		return
	}

	goose.SetBaseFS(embedMigrations)

	if err := goose.SetDialect("postgres"); err != nil {
		log.Z.Fatal("failed setting DB dialect", zap.String("err", err.Error()))
	}

	// goose needs a *sql.DB, not a pool.
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

func CloseDB() {
	db().Close()
}

// Ping backs the server's health surfaces; it lives here because dbpool is
// package-private.
func Ping(ctx context.Context) error {
	return db().Ping(ctx)
}
