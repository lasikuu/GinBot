package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"strings"

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
	dbUri := strings.Builder{}
	dbUri.WriteString("postgres://")
	dbUri.WriteString(config.Options.DB.Username)
	dbUri.WriteString(":")
	dbUri.WriteString(config.Options.DB.Password)
	dbUri.WriteString("@")
	dbUri.WriteString(config.Options.DB.Host)
	dbUri.WriteString(":")
	dbUri.WriteString(strconv.Itoa(int(config.Options.DB.Port)))
	dbUri.WriteString("/")
	dbUri.WriteString(config.Options.DB.Name)

	var err error
	dbpool, err = pgxpool.New(context.Background(), dbUri.String())
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
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		panic(err)
	}

	err := goose.Up(sqlDB, "migrations")
	fmt.Println("")
	if err != nil {
		log.Z.Fatal("failed to apply new migrations", zap.String("err", err.Error()))
	}
}

// CloseDB closes the database connection
func CloseDB() {
	db().Close()
}

// rollbackTx rolls back a transaction
func rollbackTx(tx *sql.Tx) {
	err := tx.Rollback()
	if err != nil {
		log.Z.Debug("failed to rollback transaction", zap.String("err", err.Error()))
	}
}
