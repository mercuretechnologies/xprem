package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"xprem/internal/helpers"
	"xprem/internal/providers/gcp"
	"xprem/internal/types"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
)

type GCSBucket struct {
	BucketName string
	KeyPrefix  string
}

func (b *GCSBucket) prefixedKey(key string) string {
	return b.KeyPrefix + key
}

func (b *GCSBucket) bucketHandle(ctx context.Context) (*storage.BucketHandle, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	client, err := gcp.GetClient()
	if err != nil {
		return nil, err
	}
	return client.Bucket(b.BucketName), nil
}

func (b *GCSBucket) DeleteUpdateFolder(appId, branch, runtimeVersion, updateId string) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	prefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", appId, branch, runtimeVersion, updateId))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	it := bh.Objects(gctx, &storage.Query{Prefix: prefix})
	var listErr error
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			listErr = fmt.Errorf("failed to list objects: %w", err)
			break
		}
		if attrs.Name == "" { // prefix entry
			continue
		}
		name := attrs.Name
		g.Go(func() error {
			if err := bh.Object(name).Delete(gctx); err != nil {
				return fmt.Errorf("failed to delete object %s: %w", name, err)
			}
			return nil
		})
	}
	// A worker error cancels gctx, which also aborts the listing above; the
	// worker error is the interesting one, so report it first.
	if err := g.Wait(); err != nil {
		return err
	}
	if listErr != nil {
		return listErr
	}
	return nil
}

func (b *GCSBucket) GetRuntimeVersions(appId string, branch string) ([]types.RuntimeVersionWithStats, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	branchPrefix := b.prefixedKey(appId + "/" + branch + "/")
	q := &storage.Query{Prefix: branchPrefix, Delimiter: "/"}
	it := bh.Objects(ctx, q)
	var runtimeVersions []types.RuntimeVersionWithStats
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list runtime versions: %w", err)
		}
		if attrs.Prefix == "" { // skip objects
			continue
		}
		rvPrefix := attrs.Prefix // e.g., [keyPrefix/]branch/runtime/
		rv := strings.TrimSuffix(strings.TrimPrefix(rvPrefix, branchPrefix), "/")

		// list update folders under this runtimeVersion
		updatesIt := bh.Objects(ctx, &storage.Query{Prefix: rvPrefix, Delimiter: "/"})
		var updateTimestamps []int64
		for {
			uattrs, err := updatesIt.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("list updates: %w", err)
			}
			if uattrs.Prefix == "" {
				continue
			}
			upd := strings.TrimSuffix(strings.TrimPrefix(uattrs.Prefix, rvPrefix), "/")
			if _, err := bh.Object(uattrs.Prefix + ".check").Attrs(ctx); err != nil {
				continue
			}
			ts, err := strconv.ParseInt(upd, 10, 64)
			if err == nil {
				updateTimestamps = append(updateTimestamps, ts)
			}
		}
		if len(updateTimestamps) == 0 {
			continue
		}
		sort.Slice(updateTimestamps, func(i, j int) bool { return updateTimestamps[i] < updateTimestamps[j] })
		runtimeVersions = append(runtimeVersions, types.RuntimeVersionWithStats{
			RuntimeVersion:  rv,
			CreatedAt:       helpers.NormalizeTimestamp(updateTimestamps[0]).Format(time.RFC3339),
			LastUpdatedAt:   helpers.NormalizeTimestamp(updateTimestamps[len(updateTimestamps)-1]).Format(time.RFC3339),
			NumberOfUpdates: len(updateTimestamps),
		})
	}
	return runtimeVersions, nil
}

func (b *GCSBucket) GetBranches(appId string) ([]string, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	appPrefix := b.prefixedKey(appId + "/")
	it := bh.Objects(ctx, &storage.Query{Prefix: appPrefix, Delimiter: "/"})
	var branches []string
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list branches: %w", err)
		}
		if attrs.Prefix == "" {
			continue
		}
		prefix := strings.TrimSuffix(strings.TrimPrefix(attrs.Prefix, appPrefix), "/")
		branches = append(branches, prefix)
	}
	return branches, nil
}

func (b *GCSBucket) GetUpdates(appId string, branch string, runtimeVersion string) ([]types.Update, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	prefix := b.prefixedKey(appId + "/" + branch + "/" + runtimeVersion + "/")
	it := bh.Objects(ctx, &storage.Query{Prefix: prefix, Delimiter: "/"})
	var updates []types.Update
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list updates: %w", err)
		}
		if attrs.Prefix == "" {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(attrs.Prefix, prefix), "/")
		if id, err := strconv.ParseInt(name, 10, 64); err == nil {
			updates = append(updates, types.Update{
				AppId:          appId,
				Branch:         branch,
				RuntimeVersion: runtimeVersion,
				UpdateId:       strconv.FormatInt(id, 10),
				CreatedAt:      helpers.NormalizeTimestampToDuration(id),
			})
		}
	}
	return updates, nil
}

