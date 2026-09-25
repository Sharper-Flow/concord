package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// CD-0171: Initiatives map to Linear Projects, repositories map to team
// labels, and the earliest-joined Initiative owns a shared entry. These tests
// drive the enqueue and completion surface; the drain tests in
// cmd/concord cover the remote calls.

// seedLinearWorkOfKind seeds a work item of any kind with primary membership
// in the Product's project.
func seedLinearWorkOfKind(t *testing.T, s *Store, workID, projectID, kind, title, valueStatement string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, ?, ?, 'needed', 0, 'standard', 1, ?, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, workID, kind, title, `{"title":"`+title+`","value_statement":"`+valueStatement+`","kind":"`+kind+`","priority":0,"urgency":"standard"}`); err != nil {
		t.Fatalf("seed work item %s: %v", workID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatalf("seed work membership %s: %v", workID, err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedLinearInitiativeEntry inserts one Initiative entry row directly, in the
// given join order, with fold guards open. The matching includes relation
// rides along, so the seeded projection satisfies the initiative invariants
// an unrelated later operation verifies.
func seedLinearInitiativeEntry(t *testing.T, s *Store, initiative, child string, required bool) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES(?, ?, (SELECT coalesce(max(position)+1, 0) FROM initiative_entries WHERE initiative_work_id=?), ?); INSERT INTO relations(work_id_from, work_id_to, kind, created_at) VALUES(?, ?, 'includes', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`, initiative, child, initiative, boolInt(required), initiative, child); err != nil {
		t.Fatalf("seed entry %s->%s: %v", initiative, child, err)
	}
}

// seedLinearProjectLink records the confirmed Linear Project of one
// Initiative, as the drain completion does.
func seedLinearProjectLink(t *testing.T, s *Store, initiative, remoteUUID string) {
	t.Helper()
	seedLinearProjectLinkWithURL(t, s, initiative, remoteUUID, "")
}

func seedLinearProjectLinkWithURL(t *testing.T, s *Store, initiative, remoteUUID, url string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES(?, ?, ?, ?, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`, initiative, remoteUUID, initiative, url); err != nil {
		t.Fatalf("seed project link %s: %v", initiative, err)
	}
}

func setupLinearLabelConnection(t *testing.T, s *Store, productID string, labels map[string]string) {
	t.Helper()
	setupLinearConnectionResource(t, s, productID, map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"label_ids": labels,
	}})
}

func decodeLinearPayload(t *testing.T, s *Store, operationID string) linearPayload {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload linearPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func enableLinearPlanning(t *testing.T, s *Store, productID string, fromVersion int64) {
	t.Helper()
	if _, err := s.SetProductPlanningMode(context.Background(), productID, PlanningModeLinear, "CD-0171 test", "operator", fromVersion); err != nil {
		t.Fatal(err)
	}
}

func TestLinearIssueSyncsRepositoryLabelInsteadOfProject(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "repolabel-product")
	setupLinearLabelConnection(t, s, "repolabel-product", map[string]string{
		"task": "label-task", "project:repolabel-product-project": "label-repo",
	})
	enableLinearPlanning(t, s, "repolabel-product", 2)
	seedLinearWorkItem(t, s, "repolabel-work", "repolabel-product-project", "Repo label title", "Repo label value")

	op, err := s.EnqueueLinearIssueForWork(ctx, "repolabel-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	var rawPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&rawPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawPayload, "project_id") {
		t.Fatalf("payload = %s, want no project_id field: the Project resolves at drain time", rawPayload)
	}
	payload := decodeLinearPayload(t, s, op.OperationID)
	sort.Strings(payload.LabelIDs)
	if len(payload.LabelIDs) != 2 || payload.LabelIDs[0] != "label-repo" || payload.LabelIDs[1] != "label-task" {
		t.Fatalf("payload labels = %v, want the repository and kind labels", payload.LabelIDs)
	}
}

func TestLinearIssueSyncsOneLabelPerRepositoryMembership(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "span-product")
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			projectCreatedEvent("span-other", "span-secondary-project"),
			membershipEvent("span-secondary-membership", "product_project.added", SubjectProduct, "span-product", map[string]any{
				"product_id": "span-product", "project_id": "span-other", "role": "secondary", "reason": "test",
				"expected_version": 2, "resulting_version": 3,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "span-product"): 2,
			VersionRef(SubjectProject, "span-other"):   0,
		},
	}); err != nil {
		t.Fatalf("add secondary Product project: %v", err)
	}
	setupLinearConnectionResourceAtVersion(t, s, "span-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"label_ids": map[string]string{
			"project:span-product-project": "label-repo-one",
			"project:span-other":           "label-repo-two",
		},
	}}, 3)
	enableLinearPlanning(t, s, "span-product", 3)
	seedLinearWorkItem(t, s, "span-work", "span-product-project", "Span title", "Span value")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO work_projects(work_id, project_id, role) VALUES('span-work', 'span-other', 'secondary'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	op, err := s.EnqueueLinearIssueForWork(ctx, "span-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	payload := decodeLinearPayload(t, s, op.OperationID)
	sort.Strings(payload.LabelIDs)
	if len(payload.LabelIDs) != 2 || payload.LabelIDs[0] != "label-repo-one" || payload.LabelIDs[1] != "label-repo-two" {
		t.Fatalf("payload labels = %v, want one repository label per membership", payload.LabelIDs)
	}
}

func TestLinearIssueCarriesOptionalLabelForNonRequiredEntry(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		required bool
	}{
		{name: "required entry", required: true},
		{name: "optional entry", required: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			ctx := context.Background()
			setupLinearProduct(t, s, "optional-product")
			setupLinearLabelConnection(t, s, "optional-product", map[string]string{
				"task": "label-task", "optional": "label-optional", "project:optional-product-project": "label-repo",
			})
			enableLinearPlanning(t, s, "optional-product", 2)
			seedLinearWorkOfKind(t, s, "optional-initiative", "optional-product-project", "initiative", "Optional initiative", "Initiative value")
			seedLinearWorkItem(t, s, "optional-work", "optional-product-project", "Entry title", "Entry value")
			seedLinearInitiativeEntry(t, s, "optional-initiative", "optional-work", testCase.required)

			op, err := s.EnqueueLinearIssueForWork(ctx, "optional-work", LinearOpIssueCreate)
			if err != nil {
				t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
			}
			payload := decodeLinearPayload(t, s, op.OperationID)
			hasOptional := false
			for _, label := range payload.LabelIDs {
				if label == "label-optional" {
					hasOptional = true
				}
			}
			if hasOptional == testCase.required {
				t.Fatalf("payload labels = %v, optional label present = %v", payload.LabelIDs, hasOptional)
			}
		})
	}
}

