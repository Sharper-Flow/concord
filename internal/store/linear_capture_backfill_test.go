package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// captureLinearFixtureWork captures one work item through the same operation
// shape every capture route applies: work.created followed by
// work.memberships_replaced in one operation, so the capture-time enqueue fold
// runs exactly as it does in production.
func captureLinearFixtureWork(t *testing.T, s *Store, workID, projectID, kind, title, valueStatement, externalRef string) {
	t.Helper()
	ctx := context.Background()
	priority := int64(3)
	payload, err := json.Marshal(workCreatedPayload{WorkID: workID, WorkKind: kind, Title: title, ValueStatement: valueStatement, Priority: &priority, ExternalRef: externalRef})
	if err != nil {
		t.Fatal(err)
	}
	memberships, err := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: projectID, Role: "primary"}}, ExpectedVersion: 1, ResultingVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			{EventID: workID + "-created", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: payload},
			{EventID: workID + "-memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: memberships},
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0},
	}); err != nil {
		t.Fatalf("capture %s: %v", workID, err)
	}
}

func countLinearOutboxRows(t *testing.T, s *Store, workID string) (int, string) {
	t.Helper()
	var count int
	var opKind string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*), coalesce(max(op_kind), '') FROM linear_outbox WHERE work_id=?`, workID).Scan(&count, &opKind); err != nil {
		t.Fatal(err)
	}
	return count, opKind
}

// countLinearInvisibleWorkItems is the completeness invariant of the
// capture-time enqueue: a non-terminal, non-initiative work item with no link
// row, no Linear identity in its external_ref, and membership in a
// linear_enabled Product must not exist after capture plus backfill. The
// fixture owns the declared-connection and project-mapping conditions, which
// the setup helpers establish for every linear_enabled Product it creates.
func countLinearInvisibleWorkItems(t *testing.T, s *Store) int {
	t.Helper()
	rows, err := s.DatabaseForTesting().QueryContext(context.Background(), `
