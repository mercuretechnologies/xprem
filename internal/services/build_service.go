package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/requestmeta"
	"xprem/internal/store"
	"xprem/internal/types"
	"xprem/internal/validation"

	"github.com/google/uuid"
)

const MaxBuildSize int64 = 2 << 30
const maxBuildDuration = 7 * 24 * time.Hour
const buildClockSkew = 5 * time.Minute

var ErrBuildConflict = errors.New("build ID already refers to different content")
var ErrBuildNotReady = errors.New("build artifact is not ready")
var ErrBuildIntegrity = errors.New("uploaded artifact size or SHA-256 does not match")
var ErrBuildState = errors.New("build is not awaiting an artifact upload")
var buildHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var fingerprintHash = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type BuildRepository interface {
	Create(context.Context, types.BuildRecord) (*types.BuildRecord, bool, error)
	Get(context.Context, string, string) (*types.BuildRecord, error)
	List(context.Context, string, int32, int32, *types.BuildCursor) ([]types.BuildRecord, int64, error)
	Transition(context.Context, string, string, func(types.BuildRecord) (*types.BuildRecord, error)) (*types.BuildRecord, error)
	CreateShare(context.Context, string, string, string, time.Time) (types.BuildShare, error)
	ListShares(context.Context, string) ([]types.BuildShare, error)
	RevokeShare(context.Context, string, string) error
	ResolveShare(context.Context, string) (*types.BuildRecord, time.Time, error)
	AppendLogs(context.Context, string, string, int32, string) error
	ListLogs(context.Context, string, string, int32) ([]types.BuildLogChunk, error)
}
type BuildService struct {
	repo        BuildRepository
	identifiers AppIdentifierRepository
	storage     bucket.Bucket
	now         func() time.Time
}

func NewBuildService(repo BuildRepository, identifiers AppIdentifierRepository, storage bucket.Bucket) *BuildService {
	return &BuildService{repo: repo, identifiers: identifiers, storage: storage, now: time.Now}
}

// BuildStartMetadata is what the CLI knows before compiling.
type BuildStartMetadata struct {
	Profile      string                `json:"profile"`
	Mode         string                `json:"mode,omitempty"`
	Distribution types.IosDistribution `json:"distribution,omitempty"`
	Environment  string                `json:"environment,omitempty"`
	Channel      string                `json:"channel,omitempty"`
	CLIVersion   string                `json:"cliVersion"`
	GitCommit    string                `json:"gitCommit,omitempty"`
	GitMessage   string                `json:"gitMessage,omitempty"`
	GitDirty     bool                  `json:"gitDirty,omitempty"`
	Machine      *types.BuildMachine   `json:"machine,omitempty"`
	StartedAt    time.Time             `json:"startedAt"`
}
type BuildStartInput struct {
	ArtifactType types.BuildArtifactType `json:"artifactType"`
	Metadata     BuildStartMetadata      `json:"metadata"`
}
type RegisterBuildInput struct {
	ArtifactType types.BuildArtifactType `json:"artifactType"`
	Size         int64                   `json:"size"`
	SHA256       string                  `json:"sha256"`
	Metadata     types.BuildMetadata     `json:"metadata"`
}
type FailBuildInput struct {
	FinishedAt time.Time `json:"finishedAt"`
}
type BuildRegistration struct {
	Build  *types.BuildRecord    `json:"build"`
	Upload *bucket.UploadRequest `json:"upload,omitempty"`
}

func validateBuildID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return validation.Errorf("buildId", "expected a canonical UUID")
	}
	return nil
}

// Postgres keeps microseconds, so inputs are normalized before they are compared with stored rows.
func normalizeTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

