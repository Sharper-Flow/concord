package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestMandatedLawDiagnostics(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"single", "federated", "qualified"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			repo := initKnowledgeRepo(t)
			writeManifestFixture(t, repo,
				manifestFixture{ID: "accepted", Kind: "spec", Path: ".concord/docs/current.md", Status: "accepted", Date: "2026-10-01T00:00:00Z", Title: "Current", Summary: "Current law", Scopes: homeScope()},
				manifestFixture{ID: "lesson", Kind: "lesson", Path: ".concord/docs/lesson.md", Status: "published", Date: "2026-10-01T00:00:00Z", Title: "Lesson", Summary: "Useful context, not law", Scopes: homeScope()},
			)
			commit := commitKnowledgeRepo(t, repo, "diagnostic records")
			home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
			authorizeKnowledgeProductHome(t, s, "diag-single", home, "p", "other")
			if err := s.EnsureKnowledgeIndexFresh(context.Background(), home); err != nil {
				t.Fatal(err)
			}
			if route != "single" {
				sourceRepo := initKnowledgeRepo(t)
				seedFederatedSource(t, sourceRepo, "SRC-LAW", ".concord/docs/decisions/CD-0902-source.md", "Source law")
				commitKnowledgeRepo(t, sourceRepo, "second source")
				source := KnowledgeHome{HomeProjectID: "other", HomeLocatorID: "other-loc", RepoPath: sourceRepo, HeadRef: "HEAD"}
				authorizeSourceLocator(t, s, source)
				if _, err := s.RegisterProductKnowledgeSource(context.Background(), ProductKnowledgeSourceRegistration{
					ProductID: "diag-single", ProjectID: "other", LocatorID: "other-loc", Reason: "diagnostic source", ExpectedVersion: 1,
				}); err != nil {
					t.Fatal(err)
				}
			}
			ref := func(id string) string {
				if route == "qualified" {
					return "p/" + id
				}
				return id
			}
			check := func(ids []string) error {
				return checkMandatedLawsAtHome(context.Background(), s, "p", "l", ids, nil, false)
			}
			for _, tc := range []struct {
				name       string
				ids        []string
				wantDetail []string
			}{
				{"absent", []string{ref("missing")}, []string{"no law record", ref("missing")}},
				{"nonaccepted", []string{ref("lesson")}, []string{"not accepted law", ref("lesson"), "lesson, published"}},
				{"mixed", []string{ref("accepted"), ref("missing"), ref("lesson")}, []string{"no law record", ref("missing"), "not accepted law", ref("lesson"), "lesson, published"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := check(tc.ids)
					var failure *Failure
					if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || failure.RetrySafe {
						t.Fatalf("unexpected refusal: %v", err)
					}
					for _, want := range tc.wantDetail {
						if !strings.Contains(failure.Detail, want) {
							t.Errorf("detail %q does not contain %q", failure.Detail, want)
						}
					}
					if !strings.Contains(failure.Detail, "p/l@"+commit) {
						t.Errorf("refusal lost the checked source revision: %q", failure.Detail)
					}
					if strings.Contains(failure.RecoveryAction, "rebuild") || strings.Contains(failure.RecoveryAction, "publish") {
						t.Errorf("a missing or nonaccepted record is not evidence of projection lag: %q", failure.RecoveryAction)
					}
					wantCandidates := []string{}
					for _, id := range tc.ids {
						if id != ref("accepted") {
							wantCandidates = append(wantCandidates, id)
						}
					}
					if !reflect.DeepEqual(failure.CandidateIDs, wantCandidates) {
						t.Errorf("candidates = %v, want %v", failure.CandidateIDs, wantCandidates)
					}
				})
			}
			if err := check([]string{ref("accepted")}); err != nil {
				t.Fatalf("accepted law refused: %v", err)
			}
			if route == "qualified" {
				var failure *Failure
				if err := check([]string{"other/lesson"}); !failureAs(err, &failure) || !strings.Contains(failure.Detail, "no law record") || strings.Contains(failure.Detail, "published") {
					t.Fatalf("qualified absence must not diagnose another source's record: %v", err)
				}
			}
		})
	}
}