func TestLinearIssueProjectFollowsTheEarliestInitiative(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "owner-product")
	setupLinearLabelConnection(t, s, "owner-product", map[string]string{"project:owner-product-project": "label-owner-repo"})
	enableLinearPlanning(t, s, "owner-product", 2)
	seedLinearWorkOfKind(t, s, "owner-first", "owner-product-project", "initiative", "First initiative", "First value")
	seedLinearWorkOfKind(t, s, "owner-second", "owner-product-project", "initiative", "Second initiative", "Second value")
	seedLinearWorkItem(t, s, "owner-work", "owner-product-project", "Shared title", "Shared value")
	// Join order decides ownership, not creation or link order: the shared
	// work item joined owner-first first, while owner-second is the only one
	// whose Project exists.
	seedLinearInitiativeEntry(t, s, "owner-first", "owner-work", true)
	seedLinearInitiativeEntry(t, s, "owner-second", "owner-work", true)
	seedLinearProjectLink(t, s, "owner-second", "remote-project-second")

	op, err := s.EnqueueLinearIssueForWork(ctx, "owner-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	payload := decodeLinearPayload(t, s, op.OperationID)
	if !strings.Contains(payload.Description, "## Initiatives") || !strings.Contains(payload.Description, "Owned by `owner-first`") || !strings.Contains(payload.Description, "Also in Second initiative (`owner-second`).") {
		t.Fatalf("payload description = %q, want the owner and a title reference to the other Initiative", payload.Description)
	}

	// Once the earliest-joined Initiative's Project exists, its remote uuid
	// is what the drains resolve for the issue's Project field at send time.
	seedLinearProjectLink(t, s, "owner-first", "remote-project-first")
	resolved, err := s.ResolveLinearProjectIDForWork(ctx, "owner-work")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() error = %v", err)
	}
	if resolved != "remote-project-first" {
		t.Fatalf("resolved project id = %q, want remote-project-first from the earliest-joined Initiative", resolved)
	}
}

func TestLinearIssueOutsideInitiativeHasNoProject(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "lone-product")
	setupLinearLabelConnection(t, s, "lone-product", map[string]string{"project:lone-product-project": "label-lone-repo"})
	enableLinearPlanning(t, s, "lone-product", 2)
	seedLinearWorkItem(t, s, "lone-work", "lone-product-project", "Lone title", "Lone value")

	op, err := s.EnqueueLinearIssueForWork(ctx, "lone-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	payload := decodeLinearPayload(t, s, op.OperationID)
	if strings.Contains(payload.Description, "## Initiatives") {
		t.Fatalf("payload description = %q, want no Initiatives section (CD-0171 D4)", payload.Description)
	}
	resolved, err := s.ResolveLinearProjectIDForWork(ctx, "lone-work")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() error = %v", err)
	}
	if resolved != "" {
		t.Fatalf("resolved project id = %q, want empty for a work item outside every Initiative (CD-0171 D4)", resolved)
	}
}

