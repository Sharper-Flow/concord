package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// auditReclaimEnvelopeFixture claims, completes, and vacates terminal
// worktrees, then drops orphan directories beside them, so one Product holds
// reclaimable rows and a report-only population at the same time.
func auditReclaimEnvelopeFixture(t *testing.T, terminal, orphans int) (*store.Store, *Service, Authority, string) {
	t.Helper()
	s, service, grant, repoRoot := tiersRepoFixture(t)
	baseSHA := gitRun(t, repoRoot, "rev-parse", "HEAD")
	ctx := context.Background()
	events := make([]store.Event, 0, terminal*2)
	versions := map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, "product-1"): 0,
		store.VersionRef(store.SubjectProject, "project-1"): 0,
	}
	for i := 1; i <= terminal; i++ {
		workID := fmt.Sprintf("work-paged-%02d", i)
		events = append(events,
			store.Event{EventID: "audit-paged-work-" + workID, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Audit Paged ` + workID + `","priority":1}`)},
			store.Event{EventID: "audit-paged-membership-" + workID, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		)
		versions[store.VersionRef(store.SubjectWorkItem, workID)] = 0
	}
	if len(events) > 0 {
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: versions}); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1")
	for i := 1; i <= terminal; i++ {
		workID := fmt.Sprintf("work-paged-%02d", i)
		if response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_claim", map[string]any{"host_pid": os.Getpid(),
			"work_id": workID, "project_id": "project-1", "base_sha": baseSHA, "expected_version": 2, "idempotency_key": "audit-paged-claim-" + workID,
		}); response.Outcome != OutcomeOK {
			t.Fatalf("claim %s response=%+v err=%+v", workID, response, response.Error)
		}
		completeWork(t, s, workID)
		vacateLinkedWorktree(t, s, service, grant, filepath.Join(root, workID), "audit-paged-vacate-"+workID)
	}
	for i := 1; i <= orphans; i++ {
		orphan := filepath.Join(root, fmt.Sprintf("work-orphan-%04d", i))
		if err := os.MkdirAll(orphan, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s, service, grant, repoRoot
}

// A pass whose full result exceeds the 51200-byte result envelope cap must
// still report ok: every committed attempt stays reported with its outcome
// and version, the report-only classification pages through the audit read
// under an explicit count (CD-0185), and the delivered envelope fits the cap
// the producer is held to. Before the bound, this pass committed its rows and
// then returned limit_exceeded with effect_state possible.
func TestAuditReclaimPageSizeExceedingEnvelopeReportsOkWithPagedReport(t *testing.T) {
	t.Parallel()
	const terminal, orphans = 5, 300
	s, service, grant, _ := auditReclaimEnvelopeFixture(t, terminal, orphans)
	input := map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-paged-pass-1", "limit": terminal}
	response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if response.Outcome != OutcomeOK {
		t.Fatalf("a compacted pass must report ok, got %+v", response.Error)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("delivered envelope must marshal within the cap: %v", err)
	}
	if len(encoded) > MaxResultEnvelopeBytes {
		t.Fatalf("envelope=%d bytes, cap %d", len(encoded), MaxResultEnvelopeBytes)
	}
	if response.ChangedRefs == nil || len(*response.ChangedRefs) != terminal {
		t.Fatalf("changed refs=%d, want every committed row", len(derefChangedRefs(response.ChangedRefs)))
	}
	var result struct {
		Rows []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
			Version int64  `json:"version"`
		} `json:"rows"`
		ReportOnly []struct {
			Class string `json:"class"`
			Path  string `json:"path"`
		} `json:"report_only"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Rows) != terminal {
		t.Fatalf("rows=%d, want every attempt reported", len(result.Rows))
	}
	for _, row := range result.Rows {
		if row.Outcome != "reclaimed" || row.Version == 0 {
			t.Fatalf("attempt row %s outcome=%s version=%d, want reclaimed with its version", row.WorkID, row.Outcome, row.Version)
		}
	}
	if len(result.ReportOnly) == 0 || len(result.ReportOnly) >= orphans {
		t.Fatalf("inline report_only=%d, want a non-empty prefix of the %d classified rows", len(result.ReportOnly), orphans)
	}
	var notice *Notice
	for i := range response.Omissions {
		if response.Omissions[i].Kind == "report_only_paged" {
			notice = &response.Omissions[i]
		}
	}
	if notice == nil {
		t.Fatalf("omissions=%+v, want a report_only_paged notice", response.Omissions)
	}
	if notice.Count != int64(orphans-len(result.ReportOnly)) {
		t.Fatalf("notice count=%d, want the %d paged rows", notice.Count, orphans-len(result.ReportOnly))
	}
	if notice.Details["classified"] != int64(orphans) || notice.Details["reported"] != int64(len(result.ReportOnly)) {
		t.Fatalf("notice details=%+v, want classified %d and reported %d", notice.Details, orphans, len(result.ReportOnly))
	}
}

// The idempotency record stores the compacted payload, so a replay under the
// same key returns the same bounded result instead of reclassifying into a
// different report.
func TestAuditReclaimReplayReturnsSameBoundedResult(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := auditReclaimEnvelopeFixture(t, 2, 300)
	input := map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "audit-paged-pass-replay", "limit": 2}
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if first.Outcome != OutcomeOK {
		t.Fatalf("first pass=%+v", first.Error)
	}
	second := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_audit_reclaim", input)
	if second.Outcome != OutcomeOK {
		t.Fatalf("replay=%+v", second.Error)
	}
	if !second.Replayed {
		t.Fatalf("replay must be marked replayed")
	}
	if string(second.Result) != string(first.Result) {
		t.Fatalf("replay result differs:\nfirst=%s\nsecond=%s", first.Result, second.Result)
	}
}

// The audit read pages the complete classification: every page carries a
// signed cursor until the classification is exhausted, and walking the pages
// yields each classified row exactly once (CD-0185). Before the cursor, the
// read stopped at its limit and the remainder was unreachable.
func TestWorktreeAuditReadPagesCompleteClassification(t *testing.T) {
	t.Parallel()
	const orphans = 130
	s, service, grant, _ := auditReclaimEnvelopeFixture(t, 0, orphans)
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	for {
		input := map[string]any{"product_id": "product-1", "page": map[string]any{"cursor": nil, "limit": 50}}
		if cursor != "" {
			input["page"] = map[string]any{"cursor": cursor, "limit": 50}
		}
		response := tiersInvoke(t, s, service, grant, "concord_work_browse", "worktree_audit", input)
		if response.Outcome != OutcomeOK {
			t.Fatalf("audit page failed: %+v", response.Error)
		}
		pages++
		var page struct {
			Drift []store.WorktreeDrift `json:"drift"`
		}
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatalf("decode page: %v", err)
		}
		if len(page.Drift) == 0 {
			t.Fatalf("page %d is empty before the classification is exhausted", pages)
		}
		for _, row := range page.Drift {
			if seen[row.Path] {
				t.Fatalf("path %s reported twice across pages", row.Path)
			}
			seen[row.Path] = true
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			break
		}
		cursor = *response.NextCursor
		if pages > 10 {
			t.Fatalf("paging did not exhaust %d rows in ten pages", orphans)
		}
	}
	if len(seen) != orphans {
		t.Fatalf("walked rows=%d, want the complete classification of %d", len(seen), orphans)
	}
}

// The byte budget itself, against a population the schema's row cap cannot
// save: long paths and full detail text push the result past the envelope cap
// with every array inside its schema bounds. The compaction pages the
// report-only rows first, then drops the free-text detail from the attempt
// rows from the last row backward, and never drops an attempt row (CD-0185).
func TestCompactAuditReclaimReportBoundsResultToEnvelopeCap(t *testing.T) {
	t.Parallel()
	path := strings.Repeat("p", 400)
	detail := strings.Repeat("d", 512)
	rows := make([]store.WorktreeAuditReclaimRow, 80)
	for i := range rows {
		rows[i] = store.WorktreeAuditReclaimRow{ProjectID: "project-1", WorkID: fmt.Sprintf("work-byte-%02d", i), Path: path + fmt.Sprintf("%03d", i), Lifecycle: "completed", Outcome: store.WorktreeAuditReclaimed, Version: 7, Detail: detail}
	}
	reportOnly := make([]store.WorktreeDrift, 90)
	for i := range reportOnly {
		reportOnly[i] = store.WorktreeDrift{Class: store.WorktreeDriftOrphan, ProjectID: "project-1", WorkID: fmt.Sprintf("work-orphan-%03d", i), Path: path + fmt.Sprintf("%03d", i), RecoveryAction: store.WorktreeRecoveryRemoveOrphan}
	}
	changed := make([]ChangedRef, 32)
	for i := range changed {
		changed[i] = ChangedRef{EntityKind: "work_item", ID: fmt.Sprintf("work-byte-%02d", i), Version: "7"}
	}
	payload, _ := json.Marshal(map[string]any{
		"root": "/worktrees", "rows": rows, "report_only": reportOnly,
		"changed_refs":       mutationResultChangedRefs(changed),
		"next_valid_intents": mutationResultIntents(nil),
	})
	base := NewBase("compact-bytes", "concord_work_transition", "worktree_audit_reclaim")
	base.ResolvedScope = &Scope{ProductID: "product-1"}
	if uncompacted := auditReclaimEnvelopeBytes(base, payload, changed, nil, nil); uncompacted <= MaxResultEnvelopeBytes {
		t.Fatalf("the uncompacted envelope is %d bytes; the test must start over the cap", uncompacted)
	}
	compacted, notices := compactAuditReclaimReport(base, payload, changed, nil)
	var result struct {
		Rows []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
			Version int64  `json:"version"`
			Detail  string `json:"detail"`
		} `json:"rows"`
		ReportOnly []json.RawMessage `json:"report_only"`
	}
	if err := json.Unmarshal(compacted, &result); err != nil {
		t.Fatalf("decode compacted result: %v", err)
	}
	if len(result.Rows) != len(rows) {
		t.Fatalf("rows=%d, want every attempt row kept", len(result.Rows))
	}
	for i, row := range result.Rows {
		if row.Outcome != "reclaimed" || row.Version != 7 {
			t.Fatalf("attempt row %d lost its outcome or version: %+v", i, row)
		}
	}
	if len(result.ReportOnly) == len(reportOnly) {
		t.Fatalf("report_only kept every row; the byte budget never paged")
	}
	paged, details := false, false
	for _, notice := range notices {
		if notice.Kind == "report_only_paged" {
			paged = true
		}
		if notice.Kind == "row_details_omitted" {
			details = true
		}
	}
	if !paged || !details {
		t.Fatalf("notices=%+v, want report_only_paged and row_details_omitted", notices)
	}
	if result.Rows[0].Detail == "" {
		t.Fatalf("the first attempt row must keep its detail longest")
	}
	if result.Rows[len(result.Rows)-1].Detail != "" {
		t.Fatalf("the last attempt row's detail must drop first")
	}
	if encoded := auditReclaimEnvelopeBytes(base, compacted, changed, nil, notices); encoded > MaxResultEnvelopeBytes {
		t.Fatalf("compacted envelope=%d bytes, cap %d", encoded, MaxResultEnvelopeBytes)
	}
}

func derefChangedRefs(refs *[]ChangedRef) []ChangedRef {
	if refs == nil {
		return nil
	}
	return *refs
}
