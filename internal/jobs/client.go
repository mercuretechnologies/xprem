// Package jobs runs background work on River, a Postgres-backed job queue.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"
	"xprem/internal/database"
	"xprem/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

var ErrAlreadyRunning = errors.New("a job of this kind is already running for this scope")

// Serializes rivermigrate.Migrate across replicas; distinct from the goose lock.
const riverMigrationLockID = 823672942

type Client struct {
	pool    *pgxpool.Pool
	workers *river.Workers
	queue   *river.Client[pgx.Tx]
}

func NewClient(engine *database.Engine) (*Client, error) {
	pool, ok := engine.DB.(*pgxpool.Pool)
	if !ok {
		return nil, fmt.Errorf("the jobs client needs the pgx pool, got %T", engine.DB)
	}
	return &Client{pool: pool, workers: river.NewWorkers()}, nil
}

// Workers is the registry to add workers to, before Start.
func (c *Client) Workers() *river.Workers {
	return c.workers
}

func (c *Client) Start(ctx context.Context) error {
	driver := riverpgxv5.New(c.pool)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return fmt.Errorf("failed to prepare the river migrator: %w", err)
	}
	// The lock lives on its own connection, not a pooled one: Migrate talks
	// to the pool, and a one-connection pool would deadlock if the lock sat
	// on that only slot.
	lockConn, err := pgx.ConnectConfig(ctx, c.pool.Config().ConnConfig)
	if err != nil {
		return fmt.Errorf("failed to open a connection for the river migration lock: %w", err)
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	if _, err := lockConn.Exec(lockCtx, "SELECT pg_advisory_lock($1)", riverMigrationLockID); err != nil {
		cancel()
		_ = lockConn.Close(context.Background())
		return fmt.Errorf("failed to acquire the river migration lock: %w", err)
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	_, _ = lockConn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", riverMigrationLockID)
	cancel()
	_ = lockConn.Close(context.Background())
	if err != nil {
		return fmt.Errorf("failed to migrate the river schema: %w", err)
	}
	riverClient, err := river.NewClient(driver, &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: c.workers,
	})
	if err != nil {
		return fmt.Errorf("failed to build the river client: %w", err)
	}
	if err := riverClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start the river client: %w", err)
	}
	c.queue = riverClient
	log.Println("🛠️  [JOBS] River job client started")
	return nil
}

func (c *Client) Stop() {
	if c == nil || c.queue == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.queue.Stop(ctx); err != nil {
		log.Printf("[jobs] river client did not stop cleanly: %v", err)
	}
}

// Enqueue inserts args as a job; ErrAlreadyRunning when a unique constraint
// skipped the insert.
func (c *Client) Enqueue(ctx context.Context, args river.JobArgs) (string, error) {
	if c == nil || c.queue == nil {
		return "", store.ErrNotSupportedInStatelessMode
	}
	inserted, err := c.queue.Insert(ctx, args, nil)
	if err != nil {
		return "", fmt.Errorf("failed to enqueue the job: %w", err)
	}
	if inserted.UniqueSkippedAsDuplicate {
		return "", ErrAlreadyRunning
	}
	return strconv.FormatInt(inserted.Job.ID, 10), nil
}

func (c *Client) Get(ctx context.Context, jobId string) (*rivertype.JobRow, error) {
	if c == nil || c.queue == nil {
		return nil, nil
	}
	id, err := strconv.ParseInt(jobId, 10, 64)
	if err != nil {
		return nil, nil
	}
	job, err := c.queue.JobGet(ctx, id)
	if errors.Is(err, rivertype.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the job: %w", err)
	}
	return job, nil
}

// LatestByArg returns the newest job of the kind whose args carry the value
// at the key, or nil.
func (c *Client) LatestByArg(ctx context.Context, kind string, argKey string, argValue string) (*rivertype.JobRow, error) {
	if c == nil || c.queue == nil {
		return nil, nil
	}
	params := river.NewJobListParams().
		Kinds(kind).
		Where("args->>@key = @value", river.NamedArgs{"key": argKey, "value": argValue}).
		OrderBy(river.JobListOrderByID, river.SortOrderDesc).
		First(1)
	listed, err := c.queue.JobList(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list the scope's jobs: %w", err)
	}
	if len(listed.Jobs) == 0 {
		return nil, nil
	}
	return listed.Jobs[0], nil
}

// Cancel stops a job: immediately when it still waits, at its next context
// check when it runs. False means the job is unknown.
func (c *Client) Cancel(ctx context.Context, jobId string) (bool, error) {
	if c == nil || c.queue == nil {
		return false, nil
	}
	id, err := strconv.ParseInt(jobId, 10, 64)
	if err != nil {
		return false, nil
	}
	_, err = c.queue.JobCancel(ctx, id)
	if errors.Is(err, rivertype.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to cancel the job: %w", err)
	}
	return true, nil
}