// CD-0171 D3 correction: every synced issue carries its repository label, so
// a member Project with no project:<id> mapping refuses the enqueue with the
// typed failure the former repository mapping used, instead of silently
// syncing an issue with no repository label.
func TestLinearIssueEnqueueRefusesUnmappedRepositoryProject(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "unmapped-product")
	setupLinearLabelConnection(t, s, "unmapped-product", map[string]string{"task": "label-task"})

	if _, err := s.SetProductPlanningMode(ctx, "unmapped-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "unmapped-work", "unmapped-product-project", "Unmapped title", "Unmapped value")

	_, err := s.EnqueueLinearIssueForWork(ctx, "unmapped-work", LinearOpIssueCreate)
	if err == nil || !failureKindIs(err, KindInvalidRelation) || !strings.Contains(err.Error(), "unmapped-product-project") {
		t.Fatalf("unmapped repository enqueue error = %v, want invalid_relation naming the project", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox WHERE work_id='unmapped-work'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unmapped repository enqueue queued %d operations, want 0", count)
	}

	// A work item spanning two repositories refuses when either membership
	// lacks its mapping: one unmapped repository label is enough to leave the
	// issue unable to carry its repository identity.
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			projectCreatedEvent("unmapped-other", "unmapped-other-project"),
			membershipEvent("unmapped-secondary-membership", "product_project.added", SubjectProduct, "unmapped-product", map[string]any{
				"product_id": "unmapped-product", "project_id": "unmapped-other", "role": "secondary", "reason": "test",
				"expected_version": 3, "resulting_version": 4,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "unmapped-product"): 3,
			VersionRef(SubjectProject, "unmapped-other"):   0,
		},
	}); err != nil {
		t.Fatalf("add secondary Product project: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO work_projects(work_id, project_id, role) VALUES('unmapped-work', 'unmapped-other', 'secondary'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "unmapped-second-mapping", ResourceID: "linear-conn-unmapped-product", ProductID: "unmapped-product",
		LabelIDs:                map[string]string{"task": "label-task", "project:unmapped-product-project": "label-repo-one"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.EnqueueLinearIssueForWork(ctx, "unmapped-work", LinearOpIssueCreate)
	if err == nil || !failureKindIs(err, KindInvalidRelation) || !strings.Contains(err.Error(), "unmapped-other") {
		t.Fatalf("second unmapped repository enqueue error = %v, want invalid_relation naming unmapped-other", err)
	}
}

// CD-0171 correction: an Initiative has no Linear issue of its own, so the
// issue-enqueue verb on an Initiative routes to its Project operation:
// project_create before the Project exists, project_update afterwards. This
// is the route existing Initiatives use, since only a fresh capture enqueued
// a project_create before.
func TestLinearIssueEnqueueOnAnInitiativeQueuesProjectOperations(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "route-product")
	setupLinearLabelConnection(t, s, "route-product", map[string]string{
		"task": "label-task", "project:route-product-project": "label-repo",
	})
	enableLinearPlanning(t, s, "route-product", 2)
	seedLinearWorkOfKind(t, s, "route-initiative", "route-product-project", "initiative", "Routed initiative", "Routed value")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET narrative='The routed narrative.' WHERE id='route-initiative'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	entry, err := s.EnqueueLinearIssueForProduct(ctx, "route-product", "route-initiative", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForProduct(initiative) error = %v", err)
	}
	if entry.OpKind != LinearOpProjectCreate {
		t.Fatalf("queued op kind = %s, want project_create", entry.OpKind)
	}
	payload := decodeLinearPayload(t, s, entry.OperationID)
	if payload.Title != "Routed initiative" || payload.Description != "Routed value" || payload.Content != "The routed narrative." {
		t.Fatalf("project payload = %+v, want the Initiative title, value statement, and narrative", payload)
	}
	var linkCount int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_issue_links WHERE work_id='route-initiative'`).Scan(&linkCount); err != nil {
		t.Fatal(err)
	}
	if linkCount != 0 {
		t.Fatalf("an Initiative routed to its Project queued %d issue links, want 0", linkCount)
	}

	seedLinearProjectLink(t, s, "route-initiative", "remote-project-routed")
	update, err := s.EnqueueLinearIssueForProduct(ctx, "route-product", "route-initiative", LinearOpIssueUpdate)
	if err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative(update) error = %v", err)
	}
	if update.OpKind != LinearOpProjectUpdate {
		t.Fatalf("queued op kind = %s, want project_update once the Project exists", update.OpKind)
	}
}

// CD-0171 D6 correction: the issue description links the other Initiatives by
// their Linear Project URL when the Project exists, and by title and work id
// when it does not, instead of naming a bare work id nobody can open.
func TestLinearIssueDescriptionLinksOtherInitiativesByProjectURL(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "linkdesc-product")
	setupLinearLabelConnection(t, s, "linkdesc-product", map[string]string{
		"task": "label-task", "project:linkdesc-product-project": "label-repo",
	})
	enableLinearPlanning(t, s, "linkdesc-product", 2)
	seedLinearWorkOfKind(t, s, "linkdesc-first", "linkdesc-product-project", "initiative", "First initiative", "First value")
	seedLinearWorkOfKind(t, s, "linkdesc-second", "linkdesc-product-project", "initiative", "Second initiative", "Second value")
	seedLinearWorkOfKind(t, s, "linkdesc-third", "linkdesc-product-project", "initiative", "Third initiative", "Third value")
	seedLinearWorkItem(t, s, "linkdesc-work", "linkdesc-product-project", "Shared title", "Shared value")
	seedLinearInitiativeEntry(t, s, "linkdesc-first", "linkdesc-work", true)
	seedLinearInitiativeEntry(t, s, "linkdesc-second", "linkdesc-work", true)
	seedLinearInitiativeEntry(t, s, "linkdesc-third", "linkdesc-work", true)
	seedLinearProjectLinkWithURL(t, s, "linkdesc-second", "remote-project-second", "https://linear.app/example/project/remote-project-second")

	op, err := s.EnqueueLinearIssueForWork(ctx, "linkdesc-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	description := decodeLinearPayload(t, s, op.OperationID).Description
	if !strings.Contains(description, "Also in [Second initiative](https://linear.app/example/project/remote-project-second).") {
		t.Fatalf("description = %q, want the linked other Initiative", description)
	}
	if !strings.Contains(description, "Also in Third initiative (`linkdesc-third`).") {
		t.Fatalf("description = %q, want the unlinked other Initiative by title and work id", description)
	}
	if strings.Contains(description, "Also in `linkdesc-second`") || strings.Contains(description, "Also in `linkdesc-third`") {
		t.Fatalf("description = %q, want no other Initiative named by bare work id", description)
	}
}

// A non-required entry carries the optional
// label, so a missing optional mapping refuses the enqueue with the same
// typed failure the repository label uses, instead of silently syncing an
// issue that cannot carry its requiredness.
func TestLinearIssueEnqueueRefusesUnmappedOptionalLabel(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "optmap-product")
	setupLinearLabelConnection(t, s, "optmap-product", map[string]string{
		"task": "label-task", "project:optmap-product-project": "label-repo",
	})
	enableLinearPlanning(t, s, "optmap-product", 2)
	seedLinearWorkOfKind(t, s, "optmap-initiative", "optmap-product-project", "initiative", "Optmap initiative", "Optmap value")
	seedLinearWorkItem(t, s, "optmap-required", "optmap-product-project", "Required title", "Required value")
	seedLinearWorkItem(t, s, "optmap-optional", "optmap-product-project", "Optional title", "Optional value")
	seedLinearInitiativeEntry(t, s, "optmap-initiative", "optmap-required", true)
	seedLinearInitiativeEntry(t, s, "optmap-initiative", "optmap-optional", false)

	// A required entry needs no optional label and enqueues.
	if _, err := s.EnqueueLinearIssueForWork(ctx, "optmap-required", LinearOpIssueCreate); err != nil {
		t.Fatalf("required entry enqueue error = %v", err)
	}
	// The optional entry cannot sync without its mandated label.
	_, err := s.EnqueueLinearIssueForWork(ctx, "optmap-optional", LinearOpIssueCreate)
	if err == nil || !failureKindIs(err, KindInvalidRelation) || !strings.Contains(err.Error(), `"optional" Linear label mapping`) {
		t.Fatalf("unmapped optional enqueue error = %v, want invalid_relation naming the optional mapping", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox WHERE work_id='optmap-optional'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unmapped optional enqueue queued %d operations, want 0", count)
	}

	// Mapping the optional label repairs the configuration, and the next
	// enqueue carries it.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "optmap-optional-mapping", ResourceID: "linear-conn-optmap-product", ProductID: "optmap-product",
		LabelIDs:                map[string]string{"task": "label-task", "project:optmap-product-project": "label-repo", "optional": "label-optional"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	op, err := s.EnqueueLinearIssueForWork(ctx, "optmap-optional", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("mapped optional enqueue error = %v", err)
	}
	payload := decodeLinearPayload(t, s, op.OperationID)
	hasOptional := false
	for _, label := range payload.LabelIDs {
		if label == "label-optional" {
			hasOptional = true
		}
	}
	if !hasOptional {
		t.Fatalf("payload labels = %v, want the optional label (CD-0171 D5)", payload.LabelIDs)
	}
}

// The drain resolves the Project before its remote
// call, so the project_create can complete inside the drain-to-completion
// window, where its entry refresh cannot see the issue's unconfirmed link.
// CompleteLinearIssueOperation re-reads the owning Initiative's link inside
// the completion transaction and queues the converging update when the sent
// Project differs. The store serializes transactions, so each interleaving
// leaves exactly one converging update.
func TestCompleteLinearIssueOperationClosesTheProjectRace(t *testing.T) {
	setup := func(t *testing.T) *Store {
		t.Helper()
		s := openTemp(t)
		setupLinearProduct(t, s, "raceres-product")
		setupLinearLabelConnection(t, s, "raceres-product", map[string]string{"project:raceres-product-project": "label-race-repo"})
		enableLinearPlanning(t, s, "raceres-product", 2)
		seedLinearWorkOfKind(t, s, "raceres-initiative", "raceres-product-project", "initiative", "Race initiative", "Race value")
		seedLinearWorkItem(t, s, "raceres-entry", "raceres-product-project", "Race title", "Race value")
		seedLinearInitiativeEntry(t, s, "raceres-initiative", "raceres-entry", true)
		return s
	}
	enqueueAndClaimIssue := func(t *testing.T, s *Store) ClaimedLinearOperation {
		t.Helper()
		op, err := s.EnqueueLinearIssueForWork(context.Background(), "raceres-entry", LinearOpIssueCreate)
		if err != nil {
			t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		return op
	}
	identity := LinearRemoteIdentity{RemoteUUID: "race-issue-remote", HumanKey: "RA-1", URL: "https://linear.app/example/issue/RA-1"}
	queuedUpdates := func(t *testing.T, s *Store) []linearPayload {
		t.Helper()
		rows, err := s.DatabaseForTesting().Query(`SELECT payload FROM linear_outbox WHERE work_id='raceres-entry' AND op_kind=? AND state=? ORDER BY rowid`, LinearOpIssueUpdate, LinearOutboxQueued)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var payloads []linearPayload
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var payload linearPayload
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, payload)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return payloads
	}

	t.Run("project completes while the issue drains", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		op := enqueueAndClaimIssue(t, s)
		// The project_create completes first: its link lands, and the drain's
		// entry refresh skips this entry because the issue link is still not
		// confirmed.
		projectOp, err := s.EnqueueLinearProjectForInitiative(context.Background(), "raceres-product", "raceres-initiative", LinearOpProjectCreate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteLinearProjectOperation(context.Background(), projectOp.OperationID, "remote-project-race", "Race initiative", "", LinearInitiativeProjectState{Title: "Race initiative", ValueStatement: "Race value"}); err != nil {
			t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
		}
		// The issue create lands with no Project and completes: the
		// completion transaction sees the confirmed link and queues the
		// converging update.
		if err := s.CompleteLinearIssueOperation(context.Background(), op.OperationID, identity, ""); err != nil {
			t.Fatalf("CompleteLinearIssueOperation() error = %v", err)
		}
		payloads := queuedUpdates(t, s)
		if len(payloads) != 1 {
			t.Fatalf("converging updates = %+v, want exactly one: the Project resolves at the update's send time", payloads)
		}
	})

	t.Run("issue completes before the project", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		op := enqueueAndClaimIssue(t, s)
		// No link exists yet, so the completion compares nothing and queues
		// no update.
		if err := s.CompleteLinearIssueOperation(context.Background(), op.OperationID, identity, ""); err != nil {
			t.Fatalf("CompleteLinearIssueOperation() error = %v", err)
		}
		if payloads := queuedUpdates(t, s); len(payloads) != 0 {
			t.Fatalf("updates before the Project exists = %+v, want none", payloads)
		}
		// The project_create completes now: the completion transaction sees
		// the confirmed link this completion just wrote and queues the
		// converging update inside its own commit.
		projectOp, err := s.EnqueueLinearProjectForInitiative(context.Background(), "raceres-product", "raceres-initiative", LinearOpProjectCreate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteLinearProjectOperation(context.Background(), projectOp.OperationID, "remote-project-race", "Race initiative", "", LinearInitiativeProjectState{Title: "Race initiative", ValueStatement: "Race value"}); err != nil {
			t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
		}
		if payloads := queuedUpdates(t, s); len(payloads) != 1 {
			t.Fatalf("converging updates = %+v, want exactly one: the Project resolves at the update's send time", payloads)
		}
	})

	t.Run("equal project needs no rescue", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		op := enqueueAndClaimIssue(t, s)
		// The project_create completed before the drain resolved the Project,
		// so the create carried it and the completion must not queue another
		// update.
		projectOp, err := s.EnqueueLinearProjectForInitiative(context.Background(), "raceres-product", "raceres-initiative", LinearOpProjectCreate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteLinearProjectOperation(context.Background(), projectOp.OperationID, "remote-project-race", "Race initiative", "", LinearInitiativeProjectState{Title: "Race initiative", ValueStatement: "Race value"}); err != nil {
			t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
		}
		if err := s.CompleteLinearIssueOperation(context.Background(), op.OperationID, identity, "remote-project-race"); err != nil {
			t.Fatalf("CompleteLinearIssueOperation() error = %v", err)
		}
		if payloads := queuedUpdates(t, s); len(payloads) != 0 {
			t.Fatalf("updates after an equal completion = %+v, want none", payloads)
		}
	})

	t.Run("no Initiative needs no rescue", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		seedLinearWorkItem(t, s, "raceres-lone", "raceres-product-project", "Lone title", "Lone value")
		op, err := s.EnqueueLinearIssueForWork(context.Background(), "raceres-lone", LinearOpIssueCreate)
		if err != nil {
			t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteLinearIssueOperation(context.Background(), op.OperationID, LinearRemoteIdentity{RemoteUUID: "lone-issue-remote", HumanKey: "RA-2"}, ""); err != nil {
			t.Fatalf("CompleteLinearIssueOperation() error = %v", err)
		}
		rows, err := s.DatabaseForTesting().Query(`SELECT 1 FROM linear_outbox WHERE work_id='raceres-lone' AND op_kind=?`, LinearOpIssueUpdate)
		if err != nil {
			t.Fatal(err)
		}
		if rows.Next() {
			rows.Close()
			t.Fatal("a work item outside every Initiative queued a converging update")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	})
}

// An entry change between the issue enqueue and
// its drain cannot refresh the still-unpublished link, so the drain sends the
// stale snapshot. The completion re-derives the full desired state — Project,
// labels, description — and queues exactly one converging update.
func TestCompleteLinearIssueOperationConvergesAnEntryChangeAfterEnqueue(t *testing.T) {
	queuedConvergingUpdates := func(t *testing.T, s *Store, workID string) []linearPayload {
		t.Helper()
		rows, err := s.DatabaseForTesting().Query(`SELECT payload FROM linear_outbox WHERE work_id=? AND op_kind=? AND state=? ORDER BY rowid`, workID, LinearOpIssueUpdate, LinearOutboxQueued)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var payloads []linearPayload
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var payload linearPayload
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, payload)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return payloads
	}

	t.Run("the optional flag flips after enqueue", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		ctx := context.Background()
		setupLinearProduct(t, s, "conv-product")
		setupLinearLabelConnection(t, s, "conv-product", map[string]string{
			"task": "label-task", "optional": "label-optional", "project:conv-product-project": "label-conv-repo",
		})
		enableLinearPlanning(t, s, "conv-product", 2)
		seedLinearWorkOfKind(t, s, "conv-initiative", "conv-product-project", "initiative", "Convergence initiative", "Convergence value")
		seedLinearWorkItem(t, s, "conv-entry", "conv-product-project", "Conv title", "Conv value")
		seedLinearInitiativeEntry(t, s, "conv-initiative", "conv-entry", true)

		op, err := s.EnqueueLinearIssueForWork(ctx, "conv-entry", LinearOpIssueCreate)
		if err != nil {
			t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		// The entry turns optional while the create is in flight: the
		// entry-change refresh skips the unpublished link, so the completion
		// is the only writer that can still converge the labels.
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE initiative_entries SET required=0 WHERE initiative_work_id='conv-initiative' AND child_work_id='conv-entry'; DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteLinearIssueOperation(ctx, op.OperationID, LinearRemoteIdentity{RemoteUUID: "conv-issue-remote", HumanKey: "CV-1"}, ""); err != nil {
			t.Fatalf("CompleteLinearIssueOperation() error = %v", err)
		}
		payloads := queuedConvergingUpdates(t, s, "conv-entry")
		if len(payloads) != 1 {
			t.Fatalf("converging updates = %d, want exactly one", len(payloads))
		}
		hasOptional := false
		for _, labelID := range payloads[0].LabelIDs {
			if labelID == "label-optional" {
				hasOptional = true
			}
		}
		if !hasOptional {
			t.Fatalf("converging labels = %v, want the optional label (CD-0171 D5)", payloads[0].LabelIDs)
		}
	})

	t.Run("a second Initiative joins after enqueue", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		ctx := context.Background()
		setupLinearProduct(t, s, "conv2-product")
		setupLinearLabelConnection(t, s, "conv2-product", map[string]string{
			"task": "label-task", "project:conv2-product-project": "label-conv-repo",
		})
		enableLinearPlanning(t, s, "conv2-product", 2)
		seedLinearWorkOfKind(t, s, "conv2-initiative", "conv2-product-project", "initiative", "First initiative", "First value")
		seedLinearWorkOfKind(t, s, "conv2-second", "conv2-product-project", "initiative", "Second initiative", "Second value")
		seedLinearWorkItem(t, s, "conv2-entry", "conv2-product-project", "Shared title", "Shared value")
		seedLinearInitiativeEntry(t, s, "conv2-initiative", "conv2-entry", true)

		op, err := s.EnqueueLinearIssueForWork(ctx, "conv2-entry", LinearOpIssueCreate)
		if err != nil {
			t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
		}
		if _, err := s.ClaimLinearOperations(context.Background(), 25); err != nil {
			t.Fatal(err)
		}
		// The second Initiative joins while the create is in flight: the
		// description the drain sent names no second Initiative, so the
		// completion queues the converging update that mentions it.
		seedLinearInitiativeEntry(t, s, "conv2-second", "conv2-entry", true)
		if err := s.CompleteLinearIssueOperation(ctx, op.OperationID, LinearRemoteIdentity{RemoteUUID: "conv2-issue-remote", HumanKey: "C2-1"}, ""); err != nil {
			t.Fatalf("CompleteLinearIssueOperation() error = %v", err)
		}
		payloads := queuedConvergingUpdates(t, s, "conv2-entry")
		if len(payloads) != 1 {
			t.Fatalf("converging updates = %d, want exactly one", len(payloads))
		}
		if !strings.Contains(payloads[0].Description, "Also in Second initiative (`conv2-second`)") {
			t.Fatalf("converging description = %q, want the second Initiative mention (CD-0171 D6)", payloads[0].Description)
		}
	})
}

func TestLinearProjectCreateEnqueueCarriesValueAndNarrative(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "projinit-product")
	setupLinearLabelConnection(t, s, "projinit-product", map[string]string{})
	enableLinearPlanning(t, s, "projinit-product", 2)
	seedLinearWorkOfKind(t, s, "projinit-initiative", "projinit-product-project", "initiative", "Initiative title", "Initiative value statement")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET narrative='The coordination narrative.' WHERE id='projinit-initiative'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	op, err := s.EnqueueLinearProjectForInitiative(ctx, "projinit-product", "projinit-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative() error = %v", err)
	}
	payload := decodeLinearPayload(t, s, op.OperationID)
	if payload.Description != "Initiative value statement" {
		t.Fatalf("payload description = %q, want the value statement (CD-0171 d3)", payload.Description)
	}
	if payload.Content != "The coordination narrative." {
		t.Fatalf("payload content = %q, want the narrative (CD-0171 d3)", payload.Content)
	}
	if payload.Title != "Initiative title" || payload.TeamID != "team-uuid-1" || payload.ClientUUID == "" || payload.ProductID != "projinit-product" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(payload.ClientUUID) != 36 || !strings.Contains(payload.ClientUUID, "-") {
		t.Fatalf("client uuid %q is not a UUID for ProjectCreateInput.id (CD-0171 d2)", payload.ClientUUID)
	}

	// A non-Initiative refuses.
	seedLinearWorkItem(t, s, "projinit-task", "projinit-product-project", "Plain task", "Task value")
	if _, err := s.EnqueueLinearProjectForInitiative(ctx, "projinit-product", "projinit-task", LinearOpProjectCreate); err == nil || !failureKindIs(err, KindInitiativeScopeViolation) {
		t.Fatalf("non-initiative error = %v, want initiative_scope_violation", err)
	}
	// An unknown kind refuses.
	if _, err := s.EnqueueLinearProjectForInitiative(ctx, "projinit-product", "projinit-initiative", "project_delete"); err == nil || !failureKindIs(err, KindInvalidPayload) {
		t.Fatalf("unknown kind error = %v, want invalid_payload", err)
	}
}

func TestLinearProjectUpdateRequiresCreatedProject(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "projupd-product")
	setupLinearLabelConnection(t, s, "projupd-product", map[string]string{})
	enableLinearPlanning(t, s, "projupd-product", 2)
	seedLinearWorkOfKind(t, s, "projupd-initiative", "projupd-product-project", "initiative", "Update initiative", "Update value")

	if _, err := s.EnqueueLinearProjectForInitiative(ctx, "projupd-product", "projupd-initiative", LinearOpProjectUpdate); err == nil || !strings.Contains(err.Error(), "no created Linear Project") {
		t.Fatalf("update-before-create error = %v, want typed refusal", err)
	}
	seedLinearProjectLink(t, s, "projupd-initiative", "remote-project-created")
	if _, err := s.EnqueueLinearProjectForInitiative(ctx, "projupd-product", "projupd-initiative", LinearOpProjectUpdate); err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative(update) error = %v", err)
	}
}

func TestCompleteLinearProjectOperationRecordsLinkOnce(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "projdone-product")
	setupLinearLabelConnection(t, s, "projdone-product", map[string]string{})
	enableLinearPlanning(t, s, "projdone-product", 2)
	seedLinearWorkOfKind(t, s, "projdone-initiative", "projdone-product-project", "initiative", "Done initiative", "Done value")

	entry, err := s.EnqueueLinearProjectForInitiative(ctx, "projdone-product", "projdone-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteLinearProjectOperation(ctx, entry.OperationID, "remote-project-uuid-1", "Done initiative", "https://linear.app/example/project/remote-project-uuid-1", LinearInitiativeProjectState{Title: "Done initiative", ValueStatement: "Done value"}); err != nil {
		t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
	}
	link, err := s.ReadLinearProjectLink(ctx, "projdone-initiative")
	if err != nil {
		t.Fatalf("ReadLinearProjectLink() error = %v", err)
	}
	if link.RemoteProjectUUID != "remote-project-uuid-1" || link.Name != "Done initiative" || link.URL != "https://linear.app/example/project/remote-project-uuid-1" {
		t.Fatalf("project link = %+v", link)
	}
	// Completing again is a typed refusal: the operation left in_flight.
	if err := s.CompleteLinearProjectOperation(ctx, entry.OperationID, "remote-project-uuid-2", "Done initiative", "", LinearInitiativeProjectState{}); err == nil || !failureKindIs(err, KindInvalidTransition) {
		t.Fatalf("re-completion error = %v, want invalid_transition", err)
	}
	if _, err := s.ReadLinearProjectLink(ctx, "ghost-initiative"); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("missing link error = %v, want unknown_scope", err)
	}
}

// One Linear Project per Initiative, so a repeated
// project_create never mints a second remote Project. While a create is
// queued or in flight the enqueue returns that operation; once the Project
// link exists the create addresses it with a project_update instead.
func TestLinearProjectCreateEnqueueIsIdempotentPerInitiative(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "projidem-product")
	setupLinearLabelConnection(t, s, "projidem-product", map[string]string{})
	enableLinearPlanning(t, s, "projidem-product", 2)
	seedLinearWorkOfKind(t, s, "projidem-initiative", "projidem-product-project", "initiative", "Idem initiative", "Idem value")

	first, err := s.EnqueueLinearProjectForInitiative(ctx, "projidem-product", "projidem-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("first EnqueueLinearProjectForInitiative() error = %v", err)
	}
	second, err := s.EnqueueLinearProjectForInitiative(ctx, "projidem-product", "projidem-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("repeated EnqueueLinearProjectForInitiative() error = %v", err)
	}
	if second.OperationID != first.OperationID {
		t.Fatalf("repeat create queued operation %s, want the queued operation %s returned", second.OperationID, first.OperationID)
	}
	var createCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='projidem-initiative' AND op_kind=?`, LinearOpProjectCreate).Scan(&createCount); err != nil {
		t.Fatal(err)
	}
	if createCount != 1 {
		t.Fatalf("project_create rows = %d, want 1: a repeat enqueue must not mint a second Project", createCount)
	}

	// Once the create completes, a repeated create addresses the existing
	// Project with an update instead of creating another one.
	if _, err := s.ClaimLinearOperations(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteLinearProjectOperation(ctx, first.OperationID, "remote-project-idem", "Idem initiative", "", LinearInitiativeProjectState{Title: "Idem initiative", ValueStatement: "Idem value"}); err != nil {
		t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
	}
	// The sent state equaled the stored state, so the completion queued no
	// rescue update: the create row is the only one the Initiative holds.
	var rowCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='projidem-initiative'`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("outbox rows after an equal-state completion = %d, want 1", rowCount)
	}
	third, err := s.EnqueueLinearProjectForInitiative(ctx, "projidem-product", "projidem-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("post-completion EnqueueLinearProjectForInitiative() error = %v", err)
	}
	if third.OpKind != LinearOpProjectUpdate {
		t.Fatalf("create after completion queued %s, want project_update addressing the existing Project", third.OpKind)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='projidem-initiative' AND op_kind=?`, LinearOpProjectCreate).Scan(&createCount); err != nil {
		t.Fatal(err)
	}
	if createCount != 1 {
		t.Fatalf("project_create rows after completion = %d, want 1", createCount)
	}
}

