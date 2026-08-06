package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/crypto"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/helpers"
	"xprem/internal/keyStore"
	"xprem/internal/store"
	"xprem/internal/types"
	update2 "xprem/internal/update"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(UpMigrateEnvJSON, DownMigrateEnvJSON)
}

func toTimestamptz(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func parseRFC3339ToTz(str string, fieldName string) pgtype.Timestamptz {
	if parsedTime, err := time.Parse(time.RFC3339, str); err == nil {
		return toTimestamptz(parsedTime.UTC())
	}
	ms, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		log.Printf("⚠️ Warning: Malformed time string for %s ('%s'), falling back to Now: %v", fieldName, str, err)
		return toTimestamptz(time.Now())
	}
	normalizedTime := helpers.NormalizeTimestamp(ms)
	return toTimestamptz(normalizedTime)
}

// sealLegacyKeysIntoDB converts an app whose signing keys live outside the
// database, a PEM file on disk (mode=local) or a base64 env var
// (mode=environment), into mode=database, sealing the key material under the
// master key.
//
// Neither legacy mode survives the move to the control plane as-is. The
// dashboard refuses to create local-mode apps because key files cannot be
// provisioned on every replica, and the apps table has no column for an inline
// b64 key, so an environment-mode app would migrate with no key at all and only
// fail at its first signature. Sealing both at migration time keeps the DB row
// self-sufficient.
//
// The existing pair is resealed verbatim, never regenerated: expo-updates
// clients pin the certificate at build time, so a new pair would break
// signature verification on every already-installed binary until it is rebuilt
// and shipped through the stores.
//
// aws-secrets-manager is deliberately left alone. Like a key path it is only a
// reference to external custody, but unlike a path it stays reachable from
// every replica and is still creatable from the dashboard.
func sealLegacyKeysIntoDB(app config.AppConfig, params *pgdb.MigrateLegacyAppParams) error {
	if app.Keys.Mode != config.KeysModeLocal && app.Keys.Mode != config.KeysModeEnvironment {
		return nil
	}

	// The migration reads the flat env without the full stateless-boot
	// validation, so a control-plane pod whose deployment carries EXPO_APP_ID
	// but not the key vars arrives here as mode=local (the KEYS_STORAGE_TYPE
	// default) with empty paths. Name the real problem, missing env vars,
	// instead of failing later with a generic unreadable-keys error.
	structurallyEmpty := (app.Keys.Mode == config.KeysModeLocal && (app.Keys.PublicPath == "" || app.Keys.PrivatePath == "")) ||
		(app.Keys.Mode == config.KeysModeEnvironment && (app.Keys.PublicB64 == "" || app.Keys.PrivateB64 == ""))
	if structurallyEmpty {
		return fmt.Errorf("app %q: the flat env resolves to keys mode=%s but its key env vars are empty. "+
			"When upgrading into the control plane, keep the v2 key vars set (KEYS_STORAGE_TYPE and its mode-specific siblings) "+
			"so the legacy app's signing keys can be sealed into the database, then retry the migration",
			app.Id, app.Keys.Mode)
	}

	// Read through keyStore so each legacy mode is resolved the same way the
	// v1 server resolved it (file read, or b64 decode). Both return "" on any
	// failure, which must abort: sealing an empty string would migrate cleanly
	// and then break manifest signing at runtime.
	publicKey := keyStore.GetPublicExpoKey(app)
	privateKey := keyStore.GetPrivateExpoKey(app)
	if publicKey == "" || privateKey == "" {
		return fmt.Errorf("app %q: cannot read the existing mode=%s signing keys to seal them into the database. "+
			"Make sure the key files or env vars are readable by this process, then retry the migration",
			app.Id, app.Keys.Mode)
	}

	masterKey := []byte(keyStore.ReadDBKeysMasterKey())
	if len(masterKey) != 32 {
		return fmt.Errorf("app %q: migrating mode=%s signing keys into the database needs a 32-byte master key, got %d bytes. "+
			"Set DB_KEYS_MASTER_KEY_B64 (44-char base64) or AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID before migrating",
			app.Id, app.Keys.Mode, len(masterKey))
	}

	sealedPublicKey, err := crypto.SealAESGCM([]byte(publicKey), masterKey, keyStore.AppKeyAAD(app.Id, true))
	if err != nil {
		return fmt.Errorf("app %q: failed to seal public key: %w", app.Id, err)
	}
	sealedPrivateKey, err := crypto.SealAESGCM([]byte(privateKey), masterKey, keyStore.AppKeyAAD(app.Id, false))
	if err != nil {
		return fmt.Errorf("app %q: failed to seal private key: %w", app.Id, err)
	}

	databaseMode := string(config.KeysModeDatabase)
	params.KeysMode = &databaseMode
	params.SealedPublicKey = &sealedPublicKey
	params.SealedPrivateKey = &sealedPrivateKey
	// Drop the now-stale pointers to the external key material.
	params.PathPublicKey = nil
	params.PathPrivateKey = nil

	log.Printf("🔑 [DATABASE] App %s: sealed its mode=%s signing keys into the database (same key pair, now mode=database)", app.Id, app.Keys.Mode)
	return nil
}

