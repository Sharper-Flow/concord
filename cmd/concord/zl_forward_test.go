package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

func mustJSONRaw(payload any) json.RawMessage {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return raw
}

// seedForwardingCLIWork seeds a work item with a primary membership and an
// optional secondary membership, so forwarding must decide the Product from
// the landing Project instead of the alphabetical-first membership.
func seedForwardingCLIWork(t *testing.T, dbPath, workID, primaryProject, secondaryProject string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, '{"title":?,"value_statement":"CLI forwarding value statement","kind":"task","priority":0,"urgency":"standard"}', '2026-09-30T00:00:00Z', '2026-09-30T00:00:00Z')`, workID, workID, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, primaryProject); err != nil {
		t.Fatal(err)
	}
	if secondaryProject != "" {
		if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'secondary')`, workID, secondaryProject); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedForwardingCLISecondProduct adds a second Product over an existing
// Project through the event model, so the Project spans two Products without
// disambiguation.
func seedForwardingCLISecondProduct(t *testing.T, dbPath, productID, projectID string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := store.ApplyOperation(context.Background(), s, store.Operation{
		Events: []store.Event{
			{EventID: productID + "-created", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: mustJSONRaw(map[string]any{
				"display_name": productID, "stage_maturity": "prototype", "stage_audience_commitment": "operator_only",
			})},
			{EventID: productID + "-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: mustJSONRaw(map[string]any{
				"product_id": productID, "project_id": projectID, "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
			})},
		},
		ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, productID): 0},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInheritedForwardedProductPrefersLauncherSelection(t *testing.T) {
	t.Setenv(selectedProductEnv, "selected-product")
	if got := inheritedForwardedProduct(); got != "selected-product" {
		t.Fatalf("inheritedForwardedProduct() = %q, want %q", got, "selected-product")
	}
	t.Setenv(selectedProductEnv, "")
	t.Setenv("CONCORD_PRODUCT_ID", "ambient-product")
	if got := inheritedForwardedProduct(); got != "ambient-product" {
		t.Fatalf("inheritedForwardedProduct() = %q, want %q", got, "ambient-product")
	}
	t.Setenv("CONCORD_PRODUCT_ID", "")
	if got := inheritedForwardedProduct(); got != "" {
		t.Fatalf("inheritedForwardedProduct() = %q, want empty", got)
	}
}

func TestResolveForwardedProductFollowsLandingProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "fwd-alpha-product", "fwd-alpha-project")
	seedCLIProduct(t, dbPath, "fwd-zeta-product", "fwd-zeta-project")
	seedCLIProduct(t, dbPath, "fwd-gamma-product", "fwd-gamma-project")
	seedForwardingCLISecondProduct(t, dbPath, "fwd-beta-product", "fwd-alpha-project")
	seedForwardingCLIWork(t, dbPath, "fwd-work", "fwd-zeta-project", "fwd-alpha-project")
	seedForwardingCLIWork(t, dbPath, "fwd-work-two", "fwd-alpha-project", "")
	t.Setenv(dbOverrideEnv, dbPath)
	for _, tc := range []struct {
		name      string
		work      string
		project   string
		preferred string
		want      string
		wantErr   string
	}{
		{name: "named Project owns the Product over the inherited selection", work: "fwd-work", project: "fwd-zeta-project", preferred: "fwd-alpha-product", want: "fwd-zeta-product"},
		{name: "inherited foreign selection loses to the primary Project", work: "fwd-work", preferred: "fwd-alpha-product", want: "fwd-zeta-product"},
		{name: "primary Project owns the default landing", work: "fwd-work", want: "fwd-zeta-product"},
		{name: "inherited selection matching the Project wins", work: "fwd-work", preferred: "fwd-zeta-product", want: "fwd-zeta-product"},
		{name: "ambiguous Project refuses without disambiguation", work: "fwd-work-two", project: "fwd-alpha-project", wantErr: "ambiguous_scope"},
		{name: "inherited selection disambiguates the Project", work: "fwd-work-two", project: "fwd-alpha-project", preferred: "fwd-beta-product", want: "fwd-beta-product"},
		{name: "non-member Project refuses", work: "fwd-work", project: "fwd-gamma-project", wantErr: "does not hold Project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveForwardedProduct(tc.work, tc.project, tc.preferred)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveForwardedProduct(%q, %q, %q) = %q, want a typed refusal", tc.work, tc.project, tc.preferred, got)
				}
				var failure *store.Failure
				if !errors.As(err, &failure) {
					t.Fatalf("resolveForwardedProduct(%q, %q, %q) error = %v, want a *store.Failure", tc.work, tc.project, tc.preferred, err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("resolveForwardedProduct(%q, %q, %q) error = %v, want %q", tc.work, tc.project, tc.preferred, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveForwardedProduct(%q, %q, %q): %v", tc.work, tc.project, tc.preferred, err)
			}
			if got != tc.want {
				t.Fatalf("resolveForwardedProduct(%q, %q, %q) = %q, want %q", tc.work, tc.project, tc.preferred, got, tc.want)
			}
		})
	}
}