SELECT coalesce(json_extract(w.intent_json, '$.external_ref'), '')
FROM work_items w
WHERE w.terminal_time IS NULL
  AND w.kind <> 'initiative'
  AND NOT EXISTS (SELECT 1 FROM linear_issue_links l WHERE l.work_id = w.id)
  AND EXISTS (
    SELECT 1 FROM work_projects wp
    JOIN product_projects pp ON pp.project_id = wp.project_id
     JOIN products p ON p.id = pp.product_id
     WHERE wp.work_id = w.id AND p.planning_mode = 'linear_enabled')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var externalRef string
		if err := rows.Scan(&externalRef); err != nil {
			t.Fatal(err)
		}
		if _, exists := normalizeLinearIssueExternalRef(externalRef); !exists {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNormalizeLinearIssueExternalRef(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "linear identity", value: "linear:existing-issue-uuid", want: "existing-issue-uuid", ok: true},
		{name: "workspace URL", value: "https://linear.app/sharper-flow/issue/SHA-181", want: "SHA-181", ok: true},
		// The canonical URL Linear renders carries a title slug after the key,
		// and the slug may itself contain slashes.
		{name: "slugged workspace URL", value: "https://linear.app/sharper-flow/issue/CON-52/bugadapter-dispatch-window-rewrite-does-not-bind-the-native-task", want: "CON-52", ok: true},
		{name: "slugged workspace URL with a slash in the slug", value: "https://linear.app/sharper-flow/issue/CON-209/captured-work-items/stay-invisible", want: "CON-209", ok: true},
		{name: "bare issue key", value: "CON-52", want: "CON-52", ok: true},
		{name: "tracker reference", value: "tracker:CON-52", ok: false},
		{name: "non Linear URL", value: "https://example.com/sharper-flow/issue/CON-52", ok: false},
		{name: "GitHub issue URL", value: "https://github.com/Sharper-Flow/concord/issues/703", ok: false},
		{name: "workspace URL without an issue segment", value: "https://linear.app/sharper-flow/project/CON-52", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeLinearIssueExternalRef(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("normalizeLinearIssueExternalRef(%q) = %q, %t; want %q, %t", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestCaptureEnqueuesIssueCreateInTheCaptureTransaction(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "cap-product")
	setupLinearConnectionResource(t, s, "cap-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "cap-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}

	captureLinearFixtureWork(t, s, "cap-work", "cap-product-project", "task", "Capture title", "Capture value statement", "")

	count, opKind := countLinearOutboxRows(t, s, "cap-work")
	if count != 1 || opKind != LinearOpIssueCreate {
		t.Fatalf("capture outbox = %d/%s, want 1/%s", count, opKind, LinearOpIssueCreate)
	}
	var state, productID, title string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state, json_extract(payload, '$.product_id'), json_extract(payload, '$.title') FROM linear_outbox WHERE work_id='cap-work'`).Scan(&state, &productID, &title); err != nil {
		t.Fatal(err)
	}
	if state != LinearOutboxQueued || productID != "cap-product" || title != "Capture title" {
		t.Fatalf("queued operation = %s/%s/%s, want queued/cap-product/Capture title", state, productID, title)
	}
	var linkState string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id='cap-work'`).Scan(&linkState); err != nil {
		t.Fatal(err)
	}
	if linkState != LinearLinkUnpublished {
		t.Fatalf("link state = %s, want unpublished", linkState)
	}
	if invisible := countLinearInvisibleWorkItems(t, s); invisible != 0 {
		t.Fatalf("%d captured items stay invisible to Linear after capture", invisible)
	}
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "cap-product", "cap-work", "adopt-existing-issue"); err == nil || !strings.Contains(err.Error(), "pending Linear link") {
		t.Fatalf("adoption after capture error = %v, want pending Linear link refusal", err)
	}
}

func TestCaptureEnqueueGuardsNoOpWithoutFailingCapture(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "guard-product")
	projectID := "guard-product-project"

	// local_only: the default mode skips the enqueue and the capture succeeds.
	captureLinearFixtureWork(t, s, "guard-local", projectID, "task", "Local title", "Local value", "")
	if count, _ := countLinearOutboxRows(t, s, "guard-local"); count != 0 {
		t.Fatalf("local_only capture queued %d operations, want 0", count)
	}

	// linear_enabled without a declared connection skips the enqueue.
	if _, err := s.SetProductPlanningMode(ctx, "guard-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	captureLinearFixtureWork(t, s, "guard-noconn", projectID, "task", "No connection title", "No connection value", "")
	if count, _ := countLinearOutboxRows(t, s, "guard-noconn"); count != 0 {
		t.Fatalf("capture without a declared connection queued %d operations, want 0", count)
	}

	// A declared connection whose project_ids leave the owning Concord project
	// unmapped skips the enqueue.
	setupLinearConnectionResourceAtVersion(t, s, "guard-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key", "project_ids": map[string]string{"other-project": "linear-project-9"}}}, 3)
	captureLinearFixtureWork(t, s, "guard-unmapped", projectID, "task", "Unmapped title", "Unmapped value", "")
	if count, _ := countLinearOutboxRows(t, s, "guard-unmapped"); count != 0 {
		t.Fatalf("capture with an unmapped project queued %d operations, want 0", count)
	}

	// An external_ref that already names a Linear issue skips the enqueue: a
	// second issue_create would duplicate that card.
	captureLinearFixtureWork(t, s, "guard-extref", projectID, "task", "Extref title", "Extref value", "linear:existing-issue-uuid")
	if count, _ := countLinearOutboxRows(t, s, "guard-extref"); count != 0 {
		t.Fatalf("capture with a Linear external_ref queued %d operations, want 0", count)
	}

	// A mapped connection lets the remaining guards pass and the happy path
	// enqueues; a membership replacement on the same item must not duplicate
	// the issue_create.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "guard-connection-mapped", ResourceID: "linear-conn-guard-product", ProductID: "guard-product",
		TeamID: "team-uuid-1", ProjectIDs: map[string]string{projectID: "linear-project-1"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	captureLinearFixtureWork(t, s, "guard-happy", projectID, "task", "Happy title", "Happy value", "")
	if count, _ := countLinearOutboxRows(t, s, "guard-happy"); count != 1 {
		t.Fatalf("mapped capture queued %d operations, want 1", count)
	}
	replace, err := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: projectID, Role: "primary"}}, ExpectedVersion: 2, ResultingVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{{EventID: "guard-happy-memberships-2", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "guard-happy", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: replace}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "guard-happy"): 2},
	}); err != nil {
		t.Fatal(err)
	}
	if count, _ := countLinearOutboxRows(t, s, "guard-happy"); count != 1 {
		t.Fatalf("membership replacement left %d operations, want 1", count)
	}
}

func TestLinearHealthCountsUnpublishedAfterCapture(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "health-capture-product")
	setupLinearConnectionResource(t, s, "health-capture-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "health-capture-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}

	captureLinearFixtureWork(t, s, "health-capture-work", "health-capture-product-project", "task", "Health title", "Health value", "")

	health, err := s.ReadLinearIntegrationHealth(ctx, "health-capture-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 1 {
		t.Fatalf("outbox depth after capture = %d, want 1", health.OutboxDepth)
	}
	if health.LinkCounts[LinearLinkUnpublished] != 1 {
		t.Fatalf("unpublished link count after capture = %d, want 1", health.LinkCounts[LinearLinkUnpublished])
	}
	if health.OutboxOldestPendingAgeSeconds < 0 {
		t.Fatalf("oldest pending age = %d, want non-negative", health.OutboxOldestPendingAgeSeconds)
	}
}