// A failed project_create keeps its client UUID —
// the Initiative's stable Project identity — so a re-enqueue revives the same
// operation with a refreshed payload instead of minting a second Linear
// Project for one Initiative.
func TestFailedProjectCreateReenqueueRevivesTheSameOperation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "projfail-product")
	setupLinearLabelConnection(t, s, "projfail-product", map[string]string{})
	enableLinearPlanning(t, s, "projfail-product", 2)
	seedLinearWorkOfKind(t, s, "projfail-initiative", "projfail-product-project", "initiative", "Fail initiative", "Fail value")

	first, err := s.EnqueueLinearProjectForInitiative(ctx, "projfail-product", "projfail-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("first EnqueueLinearProjectForInitiative() error = %v", err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 25); err != nil {
		t.Fatal(err)
	}
	// The drain fails the create permanently after an ambiguous remote
	// success. A retryable failure requeues the same row; a fresh enqueue
	// does not reuse a permanently failed row — the revive path owns it.
	if err := s.FailLinearOperation(ctx, first.OperationID, "permanent", "ambiguous remote success"); err != nil {
		t.Fatalf("FailLinearOperation() error = %v", err)
	}
	// The Initiative moves on while the create sits failed; the revive
	// carries the current state.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET narrative='The revived narrative.' WHERE id='projfail-initiative'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	second, err := s.EnqueueLinearProjectForInitiative(ctx, "projfail-product", "projfail-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("re-enqueue after failure error = %v", err)
	}
	if second.IdempotencyKey != first.IdempotencyKey {
		t.Fatalf("re-enqueue client UUID %s, want the failed create's %s", second.IdempotencyKey, first.IdempotencyKey)
	}
	if second.OperationID != first.OperationID {
		t.Fatalf("re-enqueue operation %s, want the failed row %s revived", second.OperationID, first.OperationID)
	}
	var rowCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='projfail-initiative'`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("outbox rows = %d, want the same single row revived", rowCount)
	}
	var state string
	var attempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT state, attempts FROM linear_outbox WHERE operation_id=?`, first.OperationID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != LinearOutboxQueued || attempts != 0 {
		t.Fatalf("revived row state=%s attempts=%d, want queued with attempts reset", state, attempts)
	}
	payload := decodeLinearPayload(t, s, second.OperationID)
	if payload.Content != "The revived narrative." {
		t.Fatalf("revived payload content = %q, want the current narrative", payload.Content)
	}
}

