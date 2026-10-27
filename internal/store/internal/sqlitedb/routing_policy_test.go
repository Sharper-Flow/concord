package sqlitedb

import (
	"path/filepath"
	"testing"
)

func TestRoutingPolicyManifestDigestFunction(t *testing.T) {
	t.Parallel()
	const want = "sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736"
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			db, err := Open(DSN(filepath.Join(t.TempDir(), "routing.db")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := t.Context()
			var first, second string
			if err := db.QueryRowContext(ctx, `SELECT concord_routing_policy_manifest_digest(), concord_routing_policy_manifest_digest()`).Scan(&first, &second); err != nil {
				t.Fatal(err)
			}
			if first != want || second != want {
				t.Fatalf("function returned %q and %q, want frozen digest %q", first, second, want)
			}
			if _, err := db.ExecContext(ctx, `CREATE TABLE digest_default(value TEXT NOT NULL DEFAULT (concord_routing_policy_manifest_digest()))`); err != nil {
				t.Fatal(err)
			}
			// SQLite admits function-based indexes only for deterministic functions.
			if _, err := db.ExecContext(ctx, `CREATE INDEX digest_function ON digest_default(concord_routing_policy_manifest_digest())`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO digest_default DEFAULT VALUES`); err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := db.QueryRowContext(ctx, `SELECT value FROM digest_default`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != want {
				t.Fatalf("default stored %q, want %q", stored, want)
			}
			if err := db.QueryRowContext(ctx, `SELECT concord_routing_policy_manifest_digest(?)`, "unexpected").Scan(&stored); err == nil {
				t.Fatal("zero-argument function accepted an argument")
			}
		})
	}
}
