package store

import (
	"context"
	"errors"
	"testing"
)

// seedForwardingFixture seeds the forwarding shapes the landing-Project rule
// must decide. Product IDs order so the alphabetical-first membership read
// would pick the secondary Product: the primary Project owns fz-zeta, the
// secondary Project owns fz-alpha. fz-proj-two spans two Products and
// fz-proj-none carries none.
func seedForwardingFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	events := []Event{
		productCreatedEvent("fz-alpha", "fz-alpha-created"),
		productCreatedEvent("fz-zeta", "fz-zeta-created"),
		productCreatedEvent("fz-beta", "fz-beta-created"),
		productCreatedEvent("fz-gamma", "fz-gamma-created"),
		projectCreatedEvent("fz-proj-alpha", "fz-proj-alpha-created"),
		projectCreatedEvent("fz-proj-zeta", "fz-proj-zeta-created"),
		projectCreatedEvent("fz-proj-two", "fz-proj-two-created"),
		operationEvent("fz-alpha-membership", "product_project.added", SubjectProduct, "fz-alpha", map[string]any{
			"product_id": "fz-alpha", "project_id": "fz-proj-alpha", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("fz-zeta-membership", "product_project.added", SubjectProduct, "fz-zeta", map[string]any{
			"product_id": "fz-zeta", "project_id": "fz-proj-zeta", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("fz-beta-membership", "product_project.added", SubjectProduct, "fz-beta", map[string]any{
			"product_id": "fz-beta", "project_id": "fz-proj-two", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("fz-gamma-membership", "product_project.added", SubjectProduct, "fz-gamma", map[string]any{
			"product_id": "fz-gamma", "project_id": "fz-proj-two", "role": "secondary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
		workCreatedEvent("fz-work-1", "fz-work-1-created"),
		operationEvent("fz-work-1-zeta", "work_project.added", SubjectWorkItem, "fz-work-1", map[string]any{
			"work_id": "fz-work-1", "project_id": "fz-proj-zeta", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("fz-work-1-alpha", "work_project.added", SubjectWorkItem, "fz-work-1", map[string]any{
			"work_id": "fz-work-1", "project_id": "fz-proj-alpha", "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3,
		}),
		workCreatedEvent("fz-work-2", "fz-work-2-created"),
		operationEvent("fz-work-2-zeta", "work_project.added", SubjectWorkItem, "fz-work-2", map[string]any{
			"work_id": "fz-work-2", "project_id": "fz-proj-zeta", "role": "primary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
		operationEvent("fz-work-2-two", "work_project.added", SubjectWorkItem, "fz-work-2", map[string]any{
			"work_id": "fz-work-2", "project_id": "fz-proj-two", "role": "secondary", "reason": "fixture", "expected_version": 2, "resulting_version": 3,
		}),
		workCreatedEvent("fz-work-3", "fz-work-3-created"),
		operationEvent("fz-work-3-alpha", "work_project.added", SubjectWorkItem, "fz-work-3", map[string]any{
			"work_id": "fz-work-3", "project_id": "fz-proj-alpha", "role": "secondary", "reason": "fixture", "expected_version": 1, "resulting_version": 2,
		}),
	}
	expected := map[SubjectRef]int64{}
	for _, ref := range []struct {
		subject SubjectType
		id      string
	}{
		{SubjectProduct, "fz-alpha"}, {SubjectProduct, "fz-zeta"}, {SubjectProduct, "fz-beta"}, {SubjectProduct, "fz-gamma"},
		{SubjectProject, "fz-proj-alpha"}, {SubjectProject, "fz-proj-zeta"}, {SubjectProject, "fz-proj-two"},
		{SubjectWorkItem, "fz-work-1"}, {SubjectWorkItem, "fz-work-2"}, {SubjectWorkItem, "fz-work-3"},
	} {
		expected[VersionRef(ref.subject, ref.id)] = 0
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
	// fz-proj-none exists only out of band: the membership fold refuses a
	// Project with no Product, which is exactly the state the resolver's
	// no-Product refusal must still answer. Seed it beside the fold with the
	// fold guard, as other out-of-band fixtures do.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('fz-proj-none','fz-proj-none',1,'2026-09-30T00:00:00Z','2026-09-30T00:00:00Z');
		INSERT INTO work_items(id,kind,title,lifecycle,priority,urgency,version,intent_json,created_at,updated_at) VALUES('fz-work-4','task','fz-work-4','needed',0,'standard',1,'{"title":"fz-work-4","kind":"task"}','2026-09-30T00:00:00Z','2026-09-30T00:00:00Z');
		INSERT INTO work_projects(work_id,project_id,role) VALUES('fz-work-4','fz-proj-none','secondary');
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ work, issue, key string }{
		{"fz-work-1", "fz-issue-1", "FZ-1"},
		{"fz-work-2", "fz-issue-2", "FZ-2"},
	} {
		// A new link starts pending and moves to confirmed, as the CLI
		// fixture does.
		for _, state := range []string{LinearLinkPending, LinearLinkConfirmed} {
			if err := s.RecordLinearLink(ctx, link.work, link.issue, link.key, "https://linear.app/example/issue/"+link.key, "", "", state); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s
}

func TestResolveLauncherWorkProductFollowsLandingProject(t *testing.T) {
	t.Parallel()
	s := seedForwardingFixture(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		work      string
		project   string
		preferred string
		want      string
		wantKind  FailureKind
		candidate string
	}{
		{name: "primary membership owns the default landing", work: "fz-work-1", want: "fz-zeta"},
		{name: "explicit member Project owns the landing", work: "fz-work-1", project: "fz-proj-alpha", want: "fz-alpha"},
		{name: "foreign inherited selection loses to the primary Project", work: "fz-work-1", preferred: "fz-alpha", want: "fz-zeta"},
		{name: "foreign inherited selection loses to the named Project", work: "fz-work-1", project: "fz-proj-zeta", preferred: "fz-alpha", want: "fz-zeta"},
		{name: "non-member Project refuses", work: "fz-work-1", project: "fz-proj-two", wantKind: KindUnknownScope},
		{name: "ambiguous Project refuses without disambiguation", work: "fz-work-2", project: "fz-proj-two", wantKind: KindAmbiguousScope, candidate: "fz-beta"},
		{name: "inherited selection disambiguates", work: "fz-work-2", project: "fz-proj-two", preferred: "fz-gamma", want: "fz-gamma"},
		{name: "work without a primary Project refuses", work: "fz-work-3", wantKind: KindUnknownScope},
		{name: "Project without a Product refuses", work: "fz-work-4", project: "fz-proj-none", wantKind: KindUnknownScope},
		{name: "unknown work refuses", work: "fz-missing", wantKind: KindUnknownScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.ResolveLauncherWorkProduct(ctx, tc.work, tc.project, tc.preferred)
			if tc.wantKind != "" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != tc.wantKind {
					t.Fatalf("ResolveLauncherWorkProduct(%q, %q, %q) error = %v, want kind %s", tc.work, tc.project, tc.preferred, err, tc.wantKind)
				}
				if tc.candidate != "" {
					found := false
					for _, candidate := range failure.CandidateIDs {
						if candidate == tc.candidate {
							found = true
						}
					}
					if !found {
						t.Fatalf("ambiguous refusal candidates = %v, want to include %q", failure.CandidateIDs, tc.candidate)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveLauncherWorkProduct(%q, %q, %q): %v", tc.work, tc.project, tc.preferred, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveLauncherWorkProduct(%q, %q, %q) = %q, want %q", tc.work, tc.project, tc.preferred, got, tc.want)
			}
		})
	}
}

func TestResolveLauncherLinearIssueFollowsLandingProject(t *testing.T) {
	t.Parallel()
	s := seedForwardingFixture(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		key       string
		project   string
		preferred string
		wantWork  string
		want      string
		wantKind  FailureKind
	}{
		{name: "confirmed link follows the primary Project", key: "FZ-1", wantWork: "fz-work-1", want: "fz-zeta"},
		{name: "confirmed link follows the named Project", key: "FZ-1", project: "fz-proj-alpha", wantWork: "fz-work-1", want: "fz-alpha"},
		{name: "ambiguous landing refuses", key: "FZ-2", project: "fz-proj-two", wantKind: KindAmbiguousScope},
		{name: "inherited selection disambiguates the landing", key: "FZ-2", project: "fz-proj-two", preferred: "fz-beta", wantWork: "fz-work-2", want: "fz-beta"},
		{name: "unlinked issue refuses", key: "FZ-9", wantKind: KindUnknownScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			linked, err := s.ResolveLauncherLinearIssue(ctx, tc.key, "", tc.project, tc.preferred)
			if tc.wantKind != "" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != tc.wantKind {
					t.Fatalf("ResolveLauncherLinearIssue(%q, %q, %q) error = %v, want kind %s", tc.key, tc.project, tc.preferred, err, tc.wantKind)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveLauncherLinearIssue(%q, %q, %q): %v", tc.key, tc.project, tc.preferred, err)
			}
			if linked.WorkID != tc.wantWork || linked.ProductID != tc.want {
				t.Fatalf("ResolveLauncherLinearIssue(%q, %q, %q) = %q/%q, want %q/%q", tc.key, tc.project, tc.preferred, linked.WorkID, linked.ProductID, tc.wantWork, tc.want)
			}
		})
	}
}
