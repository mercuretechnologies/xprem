package bucket

// validatingBucket is a decorator around any Bucket implementation that
// validates every user-supplied identifier (branch, runtimeVersion, updateId,
// fileName, assetPath, migrationId) before delegating, rejecting values that
// contain path separators, "..", or that are empty. Mounted once in
// GetBucket(); concrete backends still assume inputs are clean but this layer
// guarantees that assumption even if a handler forgets to sanitize.
type validatingBucket struct {
	Inner Bucket
}

// UnwrapBucket returns the underlying concrete backend when b is a
// validatingBucket decorator. External packages (the migration) need this
// so they can type-assert on *LocalBucket / *S3Bucket / *GCSBucket to
// call MoveRootEntriesUnder without going through the validating
// middleware (which would reject the root-level listing).
func UnwrapBucket(b Bucket) Bucket {
	if vb, ok := b.(*validatingBucket); ok {
		return vb.Inner
	}
	return b
}
