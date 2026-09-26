package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"xprem/internal/bucket"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/store"
	"xprem/internal/types"

	"github.com/pressly/goose/v3"
)

const backfillProgressInterval = 100

func init() {
	goose.AddMigrationNoTxContext(UpBackfillUpdateAssetMapping, DownBackfillUpdateAssetMapping)
}

// UpBackfillUpdateAssetMapping copies into updates.asset_mapping the mapping
// of completed updates imported from a bucket before that column existed.
func UpBackfillUpdateAssetMapping(ctx context.Context, _ *sql.DB) error {
	// Only wire.go injects the engine; tests run goose without it and have no imported rows.
	if dbEngine == nil {
		return nil
	}
	// No transaction: each mapping is committed as soon as it is copied, so an
	// interrupted boot resumes from the updates still missing one.
	queries := dbEngine.Queries
	rows, err := queries.ListUpdatesWithoutAssetMapping(ctx, int32(types.NormalUpdate))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	log.Printf("📦 [DATABASE] Backfilling the asset mapping of %d update(s) from the bucket", len(rows))
	updateStore := store.NewBucketUpdateStore(bucket.GetBucket())
	copied := 0
	for i, row := range rows {
		if i > 0 && i%backfillProgressInterval == 0 {
			log.Printf("📦 [DATABASE] Backfilled %d/%d update(s)", i, len(rows))
		}
		update := types.Update{AppId: row.AppID.String(), Branch: row.Branch, RuntimeVersion: row.RuntimeVersion, UpdateId: strconv.FormatInt(row.ID, 10)}
		mapping, err := updateStore.GetUpdateAssetMapping(ctx, update)
		if err != nil {
			// Corrupt historical metadata must not block startup. Storage errors
			// still abort the migration so it can be retried.
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
				log.Printf("⚠️ [DATABASE] Skipping invalid bucket asset mapping of update %s (app %s): %v", update.UpdateId, update.AppId, err)
				continue
			}
			return fmt.Errorf("reading the bucket asset mapping of update %s (app %s): %w", update.UpdateId, update.AppId, err)
		}
		if mapping == nil {
			continue
		}
		if _, err := queries.SetUpdateAssetMapping(ctx, pgdb.SetUpdateAssetMappingParams{ID: row.ID, AssetMapping: mapping, AppID: row.AppID, Name: row.Branch}); err != nil {
			return fmt.Errorf("storing the asset mapping of update %s (app %s): %w", update.UpdateId, update.AppId, err)
		}
		copied++
	}
	log.Printf("📦 [DATABASE] Backfilled the asset mapping of %d update(s) from the bucket", copied)
	return nil
}

func DownBackfillUpdateAssetMapping(context.Context, *sql.DB) error {
	return nil
}
