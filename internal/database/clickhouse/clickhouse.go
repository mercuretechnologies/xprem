package clickhouse

import (
	"context"
	"fmt"
	"log"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Engine is the ClickHouse handle behind the Observe feature: telemetry
// facts (observe metrics/logs, manifest-derived update health) are written
// and queried through it. It speaks the native protocol because ingestion
// wants columnar batch inserts; schema migrations go through database/sql
// separately (see migrations.go). Postgres remains the source of truth for
// the mutable identity dimension; ClickHouse only ever holds append-only
// facts and eventually-consistent projections.
type Engine struct {
	Conn driver.Conn
}

// NewClickHouseEngine mirrors database.NewPostgresEngine: parse, connect,
// ping, so a configured-but-unreachable ClickHouse surfaces at boot instead
// of on the first ingested batch. dsn is CLICKHOUSE_URL, e.g.
// clickhouse://user:password@host:9000/database
func NewClickHouseEngine(ctx context.Context, dsn string) (*Engine, error) {
	options, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("clickhouse configuration error: %w", err)
	}

	// Observe owns its tables in a dedicated database. Without a database in
	// the DSN the driver silently lands in ClickHouse's `default`; requiring
	// an explicit dedicated one keeps our schema (and its future drops)
	// clearly fenced from anything else the operator runs on that server.
	if options.Auth.Database == "" {
		return nil, fmt.Errorf("CLICKHOUSE_URL must target a dedicated database (e.g. clickhouse://user:password@host:9000/xprem)")
	}

	conn, err := ch.Open(options)
	if err != nil {
		return nil, fmt.Errorf("failed to open clickhouse connection: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clickhouse is unreachable: %w", err)
	}

	log.Println("🔌 [CLICKHOUSE] Connection established successfully")

	return &Engine{Conn: conn}, nil
}

func (e *Engine) Close() {
	if e.Conn != nil {
		log.Println("🔌 [CLICKHOUSE] Shutting down clickhouse connection...")
		_ = e.Conn.Close()
	}
}
