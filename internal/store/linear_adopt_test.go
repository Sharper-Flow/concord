package store

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

// Linear issue adoption (issue_adopt): an agent that identifies the correct
// existing Linear issue for an unlinked work item records it through the same
// enqueue, drain, and complete path the other op kinds travel. The store never
// calls Linear; the drain resolves the named issue and verifies its team.

func setupAdoptionTarget(t *testing.T, s *Store, productID, workID string) {
	t.Helper()
	setupLinearProduct(t, s, productID)
	seedLinearWorkItem(t, s, workID, productID+"-project", "Adopt title", "Adopt value statement")
	if _, err := s.SetProductPlanningMode(t.Context(), productID, PlanningModeLinear, "pilot adoption", "operator", 2); err != nil {
		t.Fatal(err)
	}
	setupLinearConnectionResourceAtVersion(t, s, productID, map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1",
		"auth_mode": "personal_api_key",
	}}, 3)
}

func TestLinearIssueAdoptionEnqueueRecordsOutboxOperation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	setupAdoptionTarget(t, s, "adopt-product", "adopt-work")
	ctx := t.Context()

	op, err := s.EnqueueLinearIssueAdoption(ctx, "adopt-product", "adopt-work", "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("EnqueueLinearIssueAdoption() error = %v", err)
	}
	if op.OpKind != LinearOpIssueAdopt {
		t.Fatalf("op kind = %s, want issue_adopt", op.OpKind)
	}
	var outboxState, opKind, payload string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state, op_kind, payload FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&outboxState, &opKind, &payload); err != nil {
		t.Fatal(err)
	}
	if outboxState != LinearOutboxQueued || opKind != LinearOpIssueAdopt {
		t.Fatalf("outbox = %s/%s, want queued/issue_adopt", outboxState, opKind)
	}
	var decoded struct {
		ClientUUID      string `json:"client_uuid"`
		ProductID       string `json:"product_id"`
		TeamID          string `json:"team_id"`
		RemoteIssueUUID string `json:"remote_issue_uuid"`
		Title           string `json:"title"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RemoteIssueUUID != "11111111-2222-3333-4444-555555555555" || decoded.ProductID != "adopt-product" || decoded.TeamID != "team-uuid-1" {
		t.Fatalf("payload = %+v", decoded)
	}
	if decoded.Title != "" {
		t.Fatalf("adoption payload carries no authored title, got %q", decoded.Title)
	}
	// No link row is created at enqueue time; the drain completes it.
	var links int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_issue_links WHERE work_id='adopt-work'`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("adoption enqueue created %d link rows, want 0", links)
	}

	// The raw typed enqueue surface accepts the kind too.
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{
		OperationID: "raw-adopt-operation", WorkID: "adopt-work", OpKind: LinearOpIssueAdopt,
		IdempotencyKey: "raw-adopt-idempotency", Payload: []byte(`{"product_id":"adopt-product","remote_issue_uuid":"99999999-8888-7777-6666-555555555555"}`),
	}); err != nil {
		t.Fatalf("EnqueueLinearOperation(issue_adopt) error = %v", err)
	}
}

func TestLinearIssueAdoptionPlanningAuthorityRefusals(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	setupLinearProduct(t, s, "auth-product")
	seedLinearWorkItem(t, s, "auth-work", "auth-product-project", "Auth title", "Auth value")
	ctx := t.Context()

	// local_only refuses.
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "auth-product", "auth-work", "11111111-2222-3333-4444-555555555555"); err == nil || !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("local_only error = %v, want typed refusal", err)
	}
	if _, err := s.SetProductPlanningMode(ctx, "auth-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	// linear_enabled without a declared connection refuses as missing setup.
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "auth-product", "auth-work", "11111111-2222-3333-4444-555555555555"); err == nil || !strings.Contains(err.Error(), "no declared Linear connection") {
		t.Fatalf("missing-setup error = %v, want typed refusal", err)
	}
	// Unknown work and a foreign Product scope refuse.
	setupLinearConnectionResourceAtVersion(t, s, "auth-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1",
		"auth_mode": "personal_api_key",
	}}, 3)
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "auth-product", "ghost-work", "11111111-2222-3333-4444-555555555555"); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unknown work error = %v, want unknown_scope", err)
	}
}