// A membership change after capture must converge
// a confirmed Linear issue's repository labels with its new Project
// memberships — the capture fold alone handles only the version-1 enqueue.
func TestMembershipChangeConvergesLinearIssueRepositoryLabels(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "memwork-product")
	setupLinearLabelConnection(t, s, "memwork-product", map[string]string{
		"project:memwork-product-project": "label-repo-a", "project:memwork-other-project": "label-repo-b",
	})
	enableLinearPlanning(t, s, "memwork-product", 2)
	seedLinearWorkItem(t, s, "memwork-item", "memwork-product-project", "Mem work title", "Mem work value")
	// The Product gains a second repository so the membership change can
	// widen the issue's label set.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{projectCreatedEvent("memwork-other-project", "memwork-other-created"), membershipEvent("memwork-other-membership", "product_project.added", SubjectProduct, "memwork-product", map[string]any{"product_id": "memwork-product", "project_id": "memwork-other-project", "role": "secondary", "reason": "test", "expected_version": 3, "resulting_version": 4})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "memwork-other-project"): 0, VersionRef(SubjectProduct, "memwork-product"): 3}}); err != nil {
		t.Fatalf("second project setup: %v", err)
	}
	// The item's issue is already confirmed on Linear.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('memwork-item', 'memwork-remote', 'MW-1', '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	queuedUpdateCount := func(t *testing.T) int {
		t.Helper()
		var queued int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='memwork-item' AND op_kind=?`, LinearOpIssueUpdate).Scan(&queued); err != nil {
			t.Fatal(err)
		}
		return queued
	}
	replaceMemberships := func(eventID string, expected, resulting int64, memberships []map[string]any) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"memberships": memberships, "expected_version": expected, "resulting_version": resulting})
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: eventID, Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "memwork-item", Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "memwork-item"): expected}}); err != nil {
			t.Fatalf("memberships_replaced(%s): %v", eventID, err)
		}
	}

	// The version-1 capture fold holds a confirmed link, so it records no
	// create and no update.
	replaceMemberships("memwork-mem-1", 1, 2, []map[string]any{{"project_id": "memwork-product-project", "role": "primary"}})
	if queued := queuedUpdateCount(t); queued != 0 {
		t.Fatalf("queued updates after the capture fold = %d, want 0", queued)
	}
	// The later membership change converges the confirmed issue's labels.
	replaceMemberships("memwork-mem-2", 2, 3, []map[string]any{
		{"project_id": "memwork-product-project", "role": "primary"},
		{"project_id": "memwork-other-project", "role": "secondary"},
	})
	if queued := queuedUpdateCount(t); queued != 1 {
		t.Fatalf("queued updates after the membership change = %d, want exactly one", queued)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE work_id='memwork-item' AND op_kind=?`, LinearOpIssueUpdate).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "label-repo-b") {
		t.Fatalf("update payload = %s, want the new repository's label", raw)
	}
}

