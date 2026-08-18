package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// SignedCursor is an authenticated, operation-bound continuation token.
// Cursor signing uses the installation key owned by the authority.
type SignedCursor struct {
	Version   int    `json:"v"`
	Tool      string `json:"tool"`
	Operation string `json:"operation"`
	Scope     string `json:"scope"`
	Filter    string `json:"filter"`
	Detail    string `json:"detail"`
	Order     string `json:"order"`
	Source    string `json:"source"`
	Last      string `json:"last"`
	Inner     string `json:"inner"`
}

// EncodeCursor authenticates a cursor without exposing the authority database
// handle to callers.
func (s *Store) EncodeCursor(ctx context.Context, cursor SignedCursor) (string, error) {
	key, err := s.cursorKey(ctx)
	if err != nil {
		return "", err
	}
	cursor.Version = 1
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// DecodeCursor authenticates and validates a cursor against its query binding.
func (s *Store) DecodeCursor(ctx context.Context, token string, expected SignedCursor) (SignedCursor, error) {
	key, err := s.cursorKey(ctx)
	if err != nil {
		return SignedCursor{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return SignedCursor{}, newFailure(KindInvalidCursor, "cursor_decode", "cursor encoding is invalid", false, "restart_query")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return SignedCursor{}, newFailure(KindInvalidCursor, "cursor_decode", "cursor encoding is invalid", false, "restart_query")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return SignedCursor{}, newFailure(KindInvalidCursor, "cursor_decode", "cursor signature is invalid", false, "restart_query")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return SignedCursor{}, newFailure(KindInvalidCursor, "cursor_decode", "cursor authentication failed", false, "restart_query")
	}
	var cursor SignedCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.Version != 1 || cursor.Tool != expected.Tool || cursor.Operation != expected.Operation || cursor.Scope != expected.Scope || cursor.Filter != expected.Filter || cursor.Detail != expected.Detail || cursor.Order != expected.Order || (expected.Source != "" && cursor.Source != expected.Source) {
		return SignedCursor{}, newFailure(KindInvalidCursor, "cursor_decode", "cursor is bound to a different query", false, "restart_query")
	}
	return cursor, nil
}

func (s *Store) cursorKey(ctx context.Context) ([]byte, error) {
	if s == nil {
		return InstallationKey(ctx, nil)
	}
	return InstallationKey(ctx, s.db)
}
