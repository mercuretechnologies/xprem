package types

import (
	"encoding/base64"
	"encoding/json"
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
	return &cursor, nil
}

func EncodeUpdateFeedCursor(item UpdateFeedItem) string {
	// A non-numeric update id encodes as 0; it never happens for rows the
	// feed itself produced.
	updateID, _ := strconv.ParseInt(item.UpdateId, 10, 64)
	encoded, _ := json.Marshal(UpdateFeedCursor{
		CreatedAt: item.FeedCreatedAt,
		BranchID:  item.BranchID,
		UpdateID:  updateID,
	})
	return base64.RawURLEncoding.EncodeToString(encoded)
}
