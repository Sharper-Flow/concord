package sessionboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

func testSnapshot() store.SessionResumeRead {
	return store.SessionResumeRead{
		WorkID: "work-1", ProductIdentity: []string{"product-1"}, SourceVersionWatermark: 42,
		Resume: &store.WorkflowResumeRead{WorkID: "work-1", SourceVersionWatermark: 42, Version: 3, State: "intake", OpeningSteps: []string{"intake", "pick_type"}},
	}
}

func testHandoff() *store.ProjectHandoff {
	return &store.ProjectHandoff{HandoffID: "handoff-1", WorkID: "work-1", ContractVersion: 1,
		SourceProjectID: "source-1", TargetProjectID: "project-1", SourceSessionRef: "session-1",
		BoundedJob: "job", Changes: []string{}, Verification: []string{}, ArtifactRefs: []string{},
		Blockers: []string{}, NextAction: "act", State: "recorded", RecordedAt: "2026-01-01T00:00:00Z"}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func alwaysFit([]byte) (bool, error) { return true, nil }
func fitsWithin(limit int) func([]byte) (bool, error) {
	return func(raw []byte) (bool, error) { return len(raw) <= limit, nil }
}

func buildPacket(t *testing.T, s store.SessionResumeRead) ([]byte, Packet) {
	t.Helper()
	raw, err := Build("product-1", "project-1", s, alwaysFit)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(raw); err != nil {
		t.Fatal(err)
	}
	var packet Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		t.Fatal(err)
	}
	return raw, packet
}

func TestBuildMatchesPublicResumeDocument(t *testing.T) {
	s := testSnapshot()
	s.PendingProjectHandoff = testHandoff()
	raw, packet := buildPacket(t, s)
	if packet.SchemaVersion != "1.0" || packet.SessionType != "operator" || packet.SessionContractVersion != "2.0" || packet.ManifestDigest != agentcontract.ManifestDigest || packet.SourceVersionWatermark != 42 {
		t.Fatalf("packet identity = %#v", packet)
	}
	if want := mustJSON(t, s.Resume); !bytes.Equal(packet.Resume, want) {
		t.Fatalf("resume = %s, want %s", packet.Resume, want)
	}
	if want := mustJSON(t, s.PendingProjectHandoff); !bytes.Equal(packet.PendingProjectHandoff, want) {
		t.Fatalf("handoff = %s, want %s", packet.PendingProjectHandoff, want)
	}
	again, _ := buildPacket(t, s)
	if !bytes.Equal(raw, again) {
		t.Fatal("build is not deterministic")
	}
}

func TestBuildStatesWorkflowAbsenceForInstanceLessWork(t *testing.T) {
	s := testSnapshot()
	s.Resume = nil
	s.WorkflowInstanceAbsent = true
	_, packet := buildPacket(t, s)
	if packet.WorkflowInstance != store.WorkflowInstanceAbsent || string(packet.Resume) != "null" {
		t.Fatalf("absence = %#v", packet)
	}
}

func TestBuildRejectsInconsistentSnapshot(t *testing.T) {
	for name, mutate := range map[string]func(*store.SessionResumeRead){
		"product":            func(s *store.SessionResumeRead) { s.ProductIdentity = []string{"product-2"} },
		"work":               func(s *store.SessionResumeRead) { s.Resume.WorkID = "work-2" },
		"watermark":          func(s *store.SessionResumeRead) { s.Resume.SourceVersionWatermark++ },
		"missing resume":     func(s *store.SessionResumeRead) { s.Resume = nil },
		"absent with resume": func(s *store.SessionResumeRead) { s.WorkflowInstanceAbsent = true },
	} {
		t.Run(name, func(t *testing.T) {
			s := testSnapshot()
			mutate(&s)
			if _, err := Build("product-1", "project-1", s, alwaysFit); err == nil {
				t.Fatal("inconsistent snapshot accepted")
			}
		})
	}
}

