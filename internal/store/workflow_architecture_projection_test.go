package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The durable design projection tests: the store continuity projection
// carries the architecture-bearing design record complete, the derived
// revision and digest are per-work and content-bound, and a design without an
// architecture projects byte-identically to its released shape with the new
// members absent, never empty.

// projectionMemberProject is the one Project seedWork joins to its work item.
const projectionMemberProject = "project"

func projectionDesignContent(choice string) workflowDesignContent {
	return workflowDesignContent{
		Approach: "Projection approach",
		Decisions: []workflowDesignDecisionPayload{{
			ID: "decision:approach", Question: "Which plan?", Choice: choice, Rationale: "The typed plan reads back whole", Rejected: []string{},
		}},
		TouchedRefs: []string{"src/projection.go"},
	}
}

// projectionArchitecture projects the shared closed-plan fixture onto the
// test work's member Project without editing the shared fixture helpers.
func projectionArchitecture() WorkflowDesignArchitecture {
	plan := designArchitectureFixture()
	reproject := func(project *string) {
		if *project == "concord" {
			*project = projectionMemberProject
		}
	}
	for i := range plan.Owners {
		reproject(&plan.Owners[i].ProjectID)
	}
	for i := range plan.References {
		reproject(&plan.References[i].Source.ProjectID)
	}
	for i := range plan.Files {
		reproject(&plan.Files[i].ProjectID)
	}
	for i := range plan.UnchangedFiles {
		reproject(&plan.UnchangedFiles[i].ProjectID)
	}
	return plan
}

func canonicalArchitectureSize(plan WorkflowDesignArchitecture) int {
	raw, err := json.Marshal(plan)
	if err != nil {
		return -1
	}
	return len(raw)
}

// maximalProjectionArchitecture returns the closed plan at exactly the
// 8,192-byte canonical bound, carrying quotes, HTML characters, a control
// byte, and a multibyte rune so the canonical form escapes them all.
func maximalProjectionArchitecture(t *testing.T) WorkflowDesignArchitecture {
	t.Helper()
	plan := projectionArchitecture()
	plan.Invariants[0].Statement = "plan\t<owns> & \"joins\"é"
	fill := 0
	addFiller := func(statement string) {
		fill++
		plan.Invariants = append(plan.Invariants, WorkflowDesignArchitectureInvariant{
			InvariantID: fmt.Sprintf("projection-fill-%02d", fill),
			OwnerID:     plan.Owners[0].OwnerID,
			Statement:   statement,
		})
	}
	for canonicalArchitectureSize(plan) < designArchitectureCanonicalMaxBytes-(designArchitectureStatementMaxBytes+128) {
		addFiller(strings.Repeat("x", 900))
	}
	for {
		addFiller("")
		remaining := designArchitectureCanonicalMaxBytes - canonicalArchitectureSize(plan)
		if remaining >= 1 && remaining <= designArchitectureStatementMaxBytes {
			plan.Invariants[len(plan.Invariants)-1].Statement = strings.Repeat("y", remaining)
			if size := canonicalArchitectureSize(plan); size == designArchitectureCanonicalMaxBytes {
				return plan
			}
		}
		plan.Invariants = plan.Invariants[:len(plan.Invariants)-1]
		plan.Invariants = plan.Invariants[:len(plan.Invariants)-1]
	}
}

func projectionDesignRaw(t *testing.T, content workflowDesignContent, plan *WorkflowDesignArchitecture) json.RawMessage {
	t.Helper()
	value := map[string]any{"approach": content.Approach, "decisions": content.Decisions, "touched_refs": content.TouchedRefs}
	if plan != nil {
		value["architecture"] = plan
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// projectionDesignEvent records one design through the one design writer and
// returns its event: payload version 1 without an architecture, 2 with.
func projectionDesignEvent(t *testing.T, workID, eventID string, expected int64, choice string, plan *WorkflowDesignArchitecture) Event {
	t.Helper()
	request := WorkflowActionExecutionRequest{WorkID: workID, Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).Add(time.Duration(expected) * time.Second)}
	events, err := workflowDesignRecordedEvents(request, "actor:test", projectionDesignRaw(t, projectionDesignContent(choice), plan), eventID, expected)
	if err != nil {
		t.Fatalf("cannot record the design event: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("design writer produced %d events, want 1", len(events))
	}
	wantVersion := 1
	if plan != nil {
		wantVersion = 2
	}
	if events[0].PayloadVersion != wantVersion {
		t.Fatalf("design payload version = %d, want %d", events[0].PayloadVersion, wantVersion)
	}
	return events[0]
}

func applyProjectionEvents(t *testing.T, s *Store, workID string, expected int64, events ...Event) error {
	t.Helper()
	return applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: workVersion(workID, expected)})
}

