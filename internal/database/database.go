package database

import (
	"context"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/database/postgres/pgdb"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func IsValidDBURL(str string) bool {
	parsedURL, err := url.Parse(str)
	if err != nil {
		return false
	}
	// No helpers.IsValidURL check here because some DBs don't have a host
	return parsedURL.Scheme != ""
}

func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" // "unique_violation"
	}
	return false
}

func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// IsForeignKeyViolation covers both ways a foreign key can refuse a write.
// They are different SQLSTATEs and the distinction is easy to miss: pointing
// at a row that does not exist raises foreign_key_violation, while deleting a
// row something still points at raises restrict_violation when the constraint
// is ON DELETE RESTRICT. Checking only the first turns "this role is still
// assigned" into an opaque 500.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503" || // "foreign_key_violation"
			pgErr.Code == "23001" // "restrict_violation"
	}
	return false
}

type DBTX interface {
	pgdb.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
	Close()
}

type Engine struct {
	*pgdb.Queries
	DB DBTX
}

type Config struct {
	ConnStr         string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

func LoadDBConfigFromEnv() Config {
	maxConnsStr := config.GetEnv("DB_MAX_CONNS")
	minConnsStr := config.GetEnv("DB_MIN_CONNS")
	maxLifetimeStr := config.GetEnv("DB_MAX_CONN_LIFETIME")
	maxIdleTimeStr := config.GetEnv("DB_MAX_CONN_IDLE_TIME")

	maxConns, _ := strconv.Atoi(maxConnsStr)
	minConns, _ := strconv.Atoi(minConnsStr)
	maxLifetime, _ := time.ParseDuration(maxLifetimeStr)
	maxIdleTime, _ := time.ParseDuration(maxIdleTimeStr)

	return Config{
		ConnStr:         config.GetEnv("DB_URL"),
		MaxConns:        int32(maxConns),
		MinConns:        int32(minConns),
		MaxConnLifetime: maxLifetime,
		MaxConnIdleTime: maxIdleTime,
	}
}

func NewPostgresEngine(ctx context.Context, dbConfig Config) (*Engine, error) {
	config, err := pgxpool.ParseConfig(dbConfig.ConnStr)
	if err != nil {
		return nil, fmt.Errorf("database configuration error: %w", err)
	}

	config.MaxConns = dbConfig.MaxConns
	config.MinConns = dbConfig.MinConns
	config.MaxConnLifetime = dbConfig.MaxConnLifetime
	config.MaxConnIdleTime = dbConfig.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to spawn pgx connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database system is unreachable: %w", err)
	}

	log.Println("🔌 [DATABASE] Connection pool established successfully")

	return &Engine{
		Queries: pgdb.New(pool),
		DB:      pool,
	}, nil
}

func (e *Engine) Close() {
	if e.DB != nil {
		log.Println("🔌 [DATABASE] Shutting down database connection...")
		e.DB.Close()
	}
}

// WithTx executes a set of queries inside a single PostgreSQL transaction.
// If the callback returns an error, the transaction is rolled back.
// Otherwise, it is committed.
func (e *Engine) WithTx(ctx context.Context, fn func(q *pgdb.Queries) error) error {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	qtx := pgdb.New(tx)
	if err := fn(qtx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}
