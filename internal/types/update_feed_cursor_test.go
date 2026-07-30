package types

import (
	"encoding/base64"
	"testing"
	"time"
)

func completeItem() UpdateFeedItem {
	item := UpdateFeedItem{BranchID: 7, FeedCreatedAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	item.UpdateId = "1753876800000"
	return item
}

func TestUpdateFeedCursorRoundTrip(t *testing.T) {
	encoded, err := EncodeUpdateFeedCursor(completeItem())
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := DecodeUpdateFeedCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.BranchID != 7 || cursor.UpdateID != 1753876800000 || !cursor.CreatedAt.Equal(completeItem().FeedCreatedAt) {
		t.Fatalf("cursor did not round-trip: %+v", cursor)
	}
}

func TestDecodeUpdateFeedCursorRejectsIncomplete(t *testing.T) {
	// An empty cursor is not an error: it means "first page".
	cursor, err := DecodeUpdateFeedCursor("")
	if err != nil || cursor != nil {
		t.Fatalf("an empty cursor must read as no cursor, got %+v %v", cursor, err)
	}

	for name, raw := range map[string]string{
		"not base64":        "not base64!!",
		"not json":          base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"empty object":      base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
		"missing ids":       base64.RawURLEncoding.EncodeToString([]byte(`{"createdAt":"2026-07-30T12:00:00Z"}`)),
		"zero branch id":    base64.RawURLEncoding.EncodeToString([]byte(`{"createdAt":"2026-07-30T12:00:00Z","branchId":0,"updateId":1}`)),
		"negative id":       base64.RawURLEncoding.EncodeToString([]byte(`{"createdAt":"2026-07-30T12:00:00Z","branchId":1,"updateId":-3}`)),
		"missing createdAt": base64.RawURLEncoding.EncodeToString([]byte(`{"branchId":1,"updateId":2}`)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeUpdateFeedCursor(raw); err == nil {
				t.Fatal("expected a decode error rather than a cursor matching nothing")
			}
		})
	}
}

func TestEncodeUpdateFeedCursorRefusesUnaddressableRow(t *testing.T) {
	nonNumeric := completeItem()
	nonNumeric.UpdateId = "not-a-number"
	if _, err := EncodeUpdateFeedCursor(nonNumeric); err == nil {
		t.Fatal("expected a refusal rather than a cursor the decoder would reject")
	}

	noBranch := completeItem()
	noBranch.BranchID = 0
	if _, err := EncodeUpdateFeedCursor(noBranch); err == nil {
		t.Fatal("expected a refusal for a row with no branch id")
	}
}
