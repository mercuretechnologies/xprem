package services

import (
	"context"
	"expo-open-ota/internal/store"
	"time"
)

// fakeRefreshTokenRepo is an in-memory rotation ledger. It deliberately mirrors
// the real store's semantics rather than a convenient subset: the claim is
// single-use and atomic with writing the successor, and UsedRecently is decided
// against the same clock that stamped UsedAt. A fake more permissive than the
// SQL it stands for would make every service test above it lie.
type fakeRefreshTokenRepo struct {
	tokens map[string]store.RefreshToken
	// rotateErr, when set, fails every rotation: the "an outage must not read
	// as a revocation" path.
	rotateErr error
	// insertErr, when set, fails every ledger write: the "a token must never
	// leave the server without a row backing it" path.
	insertErr error
}

func newFakeRefreshTokenRepo() *fakeRefreshTokenRepo {
	return &fakeRefreshTokenRepo{tokens: map[string]store.RefreshToken{}}
}

func (r *fakeRefreshTokenRepo) InsertRefreshToken(_ context.Context, params store.InsertRefreshTokenParameters) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.tokens[params.ID] = store.RefreshToken{
		Id:        params.ID,
		UserId:    params.UserID,
		FamilyId:  params.FamilyID,
		ExpiresAt: params.ExpiresAt,
	}
	return nil
}

func (r *fakeRefreshTokenRepo) RotateRefreshToken(_ context.Context, params store.RotateRefreshTokenParameters) (store.RefreshToken, error) {
	if r.rotateErr != nil {
		return store.RefreshToken{}, r.rotateErr
	}
	token, ok := r.tokens[params.OldID]
	if !ok || token.UsedAt != nil || !token.ExpiresAt.After(time.Now()) {
		return store.RefreshToken{}, &store.ErrResourceNotFound{Resource: "refresh token", Identifier: params.OldID}
	}
	now := time.Now()
	successorId := params.NewID
	token.UsedAt = &now
	token.ReplacedBy = &successorId
	r.tokens[params.OldID] = token
	// Same transaction as the claim in the real store: a successor always
	// exists for a token that was retired.
	r.tokens[successorId] = store.RefreshToken{
		Id:        successorId,
		UserId:    token.UserId,
		FamilyId:  token.FamilyId,
		ExpiresAt: params.ExpiresAt,
	}
	return token, nil
}

func (r *fakeRefreshTokenRepo) GetRefreshToken(_ context.Context, id string, replayGrace time.Duration) (store.RefreshToken, error) {
	token, ok := r.tokens[id]
	if !ok {
		return store.RefreshToken{}, &store.ErrResourceNotFound{Resource: "refresh token", Identifier: id}
	}
	token.UsedRecently = token.UsedAt != nil && time.Since(*token.UsedAt) <= replayGrace
	return token, nil
}

func (r *fakeRefreshTokenRepo) DeleteRefreshTokenFamily(_ context.Context, familyId string) error {
	for id, token := range r.tokens {
		if token.FamilyId == familyId {
			delete(r.tokens, id)
		}
	}
	return nil
}

func (r *fakeRefreshTokenRepo) DeleteExpiredRefreshTokens(_ context.Context, userId string) error {
	for id, token := range r.tokens {
		if token.UserId == userId && !token.ExpiresAt.After(time.Now()) {
			delete(r.tokens, id)
		}
	}
	return nil
}

// markUsedAt backdates a token's rotation, the only way a test can reach past
// the replay grace window without sleeping.
func (r *fakeRefreshTokenRepo) markUsedAt(id string, usedAt time.Time) {
	token := r.tokens[id]
	token.UsedAt = &usedAt
	r.tokens[id] = token
}