func UpMigrateEnvJSON(ctx context.Context, tx *sql.Tx) error {
	// A fresh control-plane install has nothing to import: apps are created from
	// the dashboard and EXPO_APP_ID is deliberately unset. Bail out before
	// ReadAppsFromFlatEnv, which reports an absent flat-env config as an error, goose
	// turns that into a fatal and the server could never boot. Mirrors the guard
	// in the sibling bucket migration (20260422_v2_scope_data_under_appid).
	if strings.TrimSpace(os.Getenv("EXPO_APP_ID")) == "" {
		log.Println("⏭️ [DATABASE] No legacy EXPO_APP_ID set, nothing to migrate from env/bucket.")
		return nil
	}

	log.Println("🚀 [DATABASE] Starting migration of infrastructure from env/bucket to the database...")
	config.LoadAppsFromFlatEnv()
	apps, source, err := config.ReadAppsFromFlatEnv()
	if err != nil {
		log.Printf("Error reading apps from config: %v", err)
		return err
	}
	log.Printf("📦 [DATABASE] Migrating %d legacy app(s) from %s", len(apps), source)

	err = dbEngine.WithTx(ctx, func(qtx *pgdb.Queries) error {
		resolvedBucket := bucket.GetBucket()
		branchStore := store.NewBucketBranchStore(resolvedBucket)
		channelStore := store.NewBucketChannelStore(resolvedBucket)
		updateStore := store.NewBucketUpdateStore(resolvedBucket)

		for _, app := range apps {
			// The apps table keys on a UUID, which only the control plane mints.
			// A legacy id predating it cannot be represented, but failing here
			// would brick an otherwise healthy stateless deploy on upgrade, skip
			// the app instead and let the operator recreate it from the dashboard.
			parsedAppId, err := uuid.Parse(app.Id)
			if err != nil {
				log.Printf("⚠️ [DATABASE] Skipping legacy app %q: its id is not a UUID, so it cannot be migrated into the control plane. Recreate the app from the dashboard.", app.Id)
				continue
			}
			// Canonicalize before the id is used as both the row key and the
			// key-sealing AAD. uuid.Parse accepts uppercase, braced and urn:
			// forms, but the row always reads back as the plain lowercase form -
			// sealing under a non-canonical EXPO_APP_ID would bind the blob to an
			// id no unseal ever reconstructs, breaking signing after migration.
			// app is a per-iteration copy, so this does not touch the loaded config.
			app.Id = parsedAppId.String()

			params := pgdb.MigrateLegacyAppParams{
				ID:                 store.ToPgUUID(app.Id),
				Name:               app.Name,
				KeysMode:           helpers.StringOrNil(string(app.Keys.Mode)),
				SealedPublicKey:    helpers.StringOrNil(string(app.Keys.SealedPublicKey)),
				SealedPrivateKey:   helpers.StringOrNil(string(app.Keys.SealedPrivateKey)),
				PathPublicKey:      helpers.StringOrNil(string(app.Keys.PublicPath)),
				PathPrivateKey:     helpers.StringOrNil(string(app.Keys.PrivatePath)),
				AwsSecretIDPublic:  helpers.StringOrNil(string(app.Keys.PublicSecretId)),
				AwsSecretIDPrivate: helpers.StringOrNil(string(app.Keys.PrivateSecretId)),
			}

			if err := sealLegacyKeysIntoDB(app, &params); err != nil {
				log.Printf("Error migrating signing keys for app '%s': %v", app.Id, err)
				return err
			}

			if err := qtx.MigrateLegacyApp(ctx, params); err != nil {
				log.Printf("Error inserting app '%s' into database: %v", app.Id, err)
				return err
			}

			// Fetch branches for the app
			branches, err := branchStore.GetBranches(ctx, app.Id)
			if err != nil {
				log.Printf("Error fetching branches for app '%s': %v", app.Id, err)
				return err
			}

			branchNameToBranchId := make(map[string]int64)
			insertedRuntimeVersions := make(map[string]bool)

			for _, branch := range branches {
				branchId, err := qtx.MigrateLegacyBranch(ctx, pgdb.MigrateLegacyBranchParams{
					AppID: store.ToPgUUID(app.Id),
					Name:  branch.BranchName,
				})
				if err != nil {
					log.Printf("Error inserting branch '%s' for app '%s': %v", branch.BranchName, app.Id, err)
					return err
				}

				branchNameToBranchId[branch.BranchName] = branchId

				// Fetch runtime versions associated *specifically* with this branch context
				runtimeVersionsForBranch, err := branchStore.GetRuntimeVersionsWithUpdateStats(ctx, app.Id, branch.BranchName)
				if err != nil {
					log.Printf("Error fetching runtime versions for branch '%s' of app '%s': %v", branch.BranchName, app.Id, err)
					return err
				}

				for _, rv := range runtimeVersionsForBranch {
					if !insertedRuntimeVersions[rv.RuntimeVersion] {
						rvParams := pgdb.MigrateLegacyRuntimeVersionParams{
							AppID:     store.ToPgUUID(app.Id),
							Version:   rv.RuntimeVersion,
							CreatedAt: parseRFC3339ToTz(rv.CreatedAt, "createdAt"),
							UpdatedAt: parseRFC3339ToTz(rv.LastUpdatedAt, "updatedAt"),
						}
						if err := qtx.MigrateLegacyRuntimeVersion(ctx, rvParams); err != nil {
							log.Printf("Error inserting runtime version '%s' for app '%s': %v", rv.RuntimeVersion, app.Id, err)
							return err
						}
						insertedRuntimeVersions[rv.RuntimeVersion] = true
					}

					updatesForRV := make([]types.UpdateItem, 0)
					var cursor *int64
					for {
						page, err := updateStore.GetUpdatesByRunTimeVersionAndBranchName(ctx, app.Id, rv.RuntimeVersion, branch.BranchName, cursor, 100)
						if err != nil {
							log.Printf("Error fetching updates for runtime version '%s' and branch '%s' of app '%s': %v", rv.RuntimeVersion, branch.BranchName, app.Id, err)
							return err
						}
						updatesForRV = append(updatesForRV, page.Items...)
						if page.NextCursor == nil {
							break
						}
						nextCursor, err := strconv.ParseInt(*page.NextCursor, 10, 64)
						if err != nil {
							return fmt.Errorf("malformed update cursor '%s': %w", *page.NextCursor, err)
						}
						cursor = &nextCursor
					}

					for _, update := range updatesForRV {
						updateIdInt, err := strconv.ParseInt(update.UpdateId, 10, 64)
						if err != nil {
							log.Printf("Malformed update ID '%s': %v", update.UpdateId, err)
							return err
						}
						updateType, err := updateStore.GetUpdateType(ctx, types.Update{
							AppId:          app.Id,
							Branch:         branch.BranchName,
							RuntimeVersion: rv.RuntimeVersion,
							UpdateId:       update.UpdateId,
						})
						if err != nil {
							log.Printf("Error fetching update type for update '%s' of app '%s': %v", update.UpdateId, app.Id, err)
							return err
						}

						var messagePtr *string
						if update.Message != "" {
							localMsg := update.Message
							messagePtr = &localMsg
						}

						checkedAtTime := update2.GetUpdateCheckStatus(types.Update{
							AppId:          app.Id,
							Branch:         branch.BranchName,
							RuntimeVersion: rv.RuntimeVersion,
							UpdateId:       update.UpdateId,
						})

						updateParams := pgdb.MigrateLegacyUpdateParams{
							ID:         updateIdInt,
							AppID:      store.ToPgUUID(app.Id),
							Name:       branch.BranchName,
							Version:    rv.RuntimeVersion,
							UpdateType: int32(updateType),
							Platform:   update.Platform,
							CommitHash: update.CommitHash,
							Message:    messagePtr,
							CheckedAt:  toTimestamptz(checkedAtTime),
							UpdateUuid: store.ToPgUUID(update.UpdateUUID),
							CreatedAt:  parseRFC3339ToTz(update.CreatedAt, "createdAt"),
						}

						if err := qtx.MigrateLegacyUpdate(ctx, updateParams); err != nil {
							log.Printf("Error inserting update '%s' for app '%s': %v", update.UpdateId, app.Id, err)
							return err
						}
					}
				}
			}

			channels, err := channelStore.GetChannels(ctx, app.Id)
			if err != nil {
				log.Printf("Error fetching channels for app '%s': %v", app.Id, err)
				return err
			}

			for _, channel := range channels {
				// Resolve via the channel's own branch, not a branch->channel map:
				// BucketBranchStore.GetBranches keeps only the first channel of
				// each branch, so every further channel sharing that branch would
				// migrate with a NULL branch_id and silently stop serving updates.
				var branchIDPtr *int64
				if channel.BranchName != nil {
					if id, exists := branchNameToBranchId[*channel.BranchName]; exists {
						allocatedID := id
						branchIDPtr = &allocatedID
					}
				}

				channelParams := pgdb.MigrateLegacyChannelParams{
					AppID:    store.ToPgUUID(app.Id),
					Name:     channel.ReleaseChannelName,
					BranchID: branchIDPtr,
				}

				if err := qtx.MigrateLegacyChannel(ctx, channelParams); err != nil {
					log.Printf("Error inserting channel '%s' for app '%s': %v", channel.ReleaseChannelName, app.Id, err)
					return err
				}
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	log.Println("🎉 [DATABASE] Successfully migrated infrastructure from env/bucket to the database.")
	return nil
}

func DownMigrateEnvJSON(ctx context.Context, tx *sql.Tx) error {
	return nil
}