func TestProjectCreateCompletionRefreshesEntryIssues(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "refresh-product")
	setupLinearLabelConnection(t, s, "refresh-product", map[string]string{"project:refresh-product-project": "label-refresh-repo"})
	enableLinearPlanning(t, s, "refresh-product", 2)
	seedLinearWorkOfKind(t, s, "refresh-initiative", "refresh-product-project", "initiative", "Refresh initiative", "Refresh value")
	seedLinearWorkItem(t, s, "refresh-entry", "refresh-product-project", "Entry title", "Entry value")
	seedLinearInitiativeEntry(t, s, "refresh-initiative", "refresh-entry", true)
	// One confirmed entry issue and one without any link.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('refresh-entry', 'remote-issue-1', 'EX-1', '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	entry, err := s.EnqueueLinearProjectForInitiative(ctx, "refresh-product", "refresh-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative() error = %v", err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 25); err != nil {
		t.Fatal(err)
	}
	// The completion queues the confirmed entry's update inside its own
	// transaction: no separate refresh call exists to fail after the Project
	// exists.
	if err := s.CompleteLinearProjectOperation(ctx, entry.OperationID, "remote-project-refresh", "Refresh initiative", "", LinearInitiativeProjectState{Title: "Refresh initiative", ValueStatement: "Refresh value"}); err != nil {
		t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
	}
	var operationID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT operation_id FROM linear_outbox WHERE work_id='refresh-entry' AND op_kind=? AND state=?`, LinearOpIssueUpdate, LinearOutboxQueued).Scan(&operationID); err != nil {
		t.Fatalf("the completion queued no entry update: %v", err)
	}
	var rawRefresh string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&rawRefresh); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawRefresh, "project_id") {
		t.Fatalf("refresh payload = %s, want no project_id field: the update resolves the Project at send time", rawRefresh)
	}
}

func TestNarrativeRevisionFoldEnqueuesProjectUpdate(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "narr-product")
	setupLinearLabelConnection(t, s, "narr-product", map[string]string{})
	enableLinearPlanning(t, s, "narr-product", 2)
	seedLinearWorkOfKind(t, s, "narr-initiative", "narr-product-project", "initiative", "Narrative initiative", "Narrative value")
	seedLinearProjectLink(t, s, "narr-initiative", "remote-project-narrative")

	event, err := InitiativeNarrativeEvent("narr-revise-1", "narr-initiative", "The revised narrative.", "sync to Linear", "operator", time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "narr-initiative"): 1},
	}); err != nil {
		t.Fatalf("ApplyOperation(narrative_revised) error = %v", err)
	}
	var opKind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT op_kind FROM linear_outbox WHERE work_id='narr-initiative' ORDER BY rowid DESC LIMIT 1`).Scan(&opKind); err != nil {
		t.Fatalf("narrative revision queued no project_update: %v", err)
	}
	if opKind != LinearOpProjectUpdate {
		t.Fatalf("queued op kind = %s, want project_update", opKind)
	}
}

