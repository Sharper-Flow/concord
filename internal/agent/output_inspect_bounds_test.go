package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The Inspect file mode publishes at most the default inspection bound of
// 16384 bytes of content. The producer reads one sentinel byte past that
// bound to detect truncation; the sentinel itself must never reach the
// published result. The real dispatch output validation pins content to the
// same maximum, so a producer that publishes the sentinel fails the
// operation instead of returning success with truncated content.

const inspectBoundsPublishedCap = 16384

// inspectBoundsInvoke dispatches one worktree_inspect read and returns the
// dispatch error to the caller, so an output-validation refusal surfaces as
// this test's failure rather than a fixture Fatal.
func inspectBoundsInvoke(t *testing.T, s *store.Store, service *Service, grant Authority, mode, path string) (agentcontract.Envelope, error) {
	t.Helper()
	input := map[string]any{"work_id": "work-2", "mode": mode}
	if path != "" {
		input["path"] = path
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	request := InvokeRequest{Tool: "concord_work_browse", Operation: "worktree_inspect", Input: raw}
	return Dispatch(context.Background(), s, service, request, mutationEnvelope(grant, scopeVersion))
}

func inspectBoundsPayload(t *testing.T, response agentcontract.Envelope) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatalf("inspect result=%s", response.Result)
	}
	return payload
}

func TestWorktreeInspectFileModeTruncatesSentinelByteAtPublishedCap(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")
	// One byte past the bound: the last byte is the sentinel the producer
	// must use only to set truncated, never publish.
	body := strings.Repeat("a", inspectBoundsPublishedCap) + "Z"
	if err := os.WriteFile(filepath.Join(worktreePath, "inspect-bounds.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	response, dispatchErr := inspectBoundsInvoke(t, s, service, grant, "file", "inspect-bounds.txt")
	if dispatchErr != nil {
		t.Fatalf("worktree_inspect file mode refused by output validation before any success outcome: %v", dispatchErr)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("response=%+v err=%+v, want success under the real output validation", response, response.Error)
	}
	payload := inspectBoundsPayload(t, response)
	truncated, _ := payload["truncated"].(bool)
	if !truncated {
		t.Fatalf("truncated=%v, want true for a %d-byte file at cap %d", payload["truncated"], len(body), inspectBoundsPublishedCap)
	}
	content, _ := payload["content"].(string)
	if content != strings.Repeat("a", inspectBoundsPublishedCap) {
		t.Fatalf("content length=%d, want exactly the first %d bytes with the sentinel byte withheld", len(content), inspectBoundsPublishedCap)
	}
	if strings.Contains(content, "Z") {
		t.Fatalf("content contains the truncation sentinel byte; only the first %d bytes may publish", inspectBoundsPublishedCap)
	}
}

func TestWorktreeInspectFileModeAtPublishedCapReturnsWholeFile(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")
	body := strings.Repeat("a", inspectBoundsPublishedCap)
	if err := os.WriteFile(filepath.Join(worktreePath, "inspect-exact.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	response, dispatchErr := inspectBoundsInvoke(t, s, service, grant, "file", "inspect-exact.txt")
	if dispatchErr != nil {
		t.Fatalf("worktree_inspect file mode refused by output validation: %v", dispatchErr)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("response=%+v err=%+v, want success", response, response.Error)
	}
	payload := inspectBoundsPayload(t, response)
	truncated, _ := payload["truncated"].(bool)
	if truncated {
		t.Fatalf("truncated=true, want false for a file exactly at the %d-byte cap", inspectBoundsPublishedCap)
	}
	content, _ := payload["content"].(string)
	if content != body {
		t.Fatalf("content length=%d, want the whole %d-byte file", len(content), inspectBoundsPublishedCap)
	}
}