func (s *BuildService) validateStart(platform types.Platform, artifactType types.BuildArtifactType, m *BuildStartMetadata) error {
	artifactPlatform, err := artifactType.Platform()
	if err != nil {
		return validation.Errorf("artifactType", "%s", err)
	}
	if artifactPlatform != platform {
		return validation.Errorf("artifactType", "%q does not match identifier platform %q", artifactType, platform)
	}
	if m.Profile == "" || m.CLIVersion == "" {
		return validation.Errorf("metadata", "profile and CLI version are required")
	}
	if m.Mode != "" && m.Mode != "debug" && m.Mode != "release" {
		return validation.Errorf("metadata.mode", "expected debug or release")
	}
	switch m.Distribution {
	case "":
	case types.IosDistributionAppStore, types.IosDistributionAdHoc:
		if platform != types.PlatformIOS {
			return validation.Errorf("metadata.distribution", "only iOS builds have a distribution")
		}
	default:
		return validation.Errorf("metadata.distribution", "expected %q or %q", types.IosDistributionAppStore, types.IosDistributionAdHoc)
	}
	for _, v := range []string{m.Profile, m.Environment, m.Channel, m.CLIVersion, m.GitCommit} {
		if len(v) > 255 {
			return validation.Errorf("metadata", "field exceeds 255 bytes")
		}
	}
	if len(m.GitMessage) > 1000 {
		return validation.Errorf("metadata", "commit message exceeds 1000 bytes")
	}
	if err := validateBuildMachine(m.Machine); err != nil {
		return err
	}
	if m.StartedAt.IsZero() || m.StartedAt.After(s.now().Add(buildClockSkew)) {
		return validation.Errorf("metadata", "startedAt is required and cannot be in the future")
	}
	m.StartedAt = normalizeTime(m.StartedAt)
	return nil
}

func validateBuildMachine(machine *types.BuildMachine) error {
	if machine == nil {
		return nil
	}
	if len(machine.Tools) == 0 {
		machine.Tools = nil
	}
	if len(machine.Tools) > 16 {
		return validation.Errorf("metadata.machine", "at most 16 tools are recorded")
	}
	values := []string{machine.Hostname, machine.OS, machine.Arch, machine.Node, machine.CI}
	for name, version := range machine.Tools {
		values = append(values, name, version)
	}
	for _, v := range values {
		if len(v) > 255 {
			return validation.Errorf("metadata.machine", "field exceeds 255 bytes")
		}
	}
	if machine.CIRunURL != "" {
		parsed, err := url.Parse(machine.CIRunURL)
		if err != nil || len(machine.CIRunURL) > 2000 || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return validation.Errorf("metadata.machine", "ciRunUrl must be an http or https URL")
		}
	}
	return nil
}

func (s *BuildService) validateFinish(startedAt time.Time, finishedAt *time.Time) error {
	if finishedAt.IsZero() || finishedAt.Before(startedAt) || finishedAt.After(s.now().Add(buildClockSkew)) || finishedAt.Sub(startedAt) > maxBuildDuration {
		return validation.Errorf("finishedAt", "expected a timestamp between startedAt and now, at most 7 days after startedAt")
	}
	*finishedAt = normalizeTime(*finishedAt)
	return nil
}

func (s *BuildService) validateRegister(platform types.Platform, input *RegisterBuildInput) error {
	m := &input.Metadata
	start := BuildStartMetadata{Profile: m.Profile, Mode: m.Mode, Distribution: m.Distribution, Environment: m.Environment, Channel: m.Channel, CLIVersion: m.CLIVersion, GitCommit: m.GitCommit, GitMessage: m.GitMessage, GitDirty: m.GitDirty, Machine: m.Machine, StartedAt: m.StartedAt}
	if err := s.validateStart(platform, input.ArtifactType, &start); err != nil {
		return err
	}
	m.StartedAt = start.StartedAt
	if input.Size <= 0 || input.Size > MaxBuildSize || !buildHash.MatchString(input.SHA256) {
		return validation.Errorf("artifact", "expected a size up to 2 GiB and SHA-256 hex checksum")
	}
	if err := validation.BuildNumber(platform, m.BuildNumber); err != nil {
		return err
	}
	if platform == types.PlatformAndroid && m.BuildNumber == "0" {
		return validation.Errorf("metadata", "buildNumber must be an Android versionCode from 1 to %d", validation.MaxAndroidBuildNumber)
	}
	if !fingerprintHash.MatchString(m.Fingerprint) {
		return validation.Errorf("metadata", "fingerprint must be a 40 or 64 character hex digest")
	}
	for _, v := range []string{m.Version, m.RuntimeVersion, m.ExpoSDK} {
		if len(v) > 255 {
			return validation.Errorf("metadata", "field exceeds 255 bytes")
		}
	}
	if err := s.validateFinish(m.StartedAt, &m.FinishedAt); err != nil {
		return err
	}
	m.DurationMs = m.FinishedAt.Sub(m.StartedAt).Milliseconds()
	return nil
}