func assertProjectionRefusal(t *testing.T, err error, want FailureKind) *Failure {
	t.Helper()
	if err == nil {
		t.Fatal("the store accepted a design the projection must refuse")
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("refusal is not typed: %v", err)
	}
	if failure.Kind != want {
		t.Fatalf("refusal kind = %q, want %q: %v", failure.Kind, want, err)
	}
	return failure
}

func projectionDesignRowCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_design_records WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func projectionWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func projectionEventCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// projectionDesignRows dumps the durable design projection rows, NULLs
// flattened to sentinel text, so a rebuild compares byte for byte.
func projectionDesignRows(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT work_version,approach,decisions,touched_refs,COALESCE(architecture,''),COALESCE(revision,-1),COALESCE(digest,''),recorded_at FROM workflow_design_records WHERE work_id=? ORDER BY work_version`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var workVersion int64
		var approach, decisions, touched, architecture, digest, recordedAt string
		var revision int64
		if err := rows.Scan(&workVersion, &approach, &decisions, &touched, &architecture, &revision, &digest, &recordedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s|%d|%s|%s", workVersion, approach, decisions, touched, architecture, revision, digest, recordedAt))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func requireProjectionLegacyAbsence(t *testing.T, record *WorkflowDesignRecord) {
	t.Helper()
	if record == nil {
		t.Fatal("the design record is absent")
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{`"architecture":`, `"revision":`, `"digest":`} {
		if strings.Contains(string(canonical), member) {
			t.Fatalf("legacy design projection carries %s: %s", member, canonical)
		}
	}
	if record.Architecture != nil || record.Revision != nil || record.Digest != "" {
		t.Fatalf("legacy design carries new members in the typed record: %+v", record)
	}
}

// chargedDesignRecordSpan measures the design_record member span the
// capacity owner charges: the canonical record value plus the member key,
// its colon, and the trailing comma every non-last inputs member carries.
func chargedDesignRecordSpan(t *testing.T, record *WorkflowDesignRecord) int {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return len(`"design_record":`) + len(raw) + 1
}

func TestArchitectureProjection(t *testing.T) {
	ctx := context.Background()

	t.Run("digest covers the canonical design content", func(t *testing.T) {
		plan := projectionArchitecture()
		content := projectionDesignContent("canonical")
		content.Architecture = &plan
		digest, err := workflowDesignProjectionDigest(content)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
			t.Fatalf("digest = %q, want the sha256-prefixed 71-byte form", digest)
		}
		// The expectation is built from a hand-written document in a
		// different key order with insignificant whitespace, so the digest
		// follows the canonical JSON of the semantic content and not the
		// struct's field order or one writer's spelling.
		planJSON, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		handWritten := fmt.Sprintf(`{ "touched_refs" : ["src/projection.go"], "decisions":[{"rejected":[],"id":"decision:approach","question":"Which plan?","choice":"canonical","rationale":"The typed plan reads back whole"}] , "approach":"Projection approach" , "architecture":%s }`, planJSON)
		canonical, err := canonicalJSON(json.RawMessage(handWritten))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(canonical)
		if want := "sha256:" + hex.EncodeToString(sum[:]); digest != want {
			t.Fatalf("digest = %q, want the canonical-content hash %q", digest, want)
		}
	})

	t.Run("legacy writer keeps payload version one and strict bytes", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-legacy"
		seedWork(t, s, workID)
		raw := json.RawMessage(`{"approach":"Legacy approach","decisions":[{"id":"decision:approach","question":"Which plan?","choice":"legacy","rationale":"The legacy shape","rejected":[]}],"touched_refs":["src/legacy.go"]}`)
		events, err := workflowDesignRecordedEvents(WorkflowActionExecutionRequest{WorkID: workID, Now: time.Date(2026, 10, 15, 12, 0, 2, 0, time.UTC)}, "actor:test", raw, "projection-legacy-design", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("legacy writer produced %d events, want 1", len(events))
		}
		if events[0].PayloadVersion != 1 {
			t.Fatalf("legacy writer payload version = %d, want 1", events[0].PayloadVersion)
		}
		// The payload bytes carry the decoded typed decisions in their struct
		// field order — the released legacy writer's exact payload.
		want := `{"approach":"Legacy approach","decisions":[{"id":"decision:approach","question":"Which plan?","choice":"legacy","rationale":"The legacy shape","rejected":[]}],"expected_version":2,"resulting_version":3,"touched_refs":["src/legacy.go"],"work_id":"projection-legacy"}`
		if string(events[0].Payload) != want {
			t.Fatalf("legacy design event bytes drifted:\n got %s\nwant %s", events[0].Payload, want)
		}
		if err := applyProjectionEvents(t, s, workID, 2, events[0]); err != nil {
			t.Fatal(err)
		}
		snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		requireProjectionLegacyAbsence(t, snapshot.DesignRecord)
		// The public record is the exact released five-member JSON object.
		wantRecord := `{"work_version":3,"approach":"Legacy approach","decisions":[{"id":"decision:approach","question":"Which plan?","choice":"legacy","rationale":"The legacy shape","rejected":[]}],"touched_refs":["src/legacy.go"],"recorded_at":"2026-10-15T12:00:02Z"}`
		canonical, err := json.Marshal(snapshot.DesignRecord)
		if err != nil {
			t.Fatal(err)
		}
		if string(canonical) != wantRecord {
			t.Fatalf("legacy design record drifted:\n got %s\nwant %s", canonical, wantRecord)
		}
		var nullRows int
		if err := s.db.QueryRow(`SELECT count(*) FROM workflow_design_records WHERE work_id=? AND architecture IS NULL AND revision IS NULL AND digest IS NULL`, workID).Scan(&nullRows); err != nil {
			t.Fatal(err)
		}
		if nullRows != 1 {
			t.Fatalf("legacy design rows with all new columns NULL = %d, want 1", nullRows)
		}
	})

	t.Run("pinned record_design payload schema refuses architecture", func(t *testing.T) {
		plan := projectionArchitecture()
		raw := projectionDesignRaw(t, projectionDesignContent("schema"), &plan)
		if err := validateWorkflowActionPayload(currentWorkflowDefinition(t), "record_design", raw); err == nil {
			t.Fatal("the released record_design payload schema accepted an architecture")
		}
		if err := validateWorkflowActionPayload(historicalWorkflowDefinition(t, "workflow.implementation", 5), "record_design", raw); err == nil {
			t.Fatal("the pinned empty record_design payload schema accepted an architecture")
		}
	})

	t.Run("raw invalid UTF-8 in the architecture refuses before normalizing", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-utf8"
		seedWork(t, s, workID)
		eventsBefore := projectionEventCount(t, s, workID)
		plan := projectionArchitecture()
		planJSON, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		poisoned := strings.Replace(string(planJSON), `"symbol":"v`, "\"symbol\":\"\xff", 1)
		if poisoned == string(planJSON) {
			t.Fatal("the poison missed the symbol member")
		}
		writerRaw := fmt.Sprintf(`{"approach":"Raw approach","decisions":[{"id":"decision:approach","question":"Which plan?","choice":"raw","rationale":"The typed plan reads back whole","rejected":[]}],"touched_refs":["src/projection.go"],"architecture":%s}`, poisoned)
		_, err = workflowDesignRecordedEvents(WorkflowActionExecutionRequest{WorkID: workID, Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}, "actor:test", json.RawMessage(writerRaw), "projection-utf8-writer", 2)
		failure := assertProjectionRefusal(t, err, KindInvalidPayload)
		if !strings.Contains(failure.Detail, "UTF-8") {
			t.Fatalf("writer refusal detail = %q, want the raw UTF-8 cause", failure.Detail)
		}
		eventRaw := fmt.Sprintf(`{"work_id":%q,"expected_version":2,"resulting_version":3,"approach":"Raw approach","decisions":[{"id":"decision:approach","question":"Which plan?","choice":"raw","rationale":"The typed plan reads back whole","rejected":[]}],"touched_refs":["src/projection.go"],"architecture":%s}`, workID, poisoned)
		event := Event{EventID: "projection-utf8-event", Kind: WorkflowDesignRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "actor:test", OccurredAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UTC(), PayloadVersion: 2, Payload: json.RawMessage(eventRaw)}
		failure = assertProjectionRefusal(t, applyProjectionEvents(t, s, workID, 2, event), KindInvalidPayload)
		if !strings.Contains(failure.Detail, "UTF-8") {
			t.Fatalf("event refusal detail = %q, want the raw UTF-8 cause", failure.Detail)
		}
		if version := projectionWorkVersion(t, s, workID); version != 2 {
			t.Fatalf("the normalized write moved the work version to %d", version)
		}
		if count := projectionDesignRowCount(t, s, workID); count != 0 {
			t.Fatalf("the normalized write left %d design rows", count)
		}
		if eventsAfter := projectionEventCount(t, s, workID); eventsAfter != eventsBefore {
			t.Fatalf("the normalized write changed the event count %d -> %d", eventsBefore, eventsAfter)
		}
	})

	t.Run("forged v2 without architecture refuses at the operation boundary", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-v2-nil"
		seedWork(t, s, workID)
		eventsBefore := projectionEventCount(t, s, workID)
		forge := func(name string, withArchitectureKey bool) Event {
			value := map[string]any{
				"work_id": workID, "expected_version": 2, "resulting_version": 3, "approach": "Forged approach",
				"decisions": projectionDesignContent("forged").Decisions, "touched_refs": []string{"src/projection.go"},
			}
			if withArchitectureKey {
				value["architecture"] = nil
			}
			event := workflowEventWithActor("projection-"+name, WorkflowDesignRecorded, workID, "actor:test", value)
			event.PayloadVersion = 2
			return event
		}
		for _, testCase := range []struct {
			name                string
			withArchitectureKey bool
		}{
			{"omitted", false},
			{"nulled", true},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				assertProjectionRefusal(t, applyProjectionEvents(t, s, workID, 2, forge(testCase.name, testCase.withArchitectureKey)), KindInvalidPayload)
			})
		}
		if version := projectionWorkVersion(t, s, workID); version != 2 {
			t.Fatalf("forged v2 moved the work version to %d", version)
		}
		if count := projectionDesignRowCount(t, s, workID); count != 0 {
			t.Fatalf("forged v2 left %d design rows", count)
		}
		if eventsAfter := projectionEventCount(t, s, workID); eventsAfter != eventsBefore {
			t.Fatalf("forged v2 changed the event count %d -> %d", eventsBefore, eventsAfter)
		}
	})

	t.Run("caller revision digest and approved fields each refuse", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-caller"
		seedWork(t, s, workID)
		plan := projectionArchitecture()
		forgeField := func(name, field, value string) Event {
			event := projectionDesignEvent(t, workID, "projection-caller-"+name, 2, "forged", &plan)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(event.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = json.RawMessage(value)
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			event.Payload = payload
			return event
		}
		for _, testCase := range []struct{ name, field, value string }{
			{"revision", "revision", `99`},
			{"digest", "digest", `"sha256:caller-supplied"`},
			{"approved", "approved", `true`},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				assertProjectionRefusal(t, applyProjectionEvents(t, s, workID, 2, forgeField(testCase.name, testCase.field, testCase.value)), KindInvalidPayload)
			})
		}
		if version := projectionWorkVersion(t, s, workID); version != 2 {
			t.Fatalf("a refused caller-derived design moved the work version to %d", version)
		}
		if count := projectionDesignRowCount(t, s, workID); count != 0 {
			t.Fatalf("refused caller-derived designs left %d rows", count)
		}
	})

	t.Run("legacy upcast keeps the strict shape and old versions refuse", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-upcast"
		seedWork(t, s, workID)
		plan := projectionArchitecture()
		forged := workflowEventWithActor("projection-upcast-arch", WorkflowDesignRecorded, workID, "actor:test", map[string]any{
			"work_id": workID, "expected_version": 2, "resulting_version": 3, "approach": "Forged approach",
			"decisions": projectionDesignContent("forged").Decisions, "touched_refs": []string{"src/projection.go"}, "architecture": plan,
		})
		if forged.PayloadVersion != 1 {
			t.Fatalf("forged v1 payload version = %d, want 1", forged.PayloadVersion)
		}
		failure := assertProjectionRefusal(t, applyProjectionEvents(t, s, workID, 2, forged), KindInvalidPayload)
		if failure.Stage != StageUpcast && failure.Stage != StageDecode {
			t.Fatalf("forged v1 architecture refusal stage = %q, want upcast or decode", failure.Stage)
		}
		plain := workflowEventWithActor("projection-upcast-plain", WorkflowDesignRecorded, workID, "actor:test", map[string]any{
			"work_id": workID, "expected_version": 2, "resulting_version": 3, "approach": "Plain approach",
			"decisions": projectionDesignContent("plain").Decisions, "touched_refs": []string{"src/projection.go"},
		})
		if err := applyProjectionEvents(t, s, workID, 2, plain); err != nil {
			t.Fatal(err)
		}
		snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		requireProjectionLegacyAbsence(t, snapshot.DesignRecord)
		unsupported := workflowEventWithActor("projection-upcast-v3", WorkflowDesignRecorded, workID, "actor:test", map[string]any{
			"work_id": workID, "expected_version": 3, "resulting_version": 4, "approach": "Future approach",
			"decisions": projectionDesignContent("future").Decisions, "touched_refs": []string{"src/projection.go"},
		})
		unsupported.PayloadVersion = 3
		assertProjectionRefusal(t, applyProjectionEvents(t, s, workID, 3, unsupported), KindUnsupportedPayloadVersion)
	})

	t.Run("malformed architecture refuses with no durable effect", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-malformed"
		seedWork(t, s, workID)
		plan := projectionArchitecture()
		plan.Invariants[0].OwnerID = "ghost-owner"
		err := applyProjectionEvents(t, s, workID, 2, projectionDesignEvent(t, workID, "projection-malformed-design", 2, "broken", &plan))
		failure := assertProjectionRefusal(t, err, KindInvalidPayload)
		if failure.Op != "workflow_design_architecture" {
			t.Fatalf("malformed architecture refusal op = %q, want workflow_design_architecture", failure.Op)
		}
		if version := projectionWorkVersion(t, s, workID); version != 2 {
			t.Fatalf("refused architecture moved the work version to %d", version)
		}
		if count := projectionDesignRowCount(t, s, workID); count != 0 {
			t.Fatalf("refused architecture left %d rows", count)
		}
	})

	t.Run("architecture naming a non-member project refuses", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-foreign"
		seedWork(t, s, workID)
		foreign := designArchitectureFixture()
		err := applyProjectionEvents(t, s, workID, 2, projectionDesignEvent(t, workID, "projection-foreign-design", 2, "foreign", &foreign))
		failure := assertProjectionRefusal(t, err, KindInvalidPayload)
		if !strings.Contains(failure.Detail, "not a member of the work") {
			t.Fatalf("non-member refusal detail = %q, want the membership join refusal", failure.Detail)
		}
	})

	t.Run("maximal design reads complete at limit one and default", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-maximal"
		seedWork(t, s, workID)
		plan := maximalProjectionArchitecture(t)
		if size := canonicalArchitectureSize(plan); size != designArchitectureCanonicalMaxBytes {
			t.Fatalf("canonical architecture = %d bytes, want the %d byte bound", size, designArchitectureCanonicalMaxBytes)
		}
		// The declared design_record slot charges the whole member span —
		// key, colon, trailing comma, and the serialized record — so the
		// approach fills the remaining wire bytes of the complete record
		// with escaping prose of its own.
		const recordedAt = "2026-10-15T12:00:02Z"
		digestPlaceholder := "sha256:" + strings.Repeat("d", 64)
		expectedRecord := func(approach, digest string) *WorkflowDesignRecord {
			return &WorkflowDesignRecord{
				WorkVersion: 3,
				Approach:    approach,
				Decisions: []WorkflowDesignDecision{{
					ID: "decision:approach", Question: "Which plan?", Choice: "maximal", Rationale: "The typed plan reads back whole", Rejected: []string{},
				}},
				TouchedRefs:  []string{"src/projection.go"},
				RecordedAt:   recordedAt,
				Architecture: func() *WorkflowDesignArchitecture { copied := plan; return &copied }(),
				Revision:     func() *int64 { revision := int64(1); return &revision }(),
				Digest:       digest,
			}
		}
		prefix := "plan\t<owns> & \"design\"\u0001é "
		fit := plan.Capacity.DesignRecord - chargedDesignRecordSpan(t, expectedRecord(prefix, digestPlaceholder))
		if fit < 1 || len(prefix)+fit > 4096 {
			t.Fatalf("the declared slot needs %d approach bytes, outside the decoded approach bound", fit)
		}
		approach := prefix + strings.Repeat("x", fit)
		if got := chargedDesignRecordSpan(t, expectedRecord(approach, digestPlaceholder)); got != plan.Capacity.DesignRecord {
			t.Fatalf("charged design_record span = %d, want the declared %d", got, plan.Capacity.DesignRecord)
		}
		content := projectionDesignContent("maximal")
		content.Approach = approach
		events, err := workflowDesignRecordedEvents(WorkflowActionExecutionRequest{WorkID: workID, Now: time.Date(2026, 10, 15, 12, 0, 2, 0, time.UTC)}, "actor:test", projectionDesignRaw(t, content, &plan), "projection-maximal-design", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].PayloadVersion != 2 {
			t.Fatalf("maximal design events = %+v, want one v2 event", events)
		}
		if err := applyProjectionEvents(t, s, workID, 2, events[0]); err != nil {
			t.Fatal(err)
		}
		recorded, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		expected := expectedRecord(approach, recorded.DesignRecord.Digest)
		// The capacity owner charges the actual slot with the complete
		// packet fixture: the real design record replaces the synthetic
		// member at the same declared span, so the witness measures the
		// genuine record and no slot overflows.
		profile, err := NewArchitectureCapacityProfile(plan.Capacity)
		if err != nil {
			t.Fatal(err)
		}
		members := architectureCapacityPacketFixture(architectureCapacityStructuralPad(t), nil, nil)
		recordRaw, err := json.Marshal(recorded.DesignRecord)
		if err != nil {
			t.Fatal(err)
		}
		if got := chargedDesignRecordSpan(t, recorded.DesignRecord); got != plan.Capacity.DesignRecord {
			t.Fatalf("recorded design_record charged span = %d, want the declared %d", got, plan.Capacity.DesignRecord)
		}
		members["design_record"] = recordRaw
		measurement, err := CheckArchitecturePacketCapacity(profile, members, false)
		if err != nil {
			t.Fatalf("the complete maximal packet refuses: %v", err)
		}
		if measurement.SlotBytes[architectureSlotDesignRecord] != plan.Capacity.DesignRecord {
			t.Fatalf("measured design_record slot = %d, want the declared %d", measurement.SlotBytes[architectureSlotDesignRecord], plan.Capacity.DesignRecord)
		}
		for _, limit := range []int{1, 0} {
			snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID, Limit: limit})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(snapshot.DesignRecord, expected) {
				t.Fatalf("maximal design at limit %d does not read back whole:\n got %+v\nwant %+v", limit, snapshot.DesignRecord, expected)
			}
		}
	})

	t.Run("v2 design derives revision one then two with content-bound digest", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-revision"
		seedWork(t, s, workID)
		plan := projectionArchitecture()
		if err := applyProjectionEvents(t, s, workID, 2, projectionDesignEvent(t, workID, "projection-revision-1", 2, "first", &plan)); err != nil {
			t.Fatal(err)
		}
		first, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		record := first.DesignRecord
		if record == nil || record.Revision == nil || *record.Revision != 1 || record.Digest == "" {
			t.Fatalf("first architecture design = %+v, want revision 1 with a digest", record)
		}
		if !reflect.DeepEqual(*record.Architecture, plan) {
			t.Fatal("first architecture design does not read back whole")
		}
		changed := projectionArchitecture()
		changed.Invariants[0].Statement = "the changed plan carries different content"
		if err := applyProjectionEvents(t, s, workID, 3, projectionDesignEvent(t, workID, "projection-revision-2", 3, "second", &changed)); err != nil {
			t.Fatal(err)
		}
		second, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		revised := second.DesignRecord
		if revised == nil || revised.Revision == nil || *revised.Revision != 2 {
			t.Fatalf("second architecture design = %+v, want revision 2", revised)
		}
		if revised.Digest == record.Digest {
			t.Fatalf("changed design content kept the digest %s", revised.Digest)
		}
		if !reflect.DeepEqual(*revised.Architecture, changed) {
			t.Fatal("second architecture design does not read back whole")
		}
		// Identical content recorded again at a later time keeps the digest
		// and takes the next revision: recording metadata never moves it.
		if err := applyProjectionEvents(t, s, workID, 4, projectionDesignEvent(t, workID, "projection-revision-3", 4, "first", &plan)); err != nil {
			t.Fatal(err)
		}
		third, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		if third.DesignRecord == nil || third.DesignRecord.Revision == nil || *third.DesignRecord.Revision != 3 {
			t.Fatalf("third architecture design = %+v, want revision 3", third.DesignRecord)
		}
		if third.DesignRecord.Digest != record.Digest {
			t.Fatalf("identical content changed the digest from %s to %s", record.Digest, third.DesignRecord.Digest)
		}
		var historical int
		if err := s.db.QueryRow(`SELECT count(*) FROM workflow_design_records WHERE work_id=? AND revision=1 AND digest=?`, workID, record.Digest).Scan(&historical); err != nil {
			t.Fatal(err)
		}
		if historical != 1 {
			t.Fatalf("historical revision 1 rows with the first digest = %d, want 1", historical)
		}
	})

	t.Run("revisions are independent per work", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const first, second = "projection-cross-a", "projection-cross-b"
		seedWork(t, s, first)
		seedWork(t, s, second)
		plan := projectionArchitecture()
		if err := applyProjectionEvents(t, s, first, 2, projectionDesignEvent(t, first, "projection-cross-a-1", 2, "a", &plan)); err != nil {
			t.Fatal(err)
		}
		if err := applyProjectionEvents(t, s, second, 2, projectionDesignEvent(t, second, "projection-cross-b-1", 2, "b", &plan)); err != nil {
			t.Fatal(err)
		}
		for _, workID := range []string{first, second} {
			snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.DesignRecord == nil || snapshot.DesignRecord.Revision == nil || *snapshot.DesignRecord.Revision != 1 {
				t.Fatalf("work %s revision = %+v, want its own revision 1", workID, snapshot.DesignRecord)
			}
		}
	})

	t.Run("supersede invalidates without deleting and the revision stays monotonic", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-supersede"
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		base, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 6)
		if !ok {
			t.Fatal("typed design workflow is not registered")
		}
		shape := workflowFixtureShape(base.Definition, 6)
		shape.Ref = "workflow.test_design_projection"
		definition, err := BuiltinWorkflowRegistry().Register(shape)
		if err != nil {
			t.Fatal(err)
		}
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/projection-supersede", ActorClass: ActorAgent}
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		plan := projectionArchitecture()
		events := []Event{
			workflowEventWithActor("projection-owner", WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
			workflowEvent("projection-definition", WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
			workflowActionCompletedFixture("projection-proposal", workID, ownerRef, 4, "proposal", "record_proposal"),
			workflowActionCompletedFixture("projection-discovery", workID, ownerRef, 5, "discovery", "record_discovery"),
			projectionDesignEvent(t, workID, "projection-supersede-design", 6, "superseded", &plan),
			workflowActionCompletedFixture("projection-design-step", workID, ownerRef, 7, "design", "record_design"),
			workflowEventWithActor("projection-contract", WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "contract_version": 1, "premise": "original scope", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:original", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
			workflowActionCompletedFixture("projection-approved", workID, ownerRef, 9, "planning", "approve_contract"),
		}
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: workVersion(workID, 2)}); err != nil {
			t.Fatal(err)
		}
		operator := operatorVerdictActor(t, workID)
		if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", issue1013SuccessorContract(), owner, operator); err != nil {
			t.Fatal(err)
		}
		invalidated, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		if invalidated.DesignRecord != nil {
			t.Fatalf("superseded architecture design stayed current: %+v", invalidated.DesignRecord)
		}
		if count := projectionDesignRowCount(t, s, workID); count != 1 {
			t.Fatalf("supersession deleted architecture history: rows=%d", count)
		}
		current := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, current, projectionDesignEvent(t, workID, "projection-supersede-design-2", current, "replacement", &plan)); err != nil {
			t.Fatal(err)
		}
		restored, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		if restored.DesignRecord == nil || restored.DesignRecord.Revision == nil || *restored.DesignRecord.Revision != 2 {
			t.Fatalf("replacement architecture design = %+v, want revision 2", restored.DesignRecord)
		}
		if count := projectionDesignRowCount(t, s, workID); count != 2 {
			t.Fatalf("replacement design lost history: rows=%d", count)
		}
		var historical int
		if err := s.db.QueryRow(`SELECT count(*) FROM workflow_design_records WHERE work_id=? AND revision=1`, workID).Scan(&historical); err != nil {
			t.Fatal(err)
		}
		if historical != 1 {
			t.Fatalf("historical revision 1 rows = %d, want 1", historical)
		}
	})

	t.Run("rebuild reproduces the projection exactly", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const architectureWork, legacyWork = "projection-rebuild-a", "projection-rebuild-b"
		seedWork(t, s, architectureWork)
		seedWork(t, s, legacyWork)
		first := projectionArchitecture()
		if err := applyProjectionEvents(t, s, architectureWork, 2, projectionDesignEvent(t, architectureWork, "projection-rebuild-a-1", 2, "first", &first)); err != nil {
			t.Fatal(err)
		}
		second := projectionArchitecture()
		second.Invariants[0].Statement = "the rebuilt second revision carries different content"
		if err := applyProjectionEvents(t, s, architectureWork, 3, projectionDesignEvent(t, architectureWork, "projection-rebuild-a-2", 3, "second", &second)); err != nil {
			t.Fatal(err)
		}
		raw := json.RawMessage(`{"approach":"Legacy rebuild","decisions":[{"id":"decision:approach","question":"Which plan?","choice":"legacy","rationale":"The legacy shape","rejected":[]}],"touched_refs":["src/legacy.go"]}`)
		legacyEvents, err := workflowDesignRecordedEvents(WorkflowActionExecutionRequest{WorkID: legacyWork, Now: time.Date(2026, 10, 15, 12, 0, 2, 0, time.UTC)}, "actor:test", raw, "projection-rebuild-b-1", 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyProjectionEvents(t, s, legacyWork, 2, legacyEvents...); err != nil {
			t.Fatal(err)
		}
		beforeA := projectionDesignRows(t, s, architectureWork)
		beforeB := projectionDesignRows(t, s, legacyWork)
		if len(beforeA) != 2 {
			t.Fatalf("architecture design rows = %d, want both revisions", len(beforeA))
		}
		if err := RebuildFromLog(ctx, s); err != nil {
			t.Fatal(err)
		}
		afterA := projectionDesignRows(t, s, architectureWork)
		afterB := projectionDesignRows(t, s, legacyWork)
		if !reflect.DeepEqual(beforeA, afterA) {
			t.Fatalf("architecture design rebuild drifted:\n before %v\n after  %v", beforeA, afterA)
		}
		if !reflect.DeepEqual(beforeB, afterB) {
			t.Fatalf("legacy design rebuild drifted:\n before %v\n after  %v", beforeB, afterB)
		}
		snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: architectureWork})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.DesignRecord == nil || snapshot.DesignRecord.Revision == nil || *snapshot.DesignRecord.Revision != 2 || !reflect.DeepEqual(*snapshot.DesignRecord.Architecture, second) {
			t.Fatal("rebuilt architecture design is not the second revision reading back whole")
		}
		var historical int
		if err := s.db.QueryRow(`SELECT count(*) FROM workflow_design_records WHERE work_id=? AND revision=1`, architectureWork).Scan(&historical); err != nil {
			t.Fatal(err)
		}
		if historical != 1 {
			t.Fatalf("historical revision 1 rows = %d, want 1", historical)
		}
	})

	t.Run("partial architecture projection refuses", func(t *testing.T) {
		s := openTemp(t)
		defer s.Close()
		const workID = "projection-partial"
		seedWork(t, s, workID)
		digest := "sha256:" + strings.Repeat("a", 64)
		// The raw rows satisfy the released column checks so each insert
		// reaches exactly the projection invariant under proof.
		rowDecisions := `[{"id":"decision:raw","question":"Which plan?","choice":"raw","rationale":"The raw row","rejected":[]}]`
		rowTouched := `["src/projection.go"]`
		// A legacy-shaped row carries NULL in all three columns and satisfies
		// the shape constraint, so the constraint owns only the new fields.
		func() {
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES(?,?,?,?,?,?)`,
				workID, 2, "Legacy shape", rowDecisions, rowTouched, "2026-10-15T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(`UPDATE workflow_design_records SET digest=? WHERE work_id=?`, digest, workID)
			if err == nil {
				t.Fatal("an update introduced a partial architecture projection")
			}
			// The rows are insert-only: the legacy immutability guard owns the
			// UPDATE route and aborts before the shape constraint evaluates,
			// so no update can introduce a partial triple.
			if !strings.Contains(err.Error(), "workflow_design_records is immutable") {
				t.Fatalf("partial projection error = %v, want the insert-only refusal", err)
			}
		}()
		inserts := []struct {
			name         string
			architecture string
			revision     string
			digestValue  string
		}{
			{"architecture without digest", `'{"capacity":null}'`, "1", "NULL"},
			{"digest without architecture", "NULL", "1", "'" + digest + "'"},
			{"architecture without revision", `'{"capacity":null}'`, "NULL", "'" + digest + "'"},
		}
		for _, insert := range inserts {
			t.Run(insert.name, func(t *testing.T) {
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(`INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,architecture,revision,digest,recorded_at) VALUES('` + workID + `',2,'Partial approach','` + rowDecisions + `','` + rowTouched + `',` + insert.architecture + `,` + insert.revision + `,` + insert.digestValue + `,'2026-10-15T00:00:00Z')`)
				if err == nil {
					t.Fatal("the partial architecture projection inserted")
				}
				if !strings.Contains(err.Error(), "CHECK constraint failed: workflow_design_records_architecture_shape") {
					t.Fatalf("partial projection error = %v, want the shape constraint refusal", err)
				}
			})
		}
		t.Run("malformed stored architecture reads as a typed failure", func(t *testing.T) {
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,architecture,revision,digest,recorded_at) VALUES(?,?,?,?,?,?,?,?,?)`,
				workID, 2, "Damaged approach", rowDecisions, rowTouched, `{"owners":{}}`, 1, digest, "2026-10-15T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			read, _, err := readCurrentWorkflowDesign(ctx, s.db, workID)
			if err == nil {
				t.Fatalf("malformed stored architecture read back as %+v", read)
			}
			failure := assertProjectionRefusal(t, err, KindInvariantViolation)
			if failure.Op != "workflow_design" {
				t.Fatalf("malformed stored architecture refusal op = %q, want workflow_design", failure.Op)
			}
		})
	})
}
