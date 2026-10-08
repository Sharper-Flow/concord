package store

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// workCollectionCursor locates a complete-item prefix ordered by recorded_at
// descending and identity ascending. The agent boundary authenticates it and
// binds it to the source watermark before a continuation reaches the store.
type workCollectionCursor struct {
	Version    int    `json:"v"`
	WorkID     string `json:"work"`
	Collection string `json:"collection"`
	RecordedAt string `json:"recorded_at"`
	ID         string `json:"id"`
}

func decodeWorkCollectionCursor(raw, work, collection string) (workCollectionCursor, error) {
	if raw == "" {
		return workCollectionCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	var cursor workCollectionCursor
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.WorkID != work || cursor.Collection != collection || len(cursor.ID) < 2 || len(cursor.ID) > 128 {
		return cursor, newFailure(KindInvalidCursor, "work_collection", "cursor does not match the work collection", false, "restart_query")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.RecordedAt); err != nil {
		return cursor, newFailure(KindInvalidCursor, "work_collection", "cursor ordering timestamp is invalid", false, "restart_query")
	}
	return cursor, nil
}

func encodeWorkCollectionCursor(work, collection, recordedAt, id string) (string, error) {
	raw, err := json.Marshal(workCollectionCursor{Version: 1, WorkID: work, Collection: collection, RecordedAt: recordedAt, ID: id})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