func sameInitialInputs(a, b types.BuildRecord) bool {
	x, y := a.Metadata, b.Metadata
	return a.AppIdentifierID == b.AppIdentifierID && a.ArtifactType == b.ArtifactType && x.Profile == y.Profile && x.Mode == y.Mode && x.Distribution == y.Distribution && x.Environment == y.Environment && x.Channel == y.Channel && x.CLIVersion == y.CLIVersion && x.GitCommit == y.GitCommit && x.GitMessage == y.GitMessage && x.GitDirty == y.GitDirty && reflect.DeepEqual(x.Machine, y.Machine) && x.StartedAt.Equal(y.StartedAt)
}

func sameArtifact(a, b types.BuildRecord) bool {
	x, y := a.Metadata, b.Metadata
	return a.Size == b.Size && a.SHA256 == b.SHA256 && x.BuildNumber == y.BuildNumber && x.Version == y.Version && x.Fingerprint == y.Fingerprint && x.RuntimeVersion == y.RuntimeVersion && x.ExpoSDK == y.ExpoSDK
}

func withArtifact(current, declared types.BuildRecord) *types.BuildRecord {
	next := current
	next.Status = types.BuildStatusUploading
	next.Size, next.SHA256 = declared.Size, declared.SHA256
	next.Metadata = declared.Metadata
	next.Metadata.ClientIP = current.Metadata.ClientIP
	return &next
}

func artifactRef(b types.BuildRecord) bucket.BuildArtifact {
	return bucket.BuildArtifact{IdentifierID: b.AppIdentifierID, BuildID: b.ID, Type: b.ArtifactType}
}

func (s *BuildService) newRecord(ctx context.Context, appID, identifierID, id string, artifactType types.BuildArtifactType) (*types.BuildRecord, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	if err := validateBuildID(id); err != nil {
		return nil, err
	}
	ref, err := s.identifiers.GetAppIdentifierByID(ctx, appID, identifierID)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, &store.ErrResourceNotFound{Resource: "app identifier", Identifier: identifierID}
	}
	if ref.Platform != types.PlatformAndroid && ref.Platform != types.PlatformIOS {
		return nil, validation.Errorf("platform", "unsupported build platform %q", ref.Platform)
	}
	actorType, actorID, actorDisplay := auditActorFromContext(ctx)
	record := &types.BuildRecord{ID: id, AppID: appID, AppIdentifierID: identifierID, Platform: ref.Platform, ApplicationID: ref.Identifier, ArtifactType: artifactType, ActorType: string(actorType), ActorID: actorID, ActorDisplay: actorDisplay}
	record.ArtifactKey = artifactRef(*record).Key(false)
	return record, nil
}

// Start records a build before compilation; repeating it with the same inputs returns the existing row.
func (s *BuildService) Start(ctx context.Context, appID, identifierID, id string, input BuildStartInput) (*types.BuildRecord, error) {
	record, err := s.newRecord(ctx, appID, identifierID, id, input.ArtifactType)
	if err != nil {
		return nil, err
	}
	if err := s.validateStart(record.Platform, input.ArtifactType, &input.Metadata); err != nil {
		return nil, err
	}
	m := input.Metadata
	record.Status = types.BuildStatusBuilding
	record.Metadata = types.BuildMetadata{Profile: m.Profile, Mode: m.Mode, Distribution: m.Distribution, Environment: m.Environment, Channel: m.Channel, CLIVersion: m.CLIVersion, GitCommit: m.GitCommit, GitMessage: m.GitMessage, GitDirty: m.GitDirty, Machine: m.Machine, StartedAt: m.StartedAt}
	record.Metadata.ClientIP = requestmeta.FromContext(ctx).IP
	existing, _, err := s.repo.Create(ctx, *record)
	if err != nil {
		return nil, err
	}
	if !sameInitialInputs(*existing, *record) {
		return nil, ErrBuildConflict
	}
	return existing, nil
}

