package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// gitVersionProbeRunner is the stub GitRunner of the version-probe tests. It
// records the argv it received and replays a canned report and error, or a
// hold that returns only once the probe context is done.
type gitVersionProbeRunner struct {
	dir  string
	args []string
	out  []byte
	err  error
	hold bool
}

func (r *gitVersionProbeRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r.dir, r.args = dir, args
	if r.hold {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.out, r.err
}

func TestIntegrationGitVersion(t *testing.T) {
	t.Run("parses reported versions", func(t *testing.T) {
		cases := []struct {
			report       string
			full         string
			major, minor int
		}{
			{report: "git version 2.38", full: "2.38", major: 2, minor: 38},
			{report: "git version 2.38.0\n", full: "2.38.0", major: 2, minor: 38},
			{report: "git version 2.53.0\n", full: "2.53.0", major: 2, minor: 53},
			{report: "git version 2.43.0.windows.1\n", full: "2.43.0.windows.1", major: 2, minor: 43},
			{report: "git version 2.39.5 (Apple Git-154)\n", full: "2.39.5 (Apple Git-154)", major: 2, minor: 39},
			{report: "git version 2.46.0.123.gabcdef1.dirty\n", full: "2.46.0.123.gabcdef1.dirty", major: 2, minor: 46},
			{report: "git version 3.0\n", full: "3.0", major: 3, minor: 0},
		}
		for _, tc := range cases {
			full, major, minor, err := parseIntegrationGitVersion(tc.report)
			if err != nil {
				t.Fatalf("parse(%q) failed: %v", tc.report, err)
			}
			if full != tc.full || major != tc.major || minor != tc.minor {
				t.Fatalf("parse(%q) = (%q, %d, %d), want (%q, %d, %d)", tc.report, full, major, minor, tc.full, tc.major, tc.minor)
			}
		}
	})

	t.Run("malformed reports refuse", func(t *testing.T) {
		for _, report := range []string{
			"",
			"   \n",
			"git",
			"git version",
			"git version ",
			"git version \n",
			"VERSION 2.38",
			"git --version 2.38",
			"git version 2\n",
			"git version abc",
			"git version (Apple Git-154)",
			"git version 2.38junk",
			"git version 2.38.1junk",
			"git version 2.99999999999999999999.0",
			"git version 2.38.99999999999999999999",
			"git version 2." + strings.Repeat("9", 5000),
			"git version 2.53.0\nnot a version report\n",
			"git version 2.53.0\nnot a version report",
			"git version 2.53.0 \n",
			"git version 2.38 junk\n",
			"git version 2.38.",
			"git version 2.38.garbage\n",
		} {
			if _, _, _, err := parseIntegrationGitVersion(report); !errors.Is(err, errNotAGitVersionReport) {
				t.Fatalf("parse(%q) error = %v, want the not-a-version-report refusal", report, err)
			}
		}
	})

	t.Run("floor and vendor suffixes through the probe", func(t *testing.T) {
		cases := []struct {
			report string
			kind   FailureKind // empty means success
			want   string
		}{
			{report: "git version 1.99\n", kind: KindInvalidOperation},
			{report: "git version 2.25.1\n", kind: KindInvalidOperation},
			{report: "git version 2.37", kind: KindInvalidOperation},
			{report: "git version 2.37.9", kind: KindInvalidOperation},
			{report: "git version junk", kind: KindMalformedResponse},
			{report: "git version 2.38", want: "2.38"},
			{report: "git version 2.38.0\n", want: "2.38.0"},
			{report: "git version 2.53.0\n", want: "2.53.0"},
			{report: "git version 2.43.0.windows.1\n", want: "2.43.0.windows.1"},
			{report: "git version 2.39.5 (Apple Git-154)\n", want: "2.39.5 (Apple Git-154)"},
			{report: "git version 2.46.0.123.gabcdef1\n", want: "2.46.0.123.gabcdef1"},
		}
		for _, tc := range cases {
			got, err := requireIntegrationGitVersion(context.Background(), &gitVersionProbeRunner{out: []byte(tc.report)}, "/repo")
			if tc.kind != "" {
				assertFailureKind(t, err, tc.kind)
				continue
			}
			if err != nil {
				t.Fatalf("probe(%q) failed: %v", tc.report, err)
			}
			if got != tc.want {
				t.Fatalf("probe(%q) = %q, want the full reported version %q", tc.report, got, tc.want)
			}
		}
	})

	t.Run("probe runs git --version through the runner", func(t *testing.T) {
		runner := &gitVersionProbeRunner{out: []byte("git version 2.53.0\n")}
		got, err := requireIntegrationGitVersion(context.Background(), runner, "/repo")
		if err != nil {
			t.Fatalf("probe failed: %v", err)
		}
		if got != "2.53.0" {
			t.Fatalf("probe returned %q, want 2.53.0", got)
		}
		if runner.dir != "/repo" || len(runner.args) != 1 || runner.args[0] != "--version" {
			t.Fatalf("probe ran %q %v, want /repo [--version]", runner.dir, runner.args)
		}
	})

	t.Run("runner failure refuses as git unreachable", func(t *testing.T) {
		_, err := requireIntegrationGitVersion(context.Background(), &gitVersionProbeRunner{err: errors.New("git: command not found")}, "/repo")
		assertFailureKind(t, err, KindGitUnreachable)
		var failure *Failure
		if !errors.As(err, &failure) || !failure.RetrySafe {
			t.Fatalf("unreachable failure = %v, want a retry-safe typed refusal", err)
		}
	})

	t.Run("fixed probe deadline refuses as timeout", func(t *testing.T) {
		original := integrationVersionProbeTimeout
		integrationVersionProbeTimeout = 20 * time.Millisecond
		t.Cleanup(func() { integrationVersionProbeTimeout = original })
		_, err := requireIntegrationGitVersion(context.Background(), &gitVersionProbeRunner{hold: true}, "/repo")
		assertFailureKind(t, err, KindTimeout)
		var failure *Failure
		if !errors.As(err, &failure) || !failure.RetrySafe {
			t.Fatalf("timeout failure = %v, want a retry-safe typed refusal", err)
		}
	})

	t.Run("five second bound", func(t *testing.T) {
		if integrationVersionProbeTimeout != 5*time.Second {
			t.Fatalf("probe bound = %v, want the fixed five-second deadline", integrationVersionProbeTimeout)
		}
	})

	t.Run("nil runner probes the real host git", func(t *testing.T) {
		got, err := requireIntegrationGitVersion(context.Background(), nil, ".")
		if err != nil {
			t.Fatalf("probe failed: %v", err)
		}
		if _, _, _, err := parseIntegrationGitVersion("git version " + got); err != nil {
			t.Fatalf("probe returned %q, want a parseable Git version: %v", got, err)
		}
	})
}