func (b *GCSBucket) GetFile(update types.Update, assetPath string) (*types.BucketFile, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	key := b.prefixedKey(update.AppId + "/" + update.Branch + "/" + update.RuntimeVersion + "/" + update.UpdateId + "/" + assetPath)
	obj := bh.Object(key)
	r, err := obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}
	attrs, _ := obj.Attrs(ctx)
	var created time.Time
	if attrs != nil {
		created = attrs.Updated
	}
	return &types.BucketFile{Reader: r, CreatedAt: created}, nil
}

func (b *GCSBucket) RequestUploadUrlForFileUpdate(appId string, branch string, runtimeVersion string, updateId string, fileName string) (string, error) {
	if b.BucketName == "" {
		return "", errors.New("BucketName not set")
	}
	key := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", appId, branch, runtimeVersion, updateId, fileName))
	url, err := gcp.SignedURL(b.BucketName, key, "PUT", "", 15*time.Minute)
	if err != nil {
		return "", fmt.Errorf("error generating signed URL: %w", err)
	}
	return url, nil
}

func (b *GCSBucket) blobKey(appId, hash string) string {
	return prefixedBlobKey(b.KeyPrefix, appId, hash)
}

func (b *GCSBucket) bsDiffKey(appId, updateId, sourceUpdateId string) string {
	return b.KeyPrefix + BSDiffObjectKey(appId, updateId, sourceUpdateId)
}

func (b *GCSBucket) BlobExists(ctx context.Context, appId, hash string) (bool, error) {
	return b.objectExists(ctx, b.blobKey(appId, hash))
}

func (b *GCSBucket) GetBlob(ctx context.Context, appId, hash string) (*types.BucketFile, error) {
	return b.getObject(ctx, b.blobKey(appId, hash))
}

func (b *GCSBucket) PutBlob(ctx context.Context, appId, hash string, body io.Reader) error {
	return b.putObject(ctx, b.blobKey(appId, hash), body)
}

func (b *GCSBucket) BSDiffExists(ctx context.Context, appId, updateId, sourceUpdateId string) (bool, error) {
	return b.objectExists(ctx, b.bsDiffKey(appId, updateId, sourceUpdateId))
}

func (b *GCSBucket) GetBSDiff(ctx context.Context, appId, updateId, sourceUpdateId string) (*types.BucketFile, error) {
	return b.getObject(ctx, b.bsDiffKey(appId, updateId, sourceUpdateId))
}

func (b *GCSBucket) PutBSDiff(ctx context.Context, appId, updateId, sourceUpdateId string, body io.Reader) error {
	return b.putObject(ctx, b.bsDiffKey(appId, updateId, sourceUpdateId), body)
}

func (b *GCSBucket) objectExists(ctx context.Context, key string) (bool, error) {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return false, err
	}
	_, err = bh.Object(key).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("Attrs error: %w", err)
	}
	return true, nil
}

// getObject returns nil, nil when the key does not exist.
func (b *GCSBucket) getObject(ctx context.Context, key string) (*types.BucketFile, error) {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	obj := bh.Object(key)
	r, err := obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetObject error: %w", err)
	}
	attrs, _ := obj.Attrs(ctx)
	var created time.Time
	if attrs != nil {
		created = attrs.Updated
	}
	return &types.BucketFile{Reader: r, CreatedAt: created}, nil
}

func (b *GCSBucket) putObject(ctx context.Context, key string, body io.Reader) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	w := bh.Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (b *GCSBucket) RequestBlobUploadURL(appId, hash, _ string) (string, error) {
	if b.BucketName == "" {
		return "", errors.New("BucketName not set")
	}
	url, err := gcp.SignedURL(b.BucketName, b.blobKey(appId, hash), "PUT", "", 15*time.Minute)
	if err != nil {
		return "", fmt.Errorf("error generating signed URL: %w", err)
	}
	return url, nil
}

func (b *GCSBucket) UploadFileIntoUpdate(update types.Update, fileName string, file io.Reader) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	key := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", update.AppId, update.Branch, update.RuntimeVersion, update.UpdateId, fileName))
	w := bh.Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, file); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return nil
}