func TestValidateRejectsOversizePacket(t *testing.T) {
	if err := Validate(bytes.Repeat([]byte("x"), MaxBootPacketBytes+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize err = %v", err)
	}
}

func TestValidateRejectsUnknownSessionIdentityAndManifest(t *testing.T) {
	raw, _ := buildPacket(t, testSnapshot())
	for name, mutate := range map[string]func(map[string]any){
		"schema version": func(v map[string]any) { v["schema_version"] = "9.0" },
		"session type":   func(v map[string]any) { v["session_type"] = "generic" },
		"old contract":   func(v map[string]any) { v["session_contract_version"] = "1.0" },
		"manifest":       func(v map[string]any) { v["manifest_digest"] = "sha256:" + strings.Repeat("0", 64) },
		"unknown member": func(v map[string]any) { v["surface_version"] = "9.0.0" },
		"absent resume":  func(v map[string]any) { v["workflow_instance"] = store.WorkflowInstanceAbsent },
	} {
		t.Run(name, func(t *testing.T) {
			var v map[string]any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			mutate(v)
			if err := Validate(mustJSON(t, v)); err == nil {
				t.Fatal("tampered packet accepted")
			}
		})
	}
}

func largeResume(count int) *store.WorkflowResumeRead {
	r := testSnapshot().Resume
	r.State = "running"
	r.Type = "change"
	r.OpeningSteps = nil
	for i := 0; i < count; i++ {
		r.Steps = append(r.Steps, store.WorkflowPlanStep{ID: fmt.Sprintf("step-%d", i), Required: true, After: []string{}, Reason: strings.Repeat("x", 2048)})
	}
	return r
}

func largeHandoff() *store.ProjectHandoff {
	h := testHandoff()
	h.BoundedJob = strings.Repeat("<", 4000)
	h.NextAction = strings.Repeat("<", 1000)
	for i := 0; i < 16; i++ {
		h.Changes = append(h.Changes, strings.Repeat("<", 512))
		h.Verification = append(h.Verification, strings.Repeat("<", 512))
		h.Blockers = append(h.Blockers, strings.Repeat("<", 512))
	}
	return h
}

func assertReference(t *testing.T, raw json.RawMessage, doc []byte, pointer map[string]any, path string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"projection_omitted": true, "digest": fmt.Sprintf("sha256:%x", sha256.Sum256(doc)), "total_bytes": float64(len(doc)), "read_pointer": pointer}
	if path != "" {
		want["projection_path"] = path
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reference = %#v, want %#v", got, want)
	}
}

func TestBuildReferencesLargeDocuments(t *testing.T) {
	for _, which := range []string{"resume", "handoff", "both"} {
		t.Run(which, func(t *testing.T) {
			s := testSnapshot()
			if which != "handoff" {
				s.Resume = largeResume(40)
			}
			if which != "resume" {
				s.PendingProjectHandoff = largeHandoff()
			}
			resume := mustJSON(t, s.Resume)
			if err := payloadschema.Validate("resume_read_document", resume); err != nil {
				t.Fatalf("resume fixture: %v", err)
			}
			if which != "handoff" && len(resume) <= MaxBootPacketBytes {
				t.Fatalf("resume only %d bytes", len(resume))
			}
			var handoff []byte
			if s.PendingProjectHandoff != nil {
				handoff = mustJSON(t, s.PendingProjectHandoff)
				if err := payloadschema.Validate("pending_project_handoff", handoff); err != nil {
					t.Fatalf("handoff fixture: %v", err)
				}
				if len(handoff) <= MaxBootPacketBytes {
					t.Fatalf("handoff only %d bytes", len(handoff))
				}
			}
			raw, packet := buildPacket(t, s)
			if len(raw) > MaxBootPacketBytes {
				t.Fatalf("packet size = %d", len(raw))
			}
			assertReference(t, packet.Resume, resume, map[string]any{"tool": "concord_work_trace", "operation": "resume", "input": map[string]any{"work_id": "work-1"}}, "")
			if handoff != nil {
				assertReference(t, packet.PendingProjectHandoff, handoff, map[string]any{"tool": "concord_work_browse", "operation": "list", "input": map[string]any{"product_id": "product-1", "project_ids": []any{"project-1"}, "work_ids": []any{"work-1"}, "detail": "full"}}, "items[0].pending_project_handoff")
			}
			again, _ := buildPacket(t, s)
			if !bytes.Equal(raw, again) {
				t.Fatal("reference output is not deterministic")
			}
		})
	}
}