func TestSingleHomeMandateRefreshesProjection(t *testing.T) {
	t.Parallel()
	for _, designated := range []bool{false, true} {
		name := "standalone"
		if designated {
			name = "product"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := openTemp(t)
			repo := initKnowledgeRepo(t)
			base := manifestFixture{ID: "base", Kind: "spec", Path: ".concord/docs/base.md", Status: "accepted", Date: "2026-10-01T00:00:00Z", Title: "Base", Summary: "Base law", Scopes: homeScope()}
			writeManifestFixture(t, repo, base)
			commitKnowledgeRepo(t, repo, "base law")
			home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
			if designated {
				authorizeKnowledgeProductHome(t, s, "single-product", home)
			} else {
				authorizeSourceLocator(t, s, home)
			}
			if designated {
				if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
					t.Fatal(err)
				}
			}
			writeManifestFixture(t, repo, base, manifestFixture{ID: "later", Kind: "spec", Path: ".concord/docs/later.md", Status: "accepted", Date: "2026-10-01T00:00:00Z", Title: "Later", Summary: "Later law", Scopes: homeScope()})
			commit := commitKnowledgeRepo(t, repo, "add later law")
			if !designated {
				if err := checkMandatedLawsAtHome(ctx, s, "p", "l", nil, nil, false); err != nil {
					t.Fatalf("a refresh limitation must not block a law-free transition: %v", err)
				}
				var failure *Failure
				if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"later"}, nil, false); !failureAs(err, &failure) || failure.Kind != KindKnowledgeAmbiguous || !strings.Contains(failure.RecoveryAction, "assign exactly one Product knowledge home") {
					t.Fatalf("an unconfigured standalone home must expose its refresh limitation, not infer an absent law: %v", err)
				}
				return
			}
			if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"later"}, nil, false); err != nil {
				t.Fatalf("a stale single-home projection must refresh before diagnosing a mandate: %v", err)
			}
			watermark, err := knowledgeIndexWatermark(ctx, s.db, "p", "l", "HEAD")
			if err != nil || watermark != commit {
				t.Fatalf("watermark = %q, want %q: %v", watermark, commit, err)
			}
			writeKnowledgeFile(t, repo, "README.md", "unrelated content\n")
			commitKnowledgeRepo(t, repo, "unrelated content")
			if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"later"}, nil, false); err != nil {
				t.Fatal(err)
			}
			after, err := knowledgeIndexWatermark(ctx, s.db, "p", "l", "HEAD")
			if err != nil || after != watermark {
				t.Fatalf("a code-only commit must not trigger an index rebuild: %q -> %q: %v", watermark, after, err)
			}
		})
	}
}

func TestMandatedLawDiagnosticsAfterFreshSourceProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "diag-home", HomeLocatorID: "diag-home-loc", HeadRef: "HEAD"}
	source := KnowledgeHome{HomeProjectID: "diag-src", HomeLocatorID: "diag-src-loc", HeadRef: "HEAD"}
	s, source := seedFederatedProduct(t, "diag-product", home, source,
		"SRC-LAW", ".concord/docs/decisions/CD-0902-source.md", "Source law")
	proof, err := s.establishKnowledgeSourceSetProofAtHome(ctx, home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		t.Fatal(err)
	}
	check := func() error {
		return s.Transact(proof, func(tx *Transaction) error {
			raw, err := transactionSQL(tx, "check_mandated_laws")
			if err != nil {
				return err
			}
			return checkMandatedLawsTxAtHome(proof, raw, home.HomeProjectID, home.HomeLocatorID, []string{"missing"}, nil, false)
		})
	}
	var failure *Failure
	if err := check(); !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || !strings.Contains(failure.Detail, "no law record") || strings.Contains(failure.RecoveryAction, "rebuild") {
		t.Fatalf("fresh source proof should diagnose an absent law, not lag: %v", err)
	}
	writeKnowledgeFile(t, source.RepoPath, ".concord/docs/work/later.md", "changed projected content\n")
	commitKnowledgeRepo(t, source.RepoPath, "advance source")
	if err := check(); !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "after its verification") {
		t.Fatalf("source drift must refuse before absence diagnostics: %v", err)
	}
	_, _, err = validateKnowledgeHomeForQueryCore(ctx, s.db, source, false, "check_mandated_laws")
	if !failureAs(err, &failure) || failure.Kind != KindIndexDegraded || !strings.Contains(failure.RecoveryAction, "rebuild") {
		t.Fatalf("confirmed projection lag must retain its rebuild route: %v", err)
	}
}
