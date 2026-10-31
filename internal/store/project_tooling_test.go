package store

import (
	"context"
	"strings"
	"testing"
)

func TestProjectToolingEmptyManifestRefuses(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "empty-tooling")
	seedWorkCanonicalPath(t, s, t.TempDir())
	runner := refineProofFakeRunner{responses: map[string][]byte{
		"symbolic-ref refs/remotes/origin/HEAD":           []byte("refs/remotes/origin/main\n"),
		"ls-tree origin/main -- .concord/tooling.v1.json": []byte("100644 blob 1111111111111111111111111111111111111111\t.concord/tooling.v1.json\n"),
		"show origin/main:.concord/tooling.v1.json":       []byte(`{"schema_version":"1.0","project":"demo","tools":[]}`),
	}}
	if _, err := resolveWorkProjectTooling(context.Background(), s, "empty-tooling", runner); err == nil || !strings.Contains(err.Error(), "declares no tools") {
		t.Fatalf("empty tooling manifest bypassed delivery guard: %v", err)
	}
}