func TestLinearIssueAdoptionRefusesConfirmedAndForeignLinks(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	setupAdoptionTarget(t, s, "conf-product", "conf-work")
	seedLinearWorkItem(t, s, "other-work", "conf-product-project", "Other title", "Other value")
	ctx := t.Context()

	// A confirmed link refuses adoption.
	if err := s.RecordLinearLink(ctx, "conf-work", "aaaaaaaa-0000-0000-0000-000000000001", "EX-1", "https://linear.app/example/issue/EX-1", "", "", LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "conf-work", "aaaaaaaa-0000-0000-0000-000000000001", "EX-1", "https://linear.app/example/issue/EX-1", "", "", LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "conf-product", "conf-work", "11111111-2222-3333-4444-555555555555"); err == nil || !strings.Contains(err.Error(), "already holds a confirmed link") {
		t.Fatalf("confirmed-link error = %v, want typed refusal", err)
	}

	// An issue any other work item already links refuses adoption, whatever
	// that link's state.
	if err := s.RecordLinearLink(ctx, "other-work", "bbbbbbbb-0000-0000-0000-000000000002", "EX-2", "https://linear.app/example/issue/EX-2", "", "", LinearLinkUnpublished); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "probe-work", "conf-product-project", "Probe title", "Probe value")
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "conf-product", "probe-work", "bbbbbbbb-0000-0000-0000-000000000002"); err == nil || !failureKindIs(err, KindInvalidRelation) {
		t.Fatalf("foreign-link error = %v, want invalid_relation", err)
	}

	// An unpublished link reserves the work item while its create is queued, so
	// adoption must refuse instead of creating a duplicate issue.
	if _, err := s.EnqueueLinearIssueAdoption(ctx, "conf-product", "other-work", "11111111-2222-3333-4444-555555555555"); err == nil || !strings.Contains(err.Error(), "pending Linear link") {
		t.Fatalf("adoption over unpublished link error = %v, want pending Linear link refusal", err)
	}
}