// RegisterArtifact declares the compiled artifact and hands back where to upload it.
func (s *BuildService) RegisterArtifact(ctx context.Context, appID, identifierID, id string, input RegisterBuildInput) (*BuildRegistration, error) {
	record, err := s.newRecord(ctx, appID, identifierID, id, input.ArtifactType)
	if err != nil {
		return nil, err
	}
	if err := s.validateRegister(record.Platform, &input); err != nil {
		return nil, err
	}
	record.Status, record.Size, record.SHA256, record.Metadata = types.BuildStatusUploading, input.Size, input.SHA256, input.Metadata
	record.Metadata.ClientIP = requestmeta.FromContext(ctx).IP
	existing, created, err := s.repo.Create(ctx, *record)
	if err != nil {
		return nil, err
	}
	if !created {
		existing, err = s.repo.Transition(ctx, appID, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
			if !sameInitialInputs(current, *record) {
				return nil, ErrBuildConflict
			}
			switch current.Status {
			case types.BuildStatusBuilding:
				return withArtifact(current, *record), nil
			case types.BuildStatusFailed:
				// A failed row carries the failure time, so only the artifact fields are compared.
				if current.Size > 0 && !sameArtifact(current, *record) {
					return nil, ErrBuildConflict
				}
				return withArtifact(current, *record), nil
			default:
				if !sameArtifact(current, *record) || !current.Metadata.FinishedAt.Equal(record.Metadata.FinishedAt) {
					return nil, ErrBuildConflict
				}
				return nil, nil
			}
		})
		if err != nil {
			return nil, err
		}
	}
	result := &BuildRegistration{Build: existing}
	if existing.Status == types.BuildStatusReady {
		return result, nil
	}
	result.Upload, err = s.storage.RequestBuildArtifactUploadURL(ctx, existing.AppID, artifactRef(*existing))
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *BuildService) Get(ctx context.Context, appID, id string) (*types.BuildRecord, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil, validation.Errorf("buildId", "invalid UUID")
	}
	return s.repo.Get(ctx, appID, id)
}

