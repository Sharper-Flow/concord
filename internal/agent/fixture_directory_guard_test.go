package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/gittest"
)

func TestAgentFixtureHelpersRefuseInvalidDirectories(t *testing.T) {
	if helper := os.Getenv("TEST_CONCORD_FIXTURE_HELPER"); helper != "" {
		dir := os.Getenv("TEST_CONCORD_FIXTURE_DIR")
		switch helper {
		case "git-run":
			gitRun(t, dir, "remote", "remove", "origin")
		case "claim-subject":
			fixtureClaimSubject(t, dir)
		default:
			t.Fatalf("unknown helper %q", helper)
		}
		return
	}
	for _, helper := range []string{"git-run", "claim-subject"} {
		for _, dir := range []string{"", ".", "..", "sub"} {
			t.Run(fmt.Sprintf("%s/%q", helper, dir), func(t *testing.T) {
				outer := t.TempDir()
				for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Fixture"}, {"commit", "--allow-empty", "-qm", "seed"}, {"remote", "add", "origin", "https://example.invalid/repo"}} {
					cmd, err := gittest.Command(outer, args...)
					if err != nil {
						t.Fatal(err)
					}
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("setup: %v\n%s", err, out)
					}
				}
				sub := filepath.Join(outer, "sub")
				if err := os.Mkdir(sub, 0o755); err != nil {
					t.Fatal(err)
				}
				before := gittest.SnapshotTree(t, outer)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentFixtureHelpersRefuseInvalidDirectories$")
				child.WaitDelay = time.Second
				child.Dir = outer
				if dir == ".." {
					child.Dir = sub
				}
				child.Env = append(os.Environ(), "TEST_CONCORD_FIXTURE_HELPER="+helper, "TEST_CONCORD_FIXTURE_DIR="+dir)
				output, err := child.CombinedOutput()
				if after := gittest.SnapshotTree(t, outer); !reflect.DeepEqual(before, after) {
					t.Errorf("invalid fixture path changed outer repository: %v\n%s", err, output)
				}
				if err == nil || !strings.Contains(string(output), "test git requires an absolute fixture directory") {
					t.Errorf("invalid fixture path was not refused by guard: %v\n%s", err, output)
				}
			})
		}
	}
}