func TestLinearLabelMappingAcceptsProjectAndOptionalKeys(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "labelkey-product")
	setupLinearLabelConnection(t, s, "labelkey-product", map[string]string{
		"optional": "label-optional", "project:labelkey-product-project": "label-repo",
	})
	connection, err := s.ReadLinearConnection(ctx, "labelkey-product")
	if err != nil {
		t.Fatalf("read with project and optional keys error = %v", err)
	}
	if connection.LabelIDs["optional"] != "label-optional" || connection.LabelIDs["project:labelkey-product-project"] != "label-repo" {
		t.Fatalf("connection labels = %v", connection.LabelIDs)
	}

	// A project key naming an unregistered Concord project refuses on write.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "labelkey-unknown", ResourceID: connection.ResourceID, ProductID: "labelkey-product",
		LabelIDs:                map[string]string{"project:ghost-project": "label-ghost"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC),
	}); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unregistered project key error = %v, want unknown_scope", err)
	}
	// An unrecognized key still refuses.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "labelkey-bad", ResourceID: connection.ResourceID, ProductID: "labelkey-product",
		LabelIDs:                map[string]string{"repository": "label-ghost"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC),
	}); err == nil || !failureKindIs(err, KindInvalidPayload) {
		t.Fatalf("unknown key error = %v, want invalid_payload", err)
	}
	// A registered project key updates cleanly.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "labelkey-good", ResourceID: connection.ResourceID, ProductID: "labelkey-product",
		LabelIDs:                map[string]string{"project:labelkey-product-project": "label-repo-two"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("registered project key update error = %v", err)
	}
}

// A Linear configuration gap — an unmapped project:<id> or optional label —
// never refuses a local fold. A stored connection can predate the label keys
// a sync needs, so a refusal here would wedge every local transition of a
// Linear-linked work item until the operator maps the labels. The explicit
// enqueue verb and the drain keep reporting the gap
// (TestLinearIssueEnqueueRefusesUnmappedRepositoryProject).
func TestLinearConfigurationGapNeverRefusesLocalFolds(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "foldgap-product")
	setupLinearLabelConnection(t, s, "foldgap-product", map[string]string{"task": "label-task"})
	enableLinearPlanning(t, s, "foldgap-product", 2)
	seedLinearWorkOfKind(t, s, "foldgap-initiative", "foldgap-product-project", "initiative", "Fold gap initiative", "Fold gap value")
	seedLinearWorkItem(t, s, "foldgap-transition", "foldgap-product-project", "Transition title", "Transition value")
	seedLinearWorkItem(t, s, "foldgap-entry-child", "foldgap-product-project", "Entry child title", "Entry child value")
	for _, workID := range []string{"foldgap-transition", "foldgap-entry-child"} {
		for _, state := range []string{LinearLinkUnpublished, LinearLinkPending, LinearLinkConfirmed} {
			if err := s.RecordLinearLink(ctx, workID, "remote-"+workID, "", "", "", "", state); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The lifecycle transition succeeds and queues no operation.
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "foldgap-work-in-progress", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "foldgap-transition", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"needed","to":"in_progress","reason":"start execution","expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "foldgap-transition"): 1}}); err != nil {
		t.Fatalf("transition with an unmapped repository label error = %v", err)
	}
	// The Initiative entry add succeeds and queues no operation.
	event, err := InitiativeEntryEvent("foldgap-add-1", "initiative_entry.added", "foldgap-initiative", InitiativeEntry{ChildWorkID: "foldgap-entry-child", Position: 0, Required: true}, "operator", time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "foldgap-initiative"): 1},
	}); err != nil {
		t.Fatalf("entry add with an unmapped repository label error = %v", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox WHERE work_id IN ('foldgap-transition','foldgap-entry-child')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("configuration gaps queued %d operations, want 0", count)
	}
	// The explicit enqueue verb still reports the gap.
	_, err = s.EnqueueLinearIssueForWork(ctx, "foldgap-transition", LinearOpIssueUpdate)
	if err == nil || !failureKindIs(err, KindInvalidRelation) || !strings.Contains(err.Error(), "foldgap-product-project") {
		t.Fatalf("explicit enqueue error = %v, want invalid_relation naming the project", err)
	}
}