func TestLinearIssueAdoptionCompletesConfirmedLinkAndRefusesConflicts(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	setupAdoptionTarget(t, s, "done-product", "done-work")
	seedLinearWorkItem(t, s, "holder-work", "done-product-project", "Holder title", "Holder value")
	ctx := t.Context()

	op, err := s.EnqueueLinearIssueAdoption(ctx, "done-product", "done-work", "cccccccc-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatalf("EnqueueLinearIssueAdoption() error = %v", err)
	}
	if err := s.ClaimLinearOperation(ctx, op.OperationID); err != nil {
		t.Fatal(err)
	}
	identity := LinearRemoteIdentity{
		RemoteUUID:      "cccccccc-0000-0000-0000-000000000003",
		HumanKey:        "EX-3",
		URL:             "https://linear.app/example/issue/EX-3",
		RemoteUpdatedAt: "2026-09-16T00:00:00Z",
	}
	if err := s.CompleteLinearOperation(ctx, op.OperationID, identity); err != nil {
		t.Fatalf("CompleteLinearOperation() error = %v", err)
	}
	link, err := s.ReadLinearLink(ctx, "done-work")
	if err != nil {
		t.Fatal(err)
	}
	if link.LinkState != LinearLinkConfirmed || link.RemoteIssueUUID != identity.RemoteUUID || link.HumanKey != "EX-3" || link.ContentHash != "" {
		t.Fatalf("completed link = %+v", link)
	}

	// The unique index is structural: no second work item can hold the same
	// remote issue, and the typed routes refuse before the index fires.
	if err := s.RecordLinearLink(ctx, "holder-work", identity.RemoteUUID, "EX-3", identity.URL, "", "", LinearLinkPending); err == nil || !failureKindIs(err, KindInvalidRelation) {
		t.Fatalf("cross-work record error = %v, want invalid_relation", err)
	}

	// A second adoption for the now-confirmed work refuses at enqueue, and a
	// claimed adoption cannot complete over a link the same work confirmed
	// through another route.
	op2, err := s.EnqueueLinearIssueAdoption(ctx, "done-product", "holder-work", "dddddddd-0000-0000-0000-000000000004")
	if err != nil {
		t.Fatalf("adoption for unlinked holder work error = %v", err)
	}
	if err := s.ClaimLinearOperation(ctx, op2.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "holder-work", "dddddddd-0000-0000-0000-000000000004", "EX-4", "https://linear.app/example/issue/EX-4", "", "", LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "holder-work", "dddddddd-0000-0000-0000-000000000004", "EX-4", "https://linear.app/example/issue/EX-4", "", "", LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}
	err = s.CompleteLinearOperation(ctx, op2.OperationID, LinearRemoteIdentity{RemoteUUID: "dddddddd-0000-0000-0000-000000000004", HumanKey: "EX-4"})
	if err == nil || !failureKindIs(err, KindInvalidTransition) || !strings.Contains(err.Error(), "an adopt operation cannot complete an already confirmed link") {
		t.Fatalf("adopt-over-confirmed error = %v, want invalid_transition", err)
	}
	if !completionFailureIsPermanent(t, err) {
		t.Fatalf("adopt-over-confirmed error = %v, want RetrySafe false", err)
	}

	// A create still cannot complete a confirmed link; its wording stays.
	seedLinearWorkItem(t, s, "create-work", "done-product-project", "Create title", "Create value")
	createOp, err := s.EnqueueLinearIssueForWork(ctx, "create-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimLinearOperation(ctx, createOp.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "create-work", "eeeeeeee-0000-0000-0000-000000000005", "EX-5", "https://linear.app/example/issue/EX-5", "", "", LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "create-work", "eeeeeeee-0000-0000-0000-000000000005", "EX-5", "https://linear.app/example/issue/EX-5", "", "", LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}
	err = s.CompleteLinearOperation(ctx, createOp.OperationID, LinearRemoteIdentity{RemoteUUID: "eeeeeeee-0000-0000-0000-000000000005", HumanKey: "EX-5"})
	if err == nil || !failureKindIs(err, KindInvalidTransition) || !strings.Contains(err.Error(), "a create operation cannot complete an already confirmed link") {
		t.Fatalf("create-over-confirmed error = %v, want invalid_transition", err)
	}
	if !completionFailureIsPermanent(t, err) {
		t.Fatalf("create-over-confirmed error = %v, want RetrySafe false", err)
	}
}

// completionFailureIsPermanent asserts the failure the drain consumes after a
// successful provider call declares a repeat unsafe, so the drain must not
// requeue the operation onto another provider call.
func completionFailureIsPermanent(t *testing.T, err error) bool {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	return !failure.RetrySafe
}

// TestLinearIssueAdoptionCompletionConvergesManagedState proves the CD-0171
// review correction: adoption completes through the same full-state
// convergence as a create. The adopted issue carries none of the managed
// state Concord enqueues, so the completion queues exactly one issue_update
// that installs the repository label, the optional label for a non-required
// entry, and the owning Initiative's Linear Project, inside the completion
// transaction.
func TestLinearIssueAdoptionCompletionConvergesManagedState(t *testing.T) {
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

	completeAdoption := func(t *testing.T, s *Store, productID, workID string) {
		t.Helper()
		ctx := t.Context()
		op, err := s.EnqueueLinearIssueAdoption(ctx, productID, workID, "11111111-2222-3333-4444-555555555555")
		if err != nil {
			t.Fatalf("EnqueueLinearIssueAdoption() error = %v", err)
		}
		if err := s.ClaimLinearOperation(ctx, op.OperationID); err != nil {
			t.Fatal(err)
		}
		// The adopted issue was authored in Linear, so its own title and
		// description are the state the drain resolved.
		if err := s.CompleteLinearIssueAdoption(ctx, op.OperationID, LinearRemoteIdentity{
			RemoteUUID:      "11111111-2222-3333-4444-555555555555",
			HumanKey:        "EX-9",
			URL:             "https://linear.app/example/issue/EX-9",
			RemoteUpdatedAt: "2026-09-24T00:00:00Z",
		}, "Human-authored title", "Human-authored description"); err != nil {
			t.Fatalf("CompleteLinearIssueAdoption() error = %v", err)
		}
		link, err := s.ReadLinearLink(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if link.LinkState != LinearLinkConfirmed || link.ContentHash != "" {
			t.Fatalf("adoption link = %+v, want confirmed with no claimed content digest", link)
		}
	}

	t.Run("an unlabeled adopted issue receives its repository label and Project", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		setupLinearProduct(t, s, "adoptconv-product")
		setupLinearLabelConnection(t, s, "adoptconv-product", map[string]string{
			"task": "label-task", "project:adoptconv-product-project": "label-repo",
		})
		enableLinearPlanning(t, s, "adoptconv-product", 2)
		seedLinearWorkOfKind(t, s, "adoptconv-initiative", "adoptconv-product-project", "initiative", "Adopt convergence initiative", "Adopt convergence value")
		seedLinearWorkItem(t, s, "adoptconv-work", "adoptconv-product-project", "Adopt conv title", "Adopt conv value")
		seedLinearInitiativeEntry(t, s, "adoptconv-initiative", "adoptconv-work", true)
		seedLinearProjectLink(t, s, "adoptconv-initiative", "adopt-project-remote")

		completeAdoption(t, s, "adoptconv-product", "adoptconv-work")
		updates := queuedConvergingUpdates(t, s, "adoptconv-work")
		if len(updates) != 1 {
			t.Fatalf("converging updates = %d, want exactly one", len(updates))
		}
		sort.Strings(updates[0].LabelIDs)
		if len(updates[0].LabelIDs) != 2 || updates[0].LabelIDs[0] != "label-repo" || updates[0].LabelIDs[1] != "label-task" {
			t.Fatalf("converging labels = %v, want the repository and kind labels (CD-0171 D3)", updates[0].LabelIDs)
		}
		// The payload carries no Project: the update drain resolves the owning
		// Initiative's Project at send time, so the link read is the assertion.
		projectID, err := s.ResolveLinearProjectIDForWork(t.Context(), "adoptconv-work")
		if err != nil {
			t.Fatal(err)
		}
		if projectID != "adopt-project-remote" {
			t.Fatalf("drain-time project = %q, want the owning Initiative's Linear Project (CD-0171 D2)", projectID)
		}
	})

	t.Run("a non-required adopted entry also receives the optional label", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		setupLinearProduct(t, s, "optconv-product")
		setupLinearLabelConnection(t, s, "optconv-product", map[string]string{
			"task": "label-task", "optional": "label-optional", "project:optconv-product-project": "label-repo",
		})
		enableLinearPlanning(t, s, "optconv-product", 2)
		seedLinearWorkOfKind(t, s, "optconv-initiative", "optconv-product-project", "initiative", "Optional convergence initiative", "Optional convergence value")
		seedLinearWorkItem(t, s, "optconv-work", "optconv-product-project", "Optional conv title", "Optional conv value")
		seedLinearInitiativeEntry(t, s, "optconv-initiative", "optconv-work", false)
		seedLinearProjectLink(t, s, "optconv-initiative", "opt-project-remote")

		completeAdoption(t, s, "optconv-product", "optconv-work")
		updates := queuedConvergingUpdates(t, s, "optconv-work")
		if len(updates) != 1 {
			t.Fatalf("converging updates = %d, want exactly one", len(updates))
		}
		hasOptional, hasRepository := false, false
		for _, labelID := range updates[0].LabelIDs {
			switch labelID {
			case "label-optional":
				hasOptional = true
			case "label-repo":
				hasRepository = true
			}
		}
		if !hasOptional || !hasRepository {
			t.Fatalf("converging labels = %v, want the repository and optional labels (CD-0171 D3, D5)", updates[0].LabelIDs)
		}
		projectID, err := s.ResolveLinearProjectIDForWork(t.Context(), "optconv-work")
		if err != nil {
			t.Fatal(err)
		}
		if projectID != "opt-project-remote" {
			t.Fatalf("drain-time project = %q, want the owning Initiative's Linear Project", projectID)
		}
	})

	t.Run("a work item outside an Initiative converges with no Linear Project", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		setupLinearProduct(t, s, "bareconv-product")
		setupLinearLabelConnection(t, s, "bareconv-product", map[string]string{
			"task": "label-task", "project:bareconv-product-project": "label-repo",
		})
		enableLinearPlanning(t, s, "bareconv-product", 2)
		seedLinearWorkItem(t, s, "bareconv-work", "bareconv-product-project", "Bare conv title", "Bare conv value")

		completeAdoption(t, s, "bareconv-product", "bareconv-work")
		updates := queuedConvergingUpdates(t, s, "bareconv-work")
		if len(updates) != 1 {
			t.Fatalf("converging updates = %d, want exactly one", len(updates))
		}
		// The payload carries no Project, and no Initiative owns this work
		// item, so the update drain must resolve no Linear Project.
		projectID, err := s.ResolveLinearProjectIDForWork(t.Context(), "bareconv-work")
		if err != nil {
			t.Fatal(err)
		}
		if projectID != "" {
			t.Fatalf("drain-time project = %q, want no Linear Project outside an Initiative (CD-0171 D2)", projectID)
		}
		hasRepository := false
		for _, labelID := range updates[0].LabelIDs {
			if labelID == "label-repo" {
				hasRepository = true
			}
		}
		if !hasRepository {
			t.Fatalf("converging labels = %v, want the repository label (CD-0171 D3)", updates[0].LabelIDs)
		}
	})
}