func TestBackfillCoversEveryUnlinkedNonTerminalItemWithoutLinearIdentity(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "bf-product")
	projectID := "bf-product-project"

	// Capture while the Product is still local_only, so the capture enqueue
	// skips every item and the backfill owns their visibility.
	captureLinearFixtureWork(t, s, "bf-plain", projectID, "task", "Plain title", "Plain value", "")
	captureLinearFixtureWork(t, s, "bf-extref", projectID, "task", "Extref title", "Extref value", "linear:existing-issue-uuid")
	captureLinearFixtureWork(t, s, "bf-url", projectID, "task", "URL title", "URL value", "https://linear.app/sharper-flow/issue/SHA-181")
	captureLinearFixtureWork(t, s, "bf-slug-url", projectID, "task", "Slug URL title", "Slug URL value", "https://linear.app/sharper-flow/issue/CON-52/bugadapter-dispatch-window-rewrite-does-not-bind-the-native-task")
	captureLinearFixtureWork(t, s, "bf-bare", projectID, "task", "Bare key title", "Bare key value", "CON-52")
	captureLinearFixtureWork(t, s, "bf-initiative", projectID, "initiative", "Initiative title", "Initiative value", "")
	captureLinearFixtureWork(t, s, "bf-gap", projectID, "task", "Gap title", "Gap value", "")
	captureLinearFixtureWork(t, s, "bf-terminal", projectID, "task", "Terminal title", "Terminal value", "")
	transition, err := json.Marshal(map[string]any{"from": "needed", "to": "cancelled", "reason": "terminal before backfill", "expected_version": 2, "resulting_version": 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{{EventID: "bf-terminal-cancelled", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "bf-terminal", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: transition}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "bf-terminal"): 2},
	}); err != nil {
		t.Fatal(err)
	}

	// A second local_only Product keeps its items out of this backfill.
	setupProductWithProject(t, s, "bf-local-product", "bf-local-project")
	captureLinearFixtureWork(t, s, "bf-elsewhere", "bf-local-project", "task", "Elsewhere title", "Elsewhere value", "")

	// Enable Linear: declared connection with the owning project mapped.
	setupLinearConnectionResource(t, s, "bf-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "bf-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}

	// An item that already holds a queued operation and link stays untouched.
	if _, err := s.EnqueueLinearIssueForWork(ctx, "bf-plain", LinearOpIssueCreate); err != nil {
		t.Fatal(err)
	}
	before, _ := countLinearOutboxRows(t, s, "bf-plain")

	// local_only refuses loudly: an explicit operator command must not report
	// an empty result for a misconfigured Product.
	if _, err := s.BackfillLinearIssueCreates(ctx, "bf-local-product"); err == nil {
		t.Fatal("backfill on a local_only Product must refuse")
	}

	// The first pass queues exactly the one eligible gap: bf-plain already
	// holds a link, the four external references carry Linear identities in
	// each supported form, bf-initiative is a grouping construct, bf-terminal
	// is terminal, and bf-elsewhere belongs to a local_only Product.
	enqueued, err := s.BackfillLinearIssueCreates(ctx, "bf-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(enqueued) != 1 || enqueued[0].WorkID != "bf-gap" || enqueued[0].OpKind != LinearOpIssueCreate {
		t.Fatalf("backfill = %+v, want exactly bf-gap issue_create", enqueued)
	}
	if count, opKind := countLinearOutboxRows(t, s, "bf-gap"); count != 1 || opKind != LinearOpIssueCreate {
		t.Fatalf("backfill outbox = %d/%s, want 1/%s", count, opKind, LinearOpIssueCreate)
	}
	var linkState string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state FROM linear_issue_links WHERE work_id='bf-gap'`).Scan(&linkState); err != nil {
		t.Fatal(err)
	}
	if linkState != LinearLinkUnpublished {
		t.Fatalf("backfilled link state = %s, want unpublished", linkState)
	}
	after, _ := countLinearOutboxRows(t, s, "bf-plain")
	if after != before {
		t.Fatalf("backfill changed the linked item's operation count from %d to %d", before, after)
	}

	// The completeness invariant holds: no non-terminal, non-initiative item
	// of a linear_enabled Product stays unlinked without a Linear identity.
	if invisible := countLinearInvisibleWorkItems(t, s); invisible != 0 {
		t.Fatalf("invariant violated: %d eligible items remain unlinked after backfill", invisible)
	}

	// A second pass finds nothing left to queue.
	enqueued, err = s.BackfillLinearIssueCreates(ctx, "bf-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(enqueued) != 0 {
		t.Fatalf("second backfill enqueued %d operations, want 0", len(enqueued))
	}
}