func TestEntryAddedFoldEnqueuesIssueUpdateForConfirmedIssue(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "entryhook-product")
	setupLinearLabelConnection(t, s, "entryhook-product", map[string]string{"optional": "label-optional", "project:entryhook-product-project": "label-entryhook-repo"})
	enableLinearPlanning(t, s, "entryhook-product", 2)
	seedLinearWorkOfKind(t, s, "entryhook-initiative", "entryhook-product-project", "initiative", "Entry hook initiative", "Entry hook value")
	seedLinearWorkItem(t, s, "entryhook-child", "entryhook-product-project", "Child title", "Child value")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('entryhook-child', 'child-issue-uuid-1', 'EX-9', '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	// The child's issue is already confirmed, and the Product's connection
	// declares a full status mapping, so the entry-added fold queues the
	// issue_update that re-labels the issue and re-points its Project.
	event, err := InitiativeEntryEvent("entryhook-add-1", "initiative_entry.added", "entryhook-initiative", InitiativeEntry{ChildWorkID: "entryhook-child", Position: 0, Required: false}, "operator", time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "entryhook-initiative"): 1},
	}); err != nil {
		t.Fatalf("ApplyOperation(initiative_entry.added) error = %v", err)
	}
	var opKind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT op_kind FROM linear_outbox WHERE work_id='entryhook-child'`).Scan(&opKind); err != nil {
		t.Fatalf("entry added queued no issue update: %v", err)
	}
	if opKind != LinearOpIssueUpdate {
		t.Fatalf("queued op kind = %s, want issue_update", opKind)
	}
	payload := decodeLinearPayload(t, s, func(t *testing.T, s *Store) string {
		var operationID string
		if err := s.DatabaseForTesting().QueryRow(`SELECT operation_id FROM linear_outbox WHERE work_id='entryhook-child'`).Scan(&operationID); err != nil {
			t.Fatal(err)
		}
		return operationID
	}(t, s))
	hasOptional := false
	for _, label := range payload.LabelIDs {
		if label == "label-optional" {
			hasOptional = true
		}
	}
	if !hasOptional {
		t.Fatalf("payload labels = %v, want the optional label from the non-required entry (CD-0171 D5)", payload.LabelIDs)
	}
}

// The completion compares the Initiative state the
// drain sent against the stored state. A revision that lands inside the
// drain-to-completion window queues one project_update, so it is never lost
// behind the create.
func TestCompleteLinearProjectOperationQueuesUpdateWhenStateMovedOn(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "projrace-product")
	setupLinearLabelConnection(t, s, "projrace-product", map[string]string{})
	enableLinearPlanning(t, s, "projrace-product", 2)
	seedLinearWorkOfKind(t, s, "projrace-initiative", "projrace-product-project", "initiative", "Race initiative", "Race value")

	entry, err := s.EnqueueLinearProjectForInitiative(ctx, "projrace-product", "projrace-initiative", LinearOpProjectCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 25); err != nil {
		t.Fatal(err)
	}
	// The narrative moves on after the drain read the state but before the
	// completion commits, so the create shipped the stale content.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET narrative='The late revision.' WHERE id='projrace-initiative'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteLinearProjectOperation(ctx, entry.OperationID, "remote-project-race", "Race initiative", "", LinearInitiativeProjectState{Title: "Race initiative", ValueStatement: "Race value"}); err != nil {
		t.Fatalf("CompleteLinearProjectOperation() error = %v", err)
	}
	var opKind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT op_kind FROM linear_outbox WHERE work_id='projrace-initiative' AND state=?`, LinearOutboxQueued).Scan(&opKind); err != nil {
		t.Fatalf("the completion queued no rescue update for the moved-on narrative: %v", err)
	}
	if opKind != LinearOpProjectUpdate {
		t.Fatalf("rescue op kind = %s, want project_update", opKind)
	}
	// The rescue update is the queued one and only one.
	var queuedCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='projrace-initiative' AND state=?`, LinearOutboxQueued).Scan(&queuedCount); err != nil {
		t.Fatal(err)
	}
	if queuedCount != 1 {
		t.Fatalf("queued rescue updates = %d, want 1", queuedCount)
	}
}

// The drain-time Project resolution follows the
// earliest-joined Initiative and stays empty before that Initiative's
// project_create completes, whichever Initiative's Project exists.
func TestResolveLinearProjectIDForWorkFollowsTheOwningInitiative(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "resolvep-product")
	setupLinearLabelConnection(t, s, "resolvep-product", map[string]string{})
	enableLinearPlanning(t, s, "resolvep-product", 2)
	seedLinearWorkOfKind(t, s, "resolvep-first", "resolvep-product-project", "initiative", "First initiative", "First value")
	seedLinearWorkOfKind(t, s, "resolvep-second", "resolvep-product-project", "initiative", "Second initiative", "Second value")
	seedLinearWorkItem(t, s, "resolvep-work", "resolvep-product-project", "Shared title", "Shared value")
	seedLinearInitiativeEntry(t, s, "resolvep-first", "resolvep-work", true)
	seedLinearInitiativeEntry(t, s, "resolvep-second", "resolvep-work", true)

	resolved, err := s.ResolveLinearProjectIDForWork(ctx, "resolvep-work")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() error = %v", err)
	}
	if resolved != "" {
		t.Fatalf("resolution before the owner's Project exists = %q, want empty", resolved)
	}
	seedLinearProjectLink(t, s, "resolvep-second", "remote-project-second")
	resolved, err = s.ResolveLinearProjectIDForWork(ctx, "resolvep-work")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "" {
		t.Fatalf("resolution with only the other Initiative linked = %q, want empty: ownership follows join order", resolved)
	}
	seedLinearProjectLink(t, s, "resolvep-first", "remote-project-first")
	resolved, err = s.ResolveLinearProjectIDForWork(ctx, "resolvep-work")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "remote-project-first" {
		t.Fatalf("resolution = %q, want remote-project-first from the earliest-joined Initiative", resolved)
	}
	// A work item outside every Initiative resolves empty.
	resolved, err = s.ResolveLinearProjectIDForWork(ctx, "resolvep-first")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "" {
		t.Fatalf("resolution for an Initiative work item = %q, want empty: Initiatives hold no issue", resolved)
	}
}
