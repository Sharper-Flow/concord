package store

import (
	"strings"
	"testing"
)

func TestProjectToolingEmptyManifestRefuses(t *testing.T) {
	if _, err := ParseProjectToolingManifest([]byte(`{"schema_version":"1.0","project":"demo","tools":[]}`)); err == nil || !strings.Contains(err.Error(), "declares no tools") {
		t.Fatalf("empty tooling manifest was accepted: %v", err)
	}
}
