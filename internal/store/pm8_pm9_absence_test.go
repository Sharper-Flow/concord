package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// allowedByteColumns is the complete set of byte-typed columns the schema is
// permitted to declare. All three hold fixed-size authority material rather
// than stored content: an Ed25519 public key, a grant hash, and a 32-byte
// installation key whose length the schema itself constrains.
//
// The list is exhaustive on purpose. PM8 forbids a content-addressed evidence
// or blob store, and a byte store needs somewhere to put bytes, so a new
// byte-typed column is the structural signature of one. Whoever adds the next
// BLOB column has to state here why it is not the store PM8 refuses.
var allowedByteColumns = map[string]string{
	"agent_client_keys.public_key": "Ed25519 public key for client assertion verification",
	"agent_grants.grant_hash":      "hash of an issued grant token, never the token",

	"agent_installation_keys.key_bytes": "32-byte installation key, length-checked by the schema",
}

// forbiddenReceiptShapes names what a separate process-exhaust store, an
// audit-receipt object, or a deletion attestation would be called. PM9 holds
// that the existing durable sequence is the only receipt.
var forbiddenReceiptShapes = []string{"receipt", "attestation", "salvage"}

// TestPM8AndPM9DeclareNoEvidenceOrReceiptStore proves two negative contracts
// structurally. Both PM8 and PM9 assert a store does not exist, which a running
// system can falsify by containing one, so neither claim is unmeasurable.
//
// The proof reads the live schema and the closed event registry rather than the
// prose. PM8 is proved by the byte-column allow-list, because storing content
// requires a place to store it. PM9 is proved by name, because a receipt or
// attestation has to be addressable to be useful.
func TestPM8AndPM9DeclareNoEvidenceOrReceiptStore(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	tables := userTables(ctx, t, s)
	if len(tables) == 0 {
		t.Fatal("schema declares no tables, so the probe would pass vacuously")
	}

	// PM8: every byte-typed column must be a declared exception.
	var found []string
	for _, table := range tables {
		for _, column := range byteColumns(ctx, t, s, table) {
			found = append(found, table+"."+column)
		}
	}
	sort.Strings(found)
	for _, qualified := range found {
		if _, allowed := allowedByteColumns[qualified]; !allowed {
			t.Errorf("column %q stores bytes and is not a declared exception; PM8 declares no evidence or blob store in v1. Add it to allowedByteColumns with the reason it is not stored content, or do not store bytes.", qualified)
		}
	}
	if len(found) == 0 {
		t.Fatal("no byte-typed columns found at all; the allow-list probe would pass vacuously")
	}

	// PM9: no table or event kind is shaped like a receipt.
	for _, table := range tables {
		for _, shape := range forbiddenReceiptShapes {
			if strings.Contains(strings.ToLower(table), shape) {
				t.Errorf("table %q matches the PM9 receipt shape %q; PM9 declares no separate process-exhaust store, audit-receipt object, or deletion attestation in v1", table, shape)
			}
		}
	}
	if len(eventKindRegistry) == 0 {
		t.Fatal("event kind registry is empty, so the probe would pass vacuously")
	}
	for kind := range eventKindRegistry {
		for _, shape := range forbiddenReceiptShapes {
			if strings.Contains(strings.ToLower(kind), shape) {
				t.Errorf("event kind %q matches the PM9 receipt shape %q; PM9 declares no deletion-attestation event", kind, shape)
			}
		}
	}
}

func userTables(ctx context.Context, t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DatabaseForTesting().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("enumerate tables: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	return names
}

func byteColumns(ctx context.Context, t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.DatabaseForTesting().QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if strings.EqualFold(strings.TrimSpace(columnType), "BLOB") {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return names
}