type forwardedHandoff struct {
	product, work, prompt, project string
}

// captureForwardSession replaces the session start so a test observes the
// handoff runZLForwarding derives without executing a host.
func captureForwardSession(t *testing.T) *[]forwardedHandoff {
	t.Helper()
	calls := &[]forwardedHandoff{}
	previous := forwardSession
	forwardSession = func(product, work, prompt, project string, _ io.Reader, _, _ io.Writer) int {
		*calls = append(*calls, forwardedHandoff{product: product, work: work, prompt: prompt, project: project})
		return 0
	}
	t.Cleanup(func() { forwardSession = previous })
	return calls
}

func TestRunZLForwardingLandsSecondaryProjectInItsOwnProduct(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "fwd-alpha-product", "fwd-alpha-project")
	seedCLIProduct(t, dbPath, "fwd-zeta-product", "fwd-zeta-project")
	seedForwardingCLIWork(t, dbPath, "fwd-work", "fwd-alpha-project", "fwd-zeta-project")
	t.Setenv(dbOverrideEnv, dbPath)
	// The opener's pane inherits the first coordinator's Product selection.
	t.Setenv(selectedProductEnv, "fwd-alpha-product")
	t.Setenv("CONCORD_PRODUCT_ID", "")
	calls := captureForwardSession(t)
	var out, errOut bytes.Buffer
	if code := runZLForwarding([]string{"fwd-work", "--project", "fwd-zeta-project"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("runZLForwarding exit = %d, stderr = %q", code, errOut.String())
	}
	want := forwardedHandoff{product: "fwd-zeta-product", work: "fwd-work", project: "fwd-zeta-project"}
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("forwarded handoffs = %+v, want [%+v]", *calls, want)
	}
}

func TestRunZLForwardingRefusesProjectForUnlinkedLinearIssue(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "fwd-alpha-product", "fwd-alpha-project")
	t.Setenv(dbOverrideEnv, dbPath)
	t.Setenv(selectedProductEnv, "fwd-alpha-product")
	// A regression that skips the guard must fail here without a remote call.
	t.Setenv(linearclient.EnvAPIKey, "")
	t.Setenv(linearclient.EnvEndpoint, "https://linear.invalid/graphql")
	calls := captureForwardSession(t)
	var out, errOut bytes.Buffer
	if code := runZLForwarding([]string{"FWD-404", "--project", "fwd-alpha-project"}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("runZLForwarding exit = %d, want 1; stderr = %q", code, errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("forwarded handoffs = %+v, want none", *calls)
	}
	if !strings.Contains(errOut.String(), "--project needs a Linear issue already linked") {
		t.Fatalf("stderr = %q, want the linked-issue refusal", errOut.String())
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var works int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 0 {
		t.Fatalf("work_items = %d, want 0: the refusal must precede adoption effects", works)
	}
}

func TestResolveZLLinearReferenceFollowsLandingProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "fwd-alpha-product", "fwd-alpha-project")
	seedCLIProduct(t, dbPath, "fwd-zeta-product", "fwd-zeta-project")
	seedForwardingCLIWork(t, dbPath, "fwd-work", "fwd-zeta-project", "fwd-alpha-project")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "fwd-work", "fwd-issue-1", "FWD-1", "https://linear.app/example/issue/FWD-1", "", "", store.LinearLinkPending); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "fwd-work", "fwd-issue-1", "FWD-1", "https://linear.app/example/issue/FWD-1", "", "", store.LinearLinkConfirmed); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, dbPath)
	work, product, err := resolveZLLinearReference("FWD-1", "", "fwd-alpha-project", "fwd-zeta-product")
	if err != nil {
		t.Fatal(err)
	}
	if work != "fwd-work" || product != "fwd-alpha-product" {
		t.Fatalf("resolved work/product = %q/%q, want fwd-work/fwd-alpha-product", work, product)
	}
}