func TestBuildKeepsMidBandPacketInline(t *testing.T) {
	for _, which := range []string{"resume", "handoff"} {
		t.Run(which, func(t *testing.T) {
			s := testSnapshot()
			s.PendingProjectHandoff = testHandoff()
			if which == "resume" {
				s.Resume = largeResume(18)
			} else {
				s.PendingProjectHandoff.BoundedJob = strings.Repeat("<", 4000)
				for i := 0; i < 4; i++ {
					s.PendingProjectHandoff.Changes = append(s.PendingProjectHandoff.Changes, strings.Repeat("<", 512))
				}
			}
			raw, packet := buildPacket(t, s)
			if len(raw) <= 32768 || len(raw) > MaxBootPacketBytes {
				t.Fatalf("packet size = %d", len(raw))
			}
			if want := mustJSON(t, s.Resume); !bytes.Equal(packet.Resume, want) {
				t.Fatal("resume did not remain byte-exact inline")
			}
			if want := mustJSON(t, s.PendingProjectHandoff); !bytes.Equal(packet.PendingProjectHandoff, want) {
				t.Fatal("handoff did not remain byte-exact inline")
			}
		})
	}
}

func TestBuildCallerBudget(t *testing.T) {
	s := testSnapshot()
	s.Resume = largeResume(18)
	s.PendingProjectHandoff = testHandoff()
	inline, _ := buildPacket(t, s)
	for _, tc := range []struct {
		name       string
		budget     int
		referenced bool
	}{
		{"exact fit", len(inline), false},
		{"one over", len(inline) - 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Build("product-1", "project-1", s, fitsWithin(tc.budget))
			if err != nil {
				t.Fatal(err)
			}
			var packet Packet
			if err := json.Unmarshal(raw, &packet); err != nil {
				t.Fatal(err)
			}
			if got := !bytes.Equal(packet.Resume, mustJSON(t, s.Resume)); got != tc.referenced {
				t.Fatalf("referenced=%v want %v", got, tc.referenced)
			}
			if len(raw) > tc.budget {
				t.Fatalf("packet size %d exceeds %d", len(raw), tc.budget)
			}
		})
	}
	for _, budget := range []int{-1, 0, 1} {
		if _, err := Build("product-1", "project-1", s, fitsWithin(budget)); err == nil {
			t.Errorf("budget %d accepted", budget)
		}
	}
	if _, err := Build("product-1", "project-1", s, nil); err == nil {
		t.Error("nil measurement accepted")
	}
	measurementErr := errors.New("measurement failed")
	for _, failAt := range []int{1, 2} {
		calls := 0
		_, err := Build("product-1", "project-1", s, func([]byte) (bool, error) {
			calls++
			if calls == failAt {
				return false, measurementErr
			}
			return false, nil
		})
		if !errors.Is(err, measurementErr) {
			t.Errorf("measurement %d: got %v", failAt, err)
		}
	}
}

func TestValidateRejectsTamperedReferenceIdentity(t *testing.T) {
	s := testSnapshot()
	s.Resume = largeResume(40)
	s.PendingProjectHandoff = largeHandoff()
	raw, _ := buildPacket(t, s)
	for _, tc := range []struct {
		doc, key string
		value    any
	}{
		{"resume", "work_id", "work-2"},
		{"pending_project_handoff", "product_id", "product-2"},
		{"pending_project_handoff", "project_ids", []string{"project-2"}},
		{"pending_project_handoff", "work_ids", []string{"work-2"}},
	} {
		t.Run(tc.doc+"/"+tc.key, func(t *testing.T) {
			var v map[string]any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			doc := v[tc.doc].(map[string]any)
			pointer := doc["read_pointer"].(map[string]any)
			input := pointer["input"].(map[string]any)
			input[tc.key] = tc.value
			if err := Validate(mustJSON(t, v)); err == nil {
				t.Fatal("tampered reference accepted")
			}
		})
	}
}
