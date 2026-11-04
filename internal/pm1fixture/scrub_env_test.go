package pm1fixture

import (
	"fmt"
	"os"
	"testing"

	"github.com/sharper-flow/concord/internal/gittest"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	if err := gittest.ScrubRepositoryEnvironment(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(testenv.Cleanup(dir, 1))
	}
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func TestGitFixtureStartupIsolation(t *testing.T) {
	gittest.TestStartupIsolation(t, func(t *testing.T) {
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		s, err := OpenTemp(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := Seed(t.Context(), s, c); err != nil {
			t.Fatal(err)
		}
		if _, err := SeedKnowledge(t.Context(), s, c, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})
}
