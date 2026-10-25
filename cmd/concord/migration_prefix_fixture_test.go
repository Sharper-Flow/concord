package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// The overlay exposes package-private migration selection to this test only.
// It compiles unchanged production open/migrate code, including migration
// callbacks, checksums and compatibility-floor writes. No schema is reconstructed.
const cliMigrationPrefixSource = `package store
import (
 "context"
 "os"
 "strconv"
 "testing"
)
func TestCLISeedMigrationPrefix(t *testing.T) {
 target, err := strconv.Atoi(os.Getenv("CONCORD_FIXTURE_PREFIX"))
 if err != nil { t.Fatal(err) }
 if target < 1 || target > CurrentSchemaVersion() { t.Fatal("invalid migration prefix") }
 for i, m := range migrations {
  if m.Version != i+1 { t.Fatalf("incomplete migration manifest at %d: %d", i+1, m.Version) }
 }
 migrations = migrations[:target]
 ctx := context.Background()
 s, err := openUnmigrated(ctx, os.Getenv("CONCORD_FIXTURE_PATH"))
 if err != nil { t.Fatal(err) }
 defer s.Close()
 if err := Migrate(ctx, s.db); err != nil { t.Fatal(err) }
 var count, top, floor int
 if err := s.db.QueryRow("SELECT count(*),max(version) FROM schema_migrations").Scan(&count,&top); err != nil { t.Fatal(err) }
 if count != target || top != target { t.Fatalf("incomplete prefix: count=%d top=%d",count,top) }
 if err := s.db.QueryRow("SELECT coalesce(max(version),0) FROM schema_migrations WHERE breaking=1").Scan(&floor); err != nil { t.Fatal(err) }
 if err := s.checkBinaryCompatibility(ctx); err != nil { t.Fatal(err) }
 t.Logf("production migration prefix=%d floor=%d",top,floor)
}
`

var cliPrefixBinary struct {
	sync.Once
	raw []byte
	err error
}

func seedCLIMigrationPrefix(t *testing.T, path string, target int) {
	t.Helper()
	if target < 1 || target > store.CurrentSchemaVersion() {
		t.Fatalf("invalid migration prefix %d", target)
	}
	cliPrefixBinary.Do(func() {
		scratch, err := os.MkdirTemp("", "concord-cli-prefix-")
		if err != nil {
			cliPrefixBinary.err = err
			return
		}
		defer os.RemoveAll(scratch)
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			cliPrefixBinary.err = fmt.Errorf("cannot locate prefix fixture source")
			return
		}
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		source := filepath.Join(scratch, "prefix_test.go")
		if err := os.WriteFile(source, []byte(cliMigrationPrefixSource), 0o600); err != nil {
			cliPrefixBinary.err = err
			return
		}
		// Compile only the seeder test, not unrelated package test fixtures.
		replacements := map[string]string{filepath.Join(root, "internal", "store", "cli_prefix_fixture_test.go"): source}
		tests, err := filepath.Glob(filepath.Join(root, "internal", "store", "*_test.go"))
		if err != nil {
			cliPrefixBinary.err = err
			return
		}
		for _, test := range tests {
			replacements[test] = ""
		}
		overlay, err := json.Marshal(map[string]any{"Replace": replacements})
		if err != nil {
			cliPrefixBinary.err = err
			return
		}
		overlayPath := filepath.Join(scratch, "overlay.json")
		if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
			cliPrefixBinary.err = err
			return
		}
		binary := filepath.Join(scratch, "prefix.test")
		build := exec.Command("go", "test", "-c", "-overlay", overlayPath, "-o", binary, "./internal/store")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			cliPrefixBinary.err = fmt.Errorf("compile production prefix seeder: %w: %s", err, output)
			return
		}
		cliPrefixBinary.raw, cliPrefixBinary.err = os.ReadFile(binary)
	})
	if cliPrefixBinary.err != nil {
		t.Fatal(cliPrefixBinary.err)
	}
	binary := filepath.Join(t.TempDir(), "prefix.test")
	if err := os.WriteFile(binary, cliPrefixBinary.raw, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := exec.Command(binary, "-test.run=^TestCLISeedMigrationPrefix$", "-test.timeout=60s", "-test.v")
	seed.Env = append(os.Environ(), "CONCORD_FIXTURE_PATH="+path, "CONCORD_FIXTURE_PREFIX="+strconv.Itoa(target))
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed production prefix %d: %v: %s", target, err, output)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != store.CurrentSchemaVersion()-target {
		t.Fatalf("incomplete pending tail from %d: %+v", target, plan.Pending)
	}
	for i, step := range plan.Pending {
		if step.Version != target+i+1 {
			t.Fatalf("pending tail skipped migration %d: %+v", target+i+1, plan.Pending)
		}
	}
}