// PutObject implements the audit archive's object write.
func (b *GCSBucket) PutObject(ctx context.Context, key string, body []byte) error {
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	w := bh.Object(b.prefixedKey(key)).NewWriter(ctx)
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (b *GCSBucket) CopyFileIntoUpdate(source types.Update, target types.Update, fileName string) error {
	if b.BucketName == "" {
		return errors.New("BucketName not set")
	}
	// Bounded so a stalled provider call cannot hold up the publish response;
	// the caller treats a timed-out copy as "upload it instead".
	ctx, cancel := context.WithTimeout(context.Background(), copyFileTimeout)
	defer cancel()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	src := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", source.AppId, source.Branch, source.RuntimeVersion, source.UpdateId, fileName))
	dst := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/%s", target.AppId, target.Branch, target.RuntimeVersion, target.UpdateId, fileName))
	if _, err := bh.Object(dst).CopierFrom(bh.Object(src)).Run(ctx); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return nil
}

func (b *GCSBucket) CreateUpdateFrom(previousUpdate *types.Update, newUpdateId string) (*types.Update, error) {
	if b.BucketName == "" {
		return nil, errors.New("BucketName not set")
	}
	if previousUpdate == nil {
		return nil, errors.New("previousUpdate is nil")
	}
	if previousUpdate.UpdateId == "" {
		return nil, errors.New("previousUpdate.UpdateId is empty")
	}
	if newUpdateId == "" {
		return nil, errors.New("newUpdateId is empty")
	}
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	sourcePrefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, previousUpdate.UpdateId))
	targetPrefix := b.prefixedKey(fmt.Sprintf("%s/%s/%s/%s/", previousUpdate.AppId, previousUpdate.Branch, previousUpdate.RuntimeVersion, newUpdateId))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	it := bh.Objects(gctx, &storage.Query{Prefix: sourcePrefix})
	var listErr error
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			listErr = fmt.Errorf("failed to list objects: %w", err)
			break
		}
		if attrs.Name == "" {
			continue
		}
		relPath := strings.TrimPrefix(attrs.Name, sourcePrefix)
		if relPath == "update-metadata.json" || relPath == ".check" || strings.HasSuffix(relPath, "/") {
			continue
		}
		src := attrs.Name
		dst := targetPrefix + relPath

		g.Go(func() error {
			cop := bh.Object(dst).CopierFrom(bh.Object(src))
			if _, err := cop.Run(gctx); err != nil {
				return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
			}
			return nil
		})
	}

	// A worker error cancels gctx, which also aborts the listing above; the
	// worker error is the interesting one, so report it first.
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if listErr != nil {
		return nil, listErr
	}

	updateId, err := strconv.ParseInt(newUpdateId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("error parsing update ID: %w", err)
	}
	return &types.Update{
		AppId:          previousUpdate.AppId,
		Branch:         previousUpdate.Branch,
		RuntimeVersion: previousUpdate.RuntimeVersion,
		UpdateId:       newUpdateId,
		CreatedAt:      helpers.NormalizeTimestampToDuration(updateId),
	}, nil
}

func (b *GCSBucket) GetInstanceID() (string, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return "", err
	}
	r, err := bh.Object(b.prefixedKey(".instanceid")).NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return "", nil
		}
		return "", err
	}
	defer r.Close()
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, r); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func (b *GCSBucket) PersistInstanceID(id string) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	w := bh.Object(b.prefixedKey(".instanceid")).NewWriter(ctx)
	if _, err := w.Write([]byte(id + "\n")); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (b *GCSBucket) RetrieveMigrationHistory() ([]string, error) {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return nil, err
	}
	obj := bh.Object(b.prefixedKey(".migrationhistory"))
	r, err := obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()
	var migrations []string
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, r); err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line != "" {
			migrations = append(migrations, line)
		}
	}
	return migrations, nil
}

func (b *GCSBucket) ApplyMigration(migrationId string) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	history, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	for _, id := range history {
		if id == migrationId {
			return nil
		}
	}
	current := strings.Join(history, "\n")
	if current != "" {
		current += "\n"
	}
	data := []byte(current + migrationId + "\n")
	w := bh.Object(b.prefixedKey(".migrationhistory")).NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func (b *GCSBucket) RemoveMigrationFromHistory(migrationId string) error {
	ctx := context.Background()
	bh, err := b.bucketHandle(ctx)
	if err != nil {
		return err
	}
	history, err := b.RetrieveMigrationHistory()
	if err != nil {
		return fmt.Errorf("RetrieveMigrationHistory error: %w", err)
	}
	// If not present, nothing to do
	found := false
	var filtered []string
	for _, id := range history {
		if id == migrationId {
			found = true
			continue
		}
		filtered = append(filtered, id)
	}
	if !found {
		return nil
	}
	content := ""
	if len(filtered) > 0 {
		content = strings.Join(filtered, "\n") + "\n"
	}
	w := bh.Object(b.prefixedKey(".migrationhistory")).NewWriter(ctx)
	if _, err := w.Write([]byte(content)); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