func (s *BuildService) List(ctx context.Context, appID string, limit, offset int32, rawCursor string) (types.BuildsPage, error) {
	if s.repo == nil {
		return types.BuildsPage{}, store.ErrNotSupportedInStatelessMode
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 || (rawCursor != "" && offset != 0) {
		return types.BuildsPage{}, validation.Errorf("pagination", "invalid limit, offset, or cursor combination")
	}
	var cursor *types.BuildCursor
	if rawCursor != "" {
		if len(rawCursor) > 512 {
			return types.BuildsPage{}, validation.Errorf("cursor", "invalid build cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(rawCursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor == nil || cursor.CreatedAt.IsZero() || validateBuildID(cursor.ID) != nil {
			return types.BuildsPage{}, validation.Errorf("cursor", "invalid build cursor")
		}
	}
	// Read ahead instead of using a total that may change between requests.
	builds, count, err := s.repo.List(ctx, appID, limit+1, offset, cursor)
	if err != nil {
		return types.BuildsPage{}, err
	}
	page := types.BuildsPage{Builds: builds, Count: count}
	if len(builds) > int(limit) {
		page.Builds = builds[:limit]
		last := page.Builds[len(page.Builds)-1]
		encoded, err := json.Marshal(types.BuildCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return types.BuildsPage{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func (s *BuildService) AppendLogs(ctx context.Context, appID, identifierID, id string, offset int32, content string) error {
	if offset < 0 || len(content) == 0 || len(content) > types.MaxBuildLogChunkBytes || int64(offset)+int64(len(content)) > types.MaxBuildLogBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return validation.Errorf("logs", "expected UTF-8 output in chunks up to 32 KiB, at most 10 MiB per build")
	}
	build, err := s.Get(ctx, appID, id)
	if err != nil {
		return err
	}
	if build.AppIdentifierID != identifierID {
		return &store.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	if err := validateBuildLogEvents(content); err != nil {
		return err
	}
	return s.repo.AppendLogs(ctx, appID, id, offset, content)
}

func (s *BuildService) ListLogs(ctx context.Context, appID, id string, after int32) ([]types.BuildLogChunk, error) {
	if after < 0 || after > types.MaxBuildLogBytes {
		return nil, validation.Errorf("after", "invalid log offset")
	}
	if _, err := s.Get(ctx, appID, id); err != nil {
		return nil, err
	}
	return s.repo.ListLogs(ctx, appID, id, after)
}

func (s *BuildService) transition(ctx context.Context, appID, identifierID, id string, decide func(types.BuildRecord) (*types.BuildRecord, error)) (*types.BuildRecord, error) {
	if s.repo == nil {
		return nil, store.ErrNotSupportedInStatelessMode
	}
	if err := validateBuildID(id); err != nil {
		return nil, err
	}
	return s.repo.Transition(ctx, appID, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
		if current.AppIdentifierID != identifierID {
			return nil, &store.ErrResourceNotFound{Resource: "build", Identifier: id}
		}
		return decide(current)
	})
}

func failed(current types.BuildRecord, finishedAt time.Time) *types.BuildRecord {
	next := current
	next.Status = types.BuildStatusFailed
	next.Metadata.FinishedAt = finishedAt
	next.Metadata.DurationMs = max(finishedAt.Sub(current.Metadata.StartedAt).Milliseconds(), 0)
	return &next
}

// Fail marks an unfinished build failed; ready and already failed builds are returned unchanged.
func (s *BuildService) Fail(ctx context.Context, appID, identifierID, id string, input FailBuildInput) (*types.BuildRecord, error) {
	if input.FinishedAt.IsZero() || input.FinishedAt.After(s.now().Add(buildClockSkew)) {
		return nil, validation.Errorf("finishedAt", "expected a timestamp that is not in the future")
	}
	staged := false
	record, err := s.transition(ctx, appID, identifierID, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
		if current.Status == types.BuildStatusReady || current.Status == types.BuildStatusFailed {
			return nil, nil
		}
		if err := s.validateFinish(current.Metadata.StartedAt, &input.FinishedAt); err != nil {
			return nil, err
		}
		staged = current.Status == types.BuildStatusUploading
		return failed(current, input.FinishedAt), nil
	})
	if err == nil && staged {
		_ = s.storage.DeleteBuildArtifact(ctx, artifactRef(*record), true)
	}
	return record, err
}

func (s *BuildService) Complete(ctx context.Context, appID, identifierID, id string) (*types.BuildRecord, error) {
	uploadable := func(b types.BuildRecord) (*types.BuildRecord, error) {
		if b.Status != types.BuildStatusReady && b.Status != types.BuildStatusUploading {
			return nil, ErrBuildState
		}
		return nil, nil
	}
	staged, err := s.transition(ctx, appID, identifierID, id, uploadable)
	if err != nil || staged.Status == types.BuildStatusReady {
		return staged, err
	}
	// The artifact is verified outside the row lock so log appends are not blocked meanwhile.
	if err := s.verifyStaged(ctx, *staged); err != nil {
		if errors.Is(err, ErrBuildIntegrity) {
			_, _ = s.transition(ctx, appID, identifierID, id, func(current types.BuildRecord) (*types.BuildRecord, error) {
				if current.Status != types.BuildStatusUploading {
					return nil, nil
				}
				return failed(current, normalizeTime(s.now())), nil
			})
		}
		return nil, err
	}
	completed, err := s.transition(ctx, appID, identifierID, id, func(b types.BuildRecord) (*types.BuildRecord, error) {
		if _, err := uploadable(b); err != nil || b.Status == types.BuildStatusReady {
			return nil, err
		}
		next := b
		next.Status = types.BuildStatusReady
		return &next, nil
	})
	if err == nil {
		_ = s.storage.DeleteBuildArtifact(ctx, artifactRef(*completed), true)
	}
	return completed, err
}

func (s *BuildService) verifyStaged(ctx context.Context, b types.BuildRecord) error {
	file, err := s.storage.GetBuildArtifact(ctx, artifactRef(b), true)
	if err != nil {
		return err
	}
	if file == nil {
		return ErrBuildIntegrity
	}
	defer file.Reader.Close()
	temporary, err := os.CreateTemp("", "xprem-build-verify-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(file.Reader, b.Size+1))
	if err != nil {
		return err
	}
	if size != b.Size || hex.EncodeToString(hash.Sum(nil)) != b.SHA256 {
		return ErrBuildIntegrity
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return s.storage.PutBuildArtifact(ctx, artifactRef(b), false, temporary)
}

func (s *BuildService) Download(ctx context.Context, record types.BuildRecord) (*types.BucketFile, error) {
	if record.Status != types.BuildStatusReady {
		return nil, ErrBuildNotReady
	}
	return s.storage.GetBuildArtifact(ctx, artifactRef(record), false)
}

func (s *BuildService) DownloadURL(ctx context.Context, record types.BuildRecord, shareExpiresAt time.Time) (string, error) {
	if record.Status != types.BuildStatusReady {
		return "", ErrBuildNotReady
	}
	now := s.now()
	expiresAt := now.Add(time.Minute)
	if shareExpiresAt.Before(expiresAt) {
		expiresAt = shareExpiresAt
	}
	return s.storage.RequestBuildArtifactDownloadURL(ctx, artifactRef(record), expiresAt)
}

type countingReader struct {
	io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += int64(n)
	return n, err
}

// UploadLocal stores the body in staging; anything beyond the declared size is discarded.
func (s *BuildService) UploadLocal(ctx context.Context, appID, identifierID, id, token string, body io.Reader) error {
	if s.repo == nil {
		return store.ErrNotSupportedInStatelessMode
	}
	if err := bucket.ValidateBuildUploadToken(token, appID, identifierID, id); err != nil {
		return ErrUnauthorized
	}
	b, err := s.Get(ctx, appID, id)
	if err != nil {
		return err
	}
	if b.AppIdentifierID != identifierID {
		return &store.ErrResourceNotFound{Resource: "build", Identifier: id}
	}
	if b.Status != types.BuildStatusUploading {
		return ErrBuildState
	}
	reader := &countingReader{Reader: io.LimitReader(body, b.Size+1)}
	if err := s.storage.PutBuildArtifact(ctx, artifactRef(*b), true, reader); err != nil {
		return err
	}
	if reader.n > b.Size {
		_ = s.storage.DeleteBuildArtifact(ctx, artifactRef(*b), true)
		return ErrBuildIntegrity
	}
	return nil
}

// tokenHash hashes a bearer token so its plaintext is never needed in the database.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomSecret returns 32 cryptographically random bytes for a link token.
func randomSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func (s *BuildService) CreateShare(ctx context.Context, appID, id string, hours int) (types.BuildShare, string, error) {
	b, err := s.Get(ctx, appID, id)
	if err != nil {
		return types.BuildShare{}, "", err
	}
	if b.Status != types.BuildStatusReady || !installableFromLink(*b) {
		return types.BuildShare{}, "", validation.Errorf("build", "only ready APK and iOS Ad Hoc builds can be shared")
	}
	if b.Platform == types.PlatformIOS && !strings.HasPrefix(config.BaseURL(), "https://") {
		return types.BuildShare{}, "", validation.Errorf("build", "iPhones only install from an HTTPS server: set BASE_URL to the https address of this server")
	}
	if hours < 1 || hours > 720 {
		return types.BuildShare{}, "", validation.Errorf("expiresInHours", "must be between 1 and 720")
	}
	secret, err := randomSecret()
	if err != nil {
		return types.BuildShare{}, "", err
	}
	token := hex.EncodeToString(secret)
	share, err := s.repo.CreateShare(ctx, uuid.NewString(), id, tokenHash(token), s.now().Add(time.Duration(hours)*time.Hour))
	return share, token, err
}

// installableFromLink reports whether a phone can install the artifact directly: store bundles cannot.
func installableFromLink(b types.BuildRecord) bool {
	return b.ArtifactType == types.BuildArtifactAPK ||
		(b.ArtifactType == types.BuildArtifactIPA && b.Metadata.Distribution == types.IosDistributionAdHoc)
}

func (s *BuildService) ListShares(ctx context.Context, appID, id string) ([]types.BuildShare, error) {
	if _, err := s.Get(ctx, appID, id); err != nil {
		return nil, err
	}
	return s.repo.ListShares(ctx, id)
}

func (s *BuildService) RevokeShare(ctx context.Context, appID, id, shareID string) error {
	if _, err := s.Get(ctx, appID, id); err != nil {
		return err
	}
	if _, err := uuid.Parse(shareID); err != nil {
		return validation.Errorf("shareId", "invalid UUID")
	}
	return s.repo.RevokeShare(ctx, id, shareID)
}

// ResolveShare returns ErrResourceNotFound for unknown, expired or revoked links.
func (s *BuildService) ResolveShare(ctx context.Context, token string) (*types.BuildRecord, time.Time, error) {
	if s.repo == nil {
		return nil, time.Time{}, store.ErrNotSupportedInStatelessMode
	}
	if !buildHash.MatchString(token) {
		return nil, time.Time{}, &store.ErrResourceNotFound{Resource: "share", Identifier: "link"}
	}
	return s.repo.ResolveShare(ctx, tokenHash(token))
}
