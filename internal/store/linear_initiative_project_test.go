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
// given join order, with fold guards open.
func seedLinearInitiativeEntry(t *testing.T, s *Store, initiative, child string, required bool) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES(?, ?, (SELECT coalesce(max(position)+1, 0) FROM initiative_entries WHERE initiative_work_id=?), ?); DELETE FROM fold_guard`, initiative, child, initiative, boolInt(required)); err != nil {
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
	payload := decodeLinearPayload(t, s, op.OperationID)
	if payload.ProjectID != "" {
		t.Fatalf("payload project id = %q, want empty: no mapping names a repository Project", payload.ProjectID)
	}
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
	if payload.ProjectID != "" {
		t.Fatalf("payload project id = %q, want empty before the owning Initiative's Project is created", payload.ProjectID)
	}
	if !strings.Contains(payload.Description, "## Initiatives") || !strings.Contains(payload.Description, "Owned by `owner-first`") || !strings.Contains(payload.Description, "Also in Second initiative (`owner-second`).") {
		t.Fatalf("payload description = %q, want the owner and a title reference to the other Initiative", payload.Description)
	}

	// Once the earliest-joined Initiative's Project exists, its remote uuid
	// sets the Project field.
	seedLinearProjectLink(t, s, "owner-first", "remote-project-first")
	update, err := s.EnqueueLinearIssueForWork(ctx, "owner-work", LinearOpIssueUpdate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork(update) error = %v", err)
	}
	updated := decodeLinearPayload(t, s, update.OperationID)
	if updated.ProjectID != "remote-project-first" {
		t.Fatalf("update project id = %q, want remote-project-first from the earliest-joined Initiative", updated.ProjectID)
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
	if payload.ProjectID != "" || strings.Contains(payload.Description, "## Initiatives") {
		t.Fatalf("payload = %+v / %q, want no Project and no Initiatives section (CD-0171 D4)", payload.ProjectID, payload.Description)
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

// CD-0171 review correction: one Linear Project per Initiative, so a repeated
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

	refreshed, err := s.EnqueueLinearIssueUpdatesForInitiativeEntries(ctx, "refresh-initiative")
	if err != nil {
		t.Fatalf("EnqueueLinearIssueUpdatesForInitiativeEntries() error = %v", err)
	}
	if len(refreshed) != 1 || refreshed[0].WorkID != "refresh-entry" || refreshed[0].OpKind != LinearOpIssueUpdate {
		t.Fatalf("refresh = %+v, want one update for the confirmed entry", refreshed)
	}
	payload := decodeLinearPayload(t, s, refreshed[0].OperationID)
	if payload.ProjectID != "" {
		t.Fatalf("entry update project id = %q, want empty before the Initiative's project_create completes", payload.ProjectID)
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

// CD-0171 review correction: the completion compares the Initiative state the
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

// CD-0171 review correction: the drain-time Project resolution follows the
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
