package types

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// UpdateFeedCursor is the opaque keyset cursor of the update feed, encoded
// base64url over JSON; every surface paging the feed shares this codec.
type UpdateFeedCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	BranchID  int64     `json:"branchId"`
	UpdateID  int64     `json:"updateId"`
}

// DecodeUpdateFeedCursor rejects anything it cannot turn into a complete
// keyset. A partial cursor would decode into zero values, which the feed query
// happily compares against and answers with an empty page: silence reading as
// "no more updates" instead of "your cursor is broken".
func DecodeUpdateFeedCursor(raw string) (*UpdateFeedCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	var cursor UpdateFeedCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return nil, err
	}
	// Both ids are positive by construction: a branch id is a serial, an
	// update id a generated timestamp.
	if cursor.CreatedAt.IsZero() || cursor.BranchID <= 0 || cursor.UpdateID <= 0 {
		return nil, errors.New("incomplete update feed cursor")
	}
	return &cursor, nil
}

// EncodeUpdateFeedCursor builds the cursor of the page ending at item. It
// refuses to encode an item it cannot address, rather than emitting a cursor
// the decoder above would reject.
func EncodeUpdateFeedCursor(item UpdateFeedItem) (string, error) {
	updateID, err := strconv.ParseInt(item.UpdateId, 10, 64)
	if err != nil {
		return "", errors.New("cannot page past update " + item.UpdateId + ": its id is not numeric")
	}
	if item.FeedCreatedAt.IsZero() || item.BranchID <= 0 || updateID <= 0 {
		return "", errors.New("cannot page past update " + item.UpdateId + ": incomplete feed row")
	}
	encoded, err := json.Marshal(UpdateFeedCursor{
		CreatedAt: item.FeedCreatedAt,
		BranchID:  item.BranchID,
		UpdateID:  updateID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}
