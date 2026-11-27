package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBareConcordPrintsUsageWithoutAuthority(t *testing.T) {
	database := filepath.Join(t.TempDir(), "authority", "concord.db")
	t.Setenv(dbOverrideEnv, database)
	var diagnostic bytes.Buffer
	if code := runWithInput(nil, strings.NewReader(""), &bytes.Buffer{}, &diagnostic); code != 2 {
		t.Fatalf("bare concord exit=%d, want 2; stderr=%q", code, diagnostic.String())
	}
	if !strings.HasPrefix(diagnostic.String(), "Usage:\n") {
		t.Fatalf("bare concord stderr=%q, want usage", diagnostic.String())
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatalf("bare concord created authority directory: %v", err)
	}
}

func TestRemovedLauncherVerbPrintsUsageWithoutOpeningAuthority(t *testing.T) {
	database := filepath.Join(t.TempDir(), "authority", "concord.db")
	t.Setenv(dbOverrideEnv, database)
	for _, args := range [][]string{{"launcher"}, {"launcher", "--list"}, {"launcher", "--resume-last"}} {
		var out, diagnostic bytes.Buffer
		if code := runWithInput(args, strings.NewReader("not JSON"), &out, &diagnostic); code != 2 {
			t.Fatalf("%v exit=%d, want 2; stderr=%q", args, code, diagnostic.String())
		}
		if out.Len() != 0 || !strings.Contains(diagnostic.String(), "unsupported arguments:") || !strings.Contains(diagnostic.String(), "Usage:") {
			t.Fatalf("%v stdout=%q stderr=%q", args, out.String(), diagnostic.String())
		}
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatalf("removed verb created authority directory: %v", err)
	}
	var usage bytes.Buffer
	writeUsage(&usage)
	if strings.Contains(usage.String(), "concord launcher") {
		t.Fatal("help still advertises the deleted launcher")
	}
}

// The CLI request stdin carries the invoke request whose input holds a
// dispatch packet with the approved 64 KiB context aggregate beside its other
// members, so a request above the 64 KiB response/envelope era passes the
// input bound and fails on its own merits instead. The bound refuses whole
// with its diagnostic; it never truncates.
func TestRequestInputLimitAdmitsRequestCapacity(t *testing.T) {
	database := filepath.Join(t.TempDir(), "authority", "concord.db")
	t.Setenv(dbOverrideEnv, database)
	envelope := `{"schema_version":"1.0","request_id":"request-input-limit","client_ref":"client/concord-1","principal_ref":"principal/operator","session_ref":"session/s","agent_ref":"agent/a","directory":"/tmp/d","worktree":"/tmp/w","ambient_project_id":"project-1","scope_version":"sha256:0000000000000000000000000000000000000000000000000000000000000000","manifest_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}`
	body := []byte(`{"call_envelope":` + envelope + `,"tool":"concord_work_transition","operation":"workflow_action","input":{"work_id":"work-input-limit","expected_version":1,"action_id":"dispatch_worker","idempotency_key":"idemp-input-limit","fields":{"attempt_id":"attempt-input-limit","filler":"` +
		strings.Repeat("x", 80*1024) + `"}}}`)
	var out, diagnostic bytes.Buffer
	code := runWithInput([]string{"invoke"}, bytes.NewReader(body), &out, &diagnostic)
	if strings.Contains(diagnostic.String(), "input exceeds") {
		t.Fatalf("the request input bound fired inside its own capacity: %q", diagnostic.String())
	}
	if code != 0 || !strings.Contains(out.String(), `"outcome"`) {
		t.Fatalf("the oversized-but-bounded request never reached the core: exit=%d stdout=%q stderr=%q", code, out.String(), diagnostic.String())
	}
}

func TestStdinReadFailuresAndLimits(t *testing.T) {
	readError := errors.New("synthetic read failure")
	for _, tc := range []struct {
		name    string
		command string
		input   func() io.Reader
		want    string
	}{
		{"invoke_read_error", "invoke", func() io.Reader {
			return io.MultiReader(strings.NewReader("{"), iotest.ErrReader(readError))
		}, "cannot read the request: synthetic read failure\n"},
		{"invoke_oversize", "invoke", func() io.Reader {
			return strings.NewReader(strings.Repeat("x", 8388609))
		}, "input exceeds 8388608 bytes\n"},
		{"upgrade_read_error", "upgrade", func() io.Reader {
			return io.MultiReader(strings.NewReader("{"), iotest.ErrReader(readError))
		}, "concord upgrade: cannot read the request: synthetic read failure\n"},
		{"upgrade_oversize", "upgrade", func() io.Reader {
			return strings.NewReader(strings.Repeat("x", 65537))
		}, "concord upgrade: input exceeds 65536 bytes\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := runWithInput([]string{tc.command}, tc.input(), &out, &diagnostic)
			if code != 1 || diagnostic.String() != tc.want || out.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit=1, empty stdout, stderr=%q", code, out.String(), diagnostic.String(), tc.want)
			}
		})
	}
}
