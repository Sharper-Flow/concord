package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const queryPublicationLaw = "publication-law"
const queryPublicationLawPath = ".concord/docs/decisions/CD-0911-publication.md"

func queryPublicationFixture(t *testing.T) (*Store, KnowledgeHome, func(string) string) {
	t.Helper()
	repo, advance := initFreshnessOriginRepo(t)
	writeManifestFixture(t, repo, manifestFixture{
		ID: queryPublicationLaw, Kind: "decision", Path: queryPublicationLawPath,
		Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Publication law",
		Summary: "Committed law remains accepted", Scopes: homeScope(),
	})
	queryPublicationCommit(t, repo, "publication law")
	runTestGit(t, repo, "push", "-q", "origin", "main")
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	home := KnowledgeHome{HomeProjectID: "query-publication-project", HomeLocatorID: "query-publication-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "query-publication-product", home, home.HomeProjectID)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	return s, home, advance
}

func queryPublicationCommit(t *testing.T, repo, message string) string {
	t.Helper()
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-q", "-m", message)
	return runTestGit(t, repo, "rev-parse", "HEAD")
}

func queryPublicationObserve(t *testing.T, s *Store, product string, home KnowledgeHome, comparison string) KnowledgePublication {
	t.Helper()
	got, err := s.ObserveKnowledgePublication(t.Context(), product, home.HomeProjectID, home.HomeLocatorID)
	if err != nil || got.Comparison != comparison {
		t.Fatalf("observe: got %+v, %v; want %s", got, err, comparison)
	}
	return got
}

func queryPublicationPair(t *testing.T, s *Store, home KnowledgeHome) (Q9Result, Q10Result) {
	t.Helper()
	// Store has one connection. A leaked query cursor makes this deadline fail
	// instead of parking the test forever when provenance reads the cache.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	q9, err := s.QueryQ9(ctx, Q9Request{Home: home, Text: queryPublicationLaw})
	if err != nil || q9.Authority != "authoritative" || len(q9.Items) != 1 || q9.Items[0].ID != queryPublicationLaw || q9.Items[0].OutcomeTag != "accepted" {
		t.Fatalf("Q9: %+v, %v", q9, err)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: queryPublicationLaw})
	if err != nil || q10.Authority != "authoritative" || q10.Status != "canonical" || q10.Note == nil || q10.Result.LawStatus != "accepted" {
		t.Fatalf("Q10: %+v, %v", q10, err)
	}
	if q9.Items[0].CommitOID != q10.Note.CommitOID || q9.Items[0].ContentHash != q10.Note.ContentHash {
		t.Fatalf("query proof mismatch: %+v, %+v", q9.Items[0], q10.Note)
	}
	return q9, q10
}

func assertQueryPublication(t *testing.T, q9 Q9Result, q10 Q10Result, home KnowledgeHome, served, published, basis, comparison string) {
	t.Helper()
	if len(q9.Publications) != 1 || q10.Publication == nil {
		t.Fatalf("missing served-query provenance: Q9=%+v Q10=%+v", q9.Publications, q10.Publication)
	}
	for _, got := range []KnowledgePublication{q9.Publications[0], *q10.Publication} {
		if got.ProjectID != home.HomeProjectID || got.LocatorID != home.HomeLocatorID || got.ServedCommit != served || got.PublicationCommit != published || got.Basis != basis || got.Comparison != comparison {
			t.Fatalf("publication = %+v; want %s/%s served=%s published=%s %s/%s", got, home.HomeProjectID, home.HomeLocatorID, served, published, basis, comparison)
		}
	}
	if q9.Publications[0].ServedCommit != q9.Items[0].CommitOID || q10.Publication.ServedCommit != q10.Note.CommitOID {
		t.Fatal("publication did not name the exact served proof commit")
	}
}

func TestKnowledgeQueryPublicationCachedMatchLagAndRebuild(t *testing.T) {
	s, home, advance := queryPublicationFixture(t)
	served := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	q9, q10 := queryPublicationPair(t, s, home)
	assertQueryPublication(t, q9, q10, home, served, "", "not_observed", "unknown")
	hash := q10.Note.ContentHash
	queryPublicationObserve(t, s, "query-publication-product", home, "match")
	q9, q10 = queryPublicationPair(t, s, home)
	assertQueryPublication(t, q9, q10, home, served, served, "cached_remote_observation", "match")
	published := advance("unrelated publication commit")
	observed := queryPublicationObserve(t, s, "query-publication-product", home, "lagging")
	goOfflineOrigin(t, home.RepoPath)
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
				t.Fatal(err)
			}
		}
		q9, q10 = queryPublicationPair(t, s, home)
		assertQueryPublication(t, q9, q10, home, served, published, "cached_remote_observation", "lagging")
		if q10.Note.ContentHash != hash || q9.Publications[0].ObservedAt != observed.ObservedAt || q10.Publication.ObservedAt != observed.ObservedAt || q10.Publication.PublicationRef != "refs/heads/main" {
			t.Fatalf("unchanged law or retained observation changed: %+v %+v", q9, q10)
		}
	}
	for _, tc := range []struct {
		result any
		key    string
	}{{q9, "publications"}, {q10, "publication"}} {
		encoded, err := json.Marshal(tc.result)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &wire); err != nil || len(wire[tc.key]) == 0 {
			t.Fatalf("missing %s on wire: %s, %v", tc.key, encoded, err)
		}
	}
}

func TestKnowledgeQueryPublicationUsesProjectedCommitAfterUnchangedHeadMove(t *testing.T) {
	s, home, advance := queryPublicationFixture(t)
	served := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	_, before := queryPublicationPair(t, s, home)
	published := advance("same law in a new publication")
	runTestGit(t, home.RepoPath, "fetch", "-q", "origin")
	runTestGit(t, home.RepoPath, "merge", "--ff-only", "origin/main")
	queryPublicationObserve(t, s, "query-publication-product", home, "match")
	if err := s.EnsureKnowledgeIndexFresh(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	q9, q10 := queryPublicationPair(t, s, home)
	assertQueryPublication(t, q9, q10, home, served, published, "cached_remote_observation", "unknown")
	if q10.Note.ContentHash != before.Note.ContentHash {
		t.Fatal("an unrelated publication commit changed law content")
	}
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	q9, q10 = queryPublicationPair(t, s, home)
	assertQueryPublication(t, q9, q10, home, published, published, "cached_remote_observation", "match")
	if q10.Note.ContentHash != before.Note.ContentHash {
		t.Fatal("rebuild changed unchanged law content")
	}
}

func TestKnowledgeQueryPublicationPreservesDirtyUnpublishedAndDiverged(t *testing.T) {
	s, home, advance := queryPublicationFixture(t)
	_, before := queryPublicationPair(t, s, home)
	writeKnowledgeFile(t, home.RepoPath, "local.txt", "unpublished unrelated file\n")
	served := queryPublicationCommit(t, home.RepoPath, "local unpublished change")
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	published := runTestGit(t, home.RepoPath, "rev-parse", "origin/main")
	for _, comparison := range []string{"unpublished", "diverged"} {
		if comparison == "diverged" {
			published = advance("independent publication")
		}
		queryPublicationObserve(t, s, "query-publication-product", home, comparison)
		writeKnowledgeFile(t, home.RepoPath, queryPublicationLawPath, "staged content is not law\n")
		runTestGit(t, home.RepoPath, "add", queryPublicationLawPath)
		writeKnowledgeFile(t, home.RepoPath, queryPublicationLawPath, "dirty content is not law\n")
		writeKnowledgeFile(t, home.RepoPath, "untracked.txt", "not committed\n")
		snapshot := func() map[string]string {
			files := map[string]string{}
			for _, path := range []string{queryPublicationLawPath, "untracked.txt", ".git/index", ".git/HEAD", ".git/config"} {
				body, err := os.ReadFile(filepath.Join(home.RepoPath, path))
				if err != nil {
					t.Fatal(err)
				}
				files[path] = string(body)
			}
			files["refs"] = runTestGit(t, home.RepoPath, "for-each-ref", "--format=%(refname) %(objectname)")
			return files
		}
		state := snapshot()
		q9, q10 := queryPublicationPair(t, s, home)
		assertQueryPublication(t, q9, q10, home, served, published, "cached_remote_observation", comparison)
		if q10.Note.ContentHash != before.Note.ContentHash || !reflect.DeepEqual(state, snapshot()) {
			t.Fatal("ordinary read served dirty law or changed checkout/index/config/refs")
		}
	}
}

func TestKnowledgeQueryPublicationFailedObservationOverridesSuccess(t *testing.T) {
	s, home, _ := queryPublicationFixture(t)
	served := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	success := queryPublicationObserve(t, s, "query-publication-product", home, "match")
	goOfflineOrigin(t, home.RepoPath)
	failed := queryPublicationObserve(t, s, "query-publication-product", home, "unknown")
	q9, q10 := queryPublicationPair(t, s, home)
	assertQueryPublication(t, q9, q10, home, served, failed.PublicationCommit, "cached_remote_observation", "unknown")
	for _, got := range []KnowledgePublication{q9.Publications[0], *q10.Publication} {
		if got.ObservedAt != failed.ObservedAt || got.Cause != failed.Cause || got.Recovery == "" || got.PreviousSuccess == nil || *got.PreviousSuccess != success.KnowledgePublicationRecord {
			t.Fatalf("latest failed observation did not own the result: %+v", got)
		}
	}
}

func TestKnowledgeQueryPublicationEmptyAndDegradedSources(t *testing.T) {
	for _, mode := range []string{"empty", "degraded", "unreachable"} {
		t.Run(mode, func(t *testing.T) {
			home := KnowledgeHome{HomeProjectID: "query-home", HomeLocatorID: "query-home-locator", HeadRef: "HEAD"}
			s, source := seedKnowledgeHomeProduct(t, "query-home-product", home, "SRC-LAW", ".concord/docs/decisions/CD-0902-source.md", "Home law")
			t.Cleanup(func() { s.Close() })
			sources, err := resolveKnowledgeQuerySources(t.Context(), s.db, "query-home-product", "test")
			if err != nil {
				t.Fatal(err)
			}
			commits := map[string]string{}
			observations := map[string]KnowledgePublication{}
			for _, registered := range sources {
				origin := queryPublicationSourceOrigin(t, registered.RepoPath)
				commits[registered.HomeProjectID] = runTestGit(t, registered.RepoPath, "rev-parse", "HEAD")
				observations[registered.HomeProjectID] = queryPublicationObserve(t, s, "query-home-product", registered, "match")
				// Moving the origin, without changing any local source binding,
				// makes an accidental observation fail instead of being hidden.
				if err := os.Rename(origin, origin+".offline"); err != nil {
					t.Fatal(err)
				}
			}
			text := "no-matching-law"
			if mode == "degraded" {
				writeManifestFixture(t, source.RepoPath, manifestFixture{ID: "SRC-LAW", Kind: "decision", Path: ".concord/docs/decisions/CD-0902-source.md", Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Changed home law", Summary: "Changed rule", Scopes: homeScope()})
				commitKnowledgeRepo(t, source.RepoPath, "changed source without rebuild")
				text = "law"
			} else if mode == "unreachable" {
				if err := os.Rename(source.RepoPath, source.RepoPath+".offline"); err != nil {
					t.Fatal(err)
				}
				text = "law"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			out, err := s.QueryQ9(ctx, Q9Request{Product: "query-home-product", Text: text, AllowDegraded: true})
			if err != nil || len(out.Publications) != 1 {
				t.Fatalf("all sources must have provenance, including empty/omitted: %+v, %v", out, err)
			}
			for i, got := range out.Publications {
				registered := sources[i]
				if got.ProjectID != registered.HomeProjectID || got.LocatorID != registered.HomeLocatorID || got.ServedCommit != commits[registered.HomeProjectID] || got.Basis != "cached_remote_observation" || got.ObservedAt != observations[registered.HomeProjectID].ObservedAt {
					t.Fatalf("source identity or cached revision lost: %+v", got)
				}
				want := "match"
				if mode != "empty" {
					want = "unknown"
				}
				if got.Comparison != want {
					t.Fatalf("source comparison = %+v, want %s", got, want)
				}
			}
			if mode == "empty" {
				if out.Authority != "authoritative" || len(out.Items) != 0 || len(out.SourceOmissions) != 0 {
					t.Fatalf("empty authoritative answer changed: %+v", out)
				}
			} else if out.Authority != "degraded" || len(out.Items) != 1 || out.Items[0].ID != "SRC-LAW" || len(out.SourceOmissions) != 0 {
				t.Fatalf("degraded omissions changed: %+v", out)
			}
		})
	}
}

func TestKnowledgeQueryPublicationSingleUnreachableProjection(t *testing.T) {
	s, home, _ := queryPublicationFixture(t)
	served := runTestGit(t, home.RepoPath, "rev-parse", "HEAD")
	queryPublicationObserve(t, s, "query-publication-product", home, "match")
	if err := os.Rename(home.RepoPath, home.RepoPath+".offline"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := s.QueryQ9(ctx, Q9Request{Home: home})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnreachable {
		t.Fatalf("strict unreachable semantics changed: %v", err)
	}
	out, err := s.QueryQ9(ctx, Q9Request{Home: home, AllowDegraded: true})
	if err != nil || out.Authority != "degraded" || out.IndexWatermark != "unreachable" || len(out.Items) != 1 || len(out.Publications) != 1 || out.Publications[0].ServedCommit != served || out.Publications[0].Comparison != "unknown" {
		t.Fatalf("degraded read must name actual cached commit, not reachability sentinel: %+v, %v", out, err)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: queryPublicationLaw, AllowDegraded: true})
	if err != nil || q10.Authority != "degraded" || q10.Status != "missing" || q10.Note != nil || q10.Publication != nil {
		t.Fatalf("unavailable proof must not claim a served note: %+v, %v", q10, err)
	}
}

func TestKnowledgeQueryPublicationHistoricalAndMissingNotes(t *testing.T) {
	s, home, _ := queryPublicationFixture(t)
	path := ".concord/docs/work/2026-09-20-publication-history.md"
	writeKnowledgeFile(t, home.RepoPath, path, canonicalWorkNote("publication-history-work", "2026-09-20T00:00:00Z"))
	commit := queryPublicationCommit(t, home.RepoPath, "historical work note")
	runTestGit(t, home.RepoPath, "push", "-q", "origin", "main")
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	queryPublicationObserve(t, s, "query-publication-product", home, "match")
	goOfflineOrigin(t, home.RepoPath)
	for _, req := range []Q10Request{{Work: "publication-history-work"}, {KnowledgeID: "publication-history-work"}} {
		out, err := s.QueryQ10(t.Context(), req)
		if err != nil || out.Status != "canonical" || out.Note == nil || out.Note.NotePath != path || out.Note.CommitOID != commit || out.Result.LawStatus != "" || out.Publication == nil {
			t.Fatalf("historical work note: %+v, %v", out, err)
		}
		if got := out.Publication; got.Basis != "historical_read" || got.Comparison != "unknown" || got.ProjectID != out.Note.HomeProjectID || got.LocatorID != out.Note.HomeLocatorID || got.ServedCommit != out.Note.CommitOID || got.PublicationCommit != "" {
			t.Fatalf("historical proof must not claim current publication: %+v", got)
		}
	}
	_, law := queryPublicationPair(t, s, home)
	if law.Publication.Basis != "cached_remote_observation" || law.Publication.Comparison != "match" {
		t.Fatalf("manifest proof at a recorded commit is still a served law read: %+v", law)
	}
	missing, err := s.QueryQ10(t.Context(), Q10Request{KnowledgeID: "absent-law"})
	if err != nil || missing.Status != "missing" || missing.Publication != nil {
		t.Fatalf("missing note must have no publication: %+v, %v", missing, err)
	}
	encoded, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, found := wire["publication"]; found {
		t.Fatalf("missing publication should be omitted: %s", encoded)
	}
}

func queryPublicationSourceOrigin(t *testing.T, repo string) string {
	t.Helper()
	fixture, err := queryPublicationFixtureDirectory(repo)
	if err != nil {
		t.Fatal(err)
	}
	common := runTestGit(t, fixture, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if common != filepath.Join(fixture, ".git") {
		t.Fatalf("publication fixture has an external Git common directory: %q", common)
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	runTestGit(t, fixture, "init", "-q", "--bare", "-b", "main", origin)
	runTestGit(t, fixture, "remote", "add", "origin", "https://query-publication.invalid/repository.git")
	runTestGit(t, fixture, "config", "--local", "url."+origin+".insteadOf", "https://query-publication.invalid/repository.git")
	runTestGit(t, fixture, "push", "-q", "origin", "HEAD:refs/heads/main")
	return origin
}

// queryPublicationAdvance publishes an unrelated commit from a throwaway
// clone, so the origin moves while every local head and watermark stays put.
func queryPublicationAdvance(t *testing.T, repo, message string) string {
	t.Helper()
	mapped := runTestGit(t, repo, "config", "--local", "--get-regexp", `^url\..*\.insteadOf$`)
	fields := strings.Fields(mapped)
	if len(fields) != 2 {
		t.Fatalf("fixture origin mapping missing: %q", mapped)
	}
	remote := strings.TrimSuffix(strings.TrimPrefix(fields[0], "url."), ".insteadof")
	seed := filepath.Join(t.TempDir(), "seed")
	runTestGit(t, t.TempDir(), "clone", "-q", remote, seed)
	writeKnowledgeFile(t, seed, "unrelated.txt", message+"\n")
	runTestGit(t, seed, "add", ".")
	runTestGit(t, seed, "commit", "-q", "-m", message)
	runTestGit(t, seed, "push", "-q", "origin", "HEAD:main")
	return runTestGit(t, seed, "rev-parse", "HEAD")
}

func queryPublicationConsumerFixture(t *testing.T) (*Store, KnowledgeHome, string) {
	t.Helper()
	home := KnowledgeHome{HomeProjectID: "qpub-c-home", HomeLocatorID: "qpub-c-home-loc", HeadRef: "HEAD"}
	s, home := seedKnowledgeHomeProduct(t, "qpub-consumer-product", home,
		"SRC-LAW", ".concord/docs/decisions/CD-0921-consumer-law.md", "Consumer law")
	queryPublicationSourceOrigin(t, home.RepoPath)
	queryPublicationObserve(t, s, "qpub-consumer-product", home, "match")
	seedKnowledgeWork(t, s, "qpub-consumer-work", "Consumer work")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE work_projects SET project_id=? WHERE work_id='qpub-consumer-work' AND role='primary'; DELETE FROM write_guard`, home.HomeProjectID); err != nil {
		t.Fatal(err)
	}
	return s, home, "qpub-consumer-work"
}

func queryPublicationConsumerProof(t *testing.T, s *Store, work string, mandated []string) []WorkflowLawRevision {
	t.Helper()
	proof, err := s.EstablishKnowledgeSourceSetProof(t.Context(), work)
	if err != nil {
		t.Fatalf("source-set proof refused active work: %v", err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(proof, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	revisions, err := deriveWorkflowLawRevisionsTx(proof, tx, work, mandated, nil)
	if err != nil {
		t.Fatalf("mandated-law derivation refused active work: %v", err)
	}
	return revisions
}

// queryPublicationPinState reads the retained contract-pin consumers for the
// work's one active contract: deleted-law staleness, same-ID amendments, and
// the consequential boundary every workflow mutation runs. Amendments are read
// only for a current pin, in the order the continuity read uses.
func queryPublicationPinState(t *testing.T, s *Store, home KnowledgeHome, work string) (*StaleLawRevision, []CompatibleLawAmendment, error) {
	t.Helper()
	db := s.DatabaseForTesting()
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, work).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active contracts = %d, %v; the boundary passes vacuously without exactly one", active, err)
	}
	boundary := s.Transact(t.Context(), func(tx *Transaction) error {
		return CheckWorkflowConsequentialBoundaryTx(t.Context(), tx, work)
	})
	stale, err := findStaleWorkflowLawRevision(t.Context(), db, home.HomeProjectID, home.HomeLocatorID, work, 1, []string{"SRC-LAW"})
	if err != nil {
		t.Fatalf("stale pin read: %v", err)
	}
	if stale != nil {
		return stale, nil, boundary
	}
	amendments, err := findCompatibleWorkflowLawAmendments(t.Context(), db, home.HomeProjectID, home.HomeLocatorID, work, 1, []string{"SRC-LAW"})
	if err != nil {
		t.Fatalf("amendment read: %v", err)
	}
	return nil, amendments, boundary
}

// TestKnowledgeQueryPublicationConsumerWorkUsableAcrossUnrelatedPublication
// exercises the delivered consequential consumers, not only query output: the
// work-scoped source-set proof, the transaction-scoped mandated-law boundary,
// and contract-pin derivation must keep an active work usable when the origin
// gains unrelated commits, the served law hash is unchanged, and the cached
// publication comparison is lagging.
func TestKnowledgeQueryPublicationConsumerWorkUsableAcrossUnrelatedPublication(t *testing.T) {
	s, home, work := queryPublicationConsumerFixture(t)
	revisions := queryPublicationConsumerProof(t, s, work, []string{"SRC-LAW"})
	if len(revisions) != 1 || revisions[0].LawID != "SRC-LAW" || revisions[0].ContentHash == "" {
		t.Fatalf("pins = %+v", revisions)
	}
	hash := revisions[0].ContentHash
	insertLawRevisionFixture(t, s, work, "SRC-LAW", hash)
	if err := checkMandatedLawsAtHome(t.Context(), s, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err != nil {
		t.Fatalf("mandated-law boundary refused before any publication movement: %v", err)
	}
	published := queryPublicationAdvance(t, home.RepoPath, "unrelated publication commit")
	queryPublicationObserve(t, s, "qpub-consumer-product", home, "lagging")
	revisions = queryPublicationConsumerProof(t, s, work, []string{"SRC-LAW"})
	if len(revisions) != 1 || revisions[0].ContentHash != hash {
		t.Fatalf("unrelated publication changed the governing law hash: %+v", revisions)
	}
	if err := checkMandatedLawsAtHome(t.Context(), s, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err != nil {
		t.Fatalf("mandated-law boundary refused after an unrelated publication: %v", err)
	}
	law, err := s.QueryQ10(t.Context(), Q10Request{Product: "qpub-consumer-product", KnowledgeID: "SRC-LAW"})
	if err != nil || law.Status != "canonical" || law.Note == nil || law.Note.ContentHash != hash || law.Publication == nil {
		t.Fatalf("served law changed under an unrelated publication: %+v, %v", law, err)
	}
	if got := law.Publication; got.Comparison != "lagging" || got.ServedCommit != law.Note.CommitOID || got.PublicationCommit != published {
		t.Fatalf("cached publication = %+v", got)
	}
	stale, amendments, boundary := queryPublicationPinState(t, s, home, work)
	if stale != nil || len(amendments) != 0 || boundary != nil {
		t.Fatalf("origin-only lag reached the contract pin: stale=%+v amendments=%+v boundary=%v", stale, amendments, boundary)
	}
}

// TestKnowledgeQueryPublicationChangedGoverningLawStaysScoped holds that an
// actually changed governing law reaches only the delivered scoped read
// boundaries. An accepted same-ID amendment keeps the source-set proof, the
// mandated-law boundary, and the contract pin's consequential boundary green,
// reports the amendment against the pinned hash, and serves the new committed
// hash. A deleted mandated law refuses through the mandated-law boundary and
// makes the pinned contract stale. Publication stays a read: no enforcement
// behavior is added.
func TestKnowledgeQueryPublicationChangedGoverningLawStaysScoped(t *testing.T) {
	s, home, work := queryPublicationConsumerFixture(t)
	revisions := queryPublicationConsumerProof(t, s, work, []string{"SRC-LAW"})
	if len(revisions) != 1 || revisions[0].ContentHash == "" {
		t.Fatalf("pins = %+v", revisions)
	}
	pinned := revisions[0].ContentHash
	insertLawRevisionFixture(t, s, work, "SRC-LAW", pinned)
	body := "amended SRC-LAW blob\n"
	writeKnowledgeFile(t, home.RepoPath, ".concord/docs/decisions/CD-0921-consumer-law.md", body)
	sum := sha256.Sum256([]byte(body))
	amended := KnowledgeManifest{
		SchemaVersion: "1.2", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"},
		Records: []KnowledgeRecord{{
			ID: "SRC-LAW", Kind: "decision", Path: ".concord/docs/decisions/CD-0921-consumer-law.md", Status: "accepted",
			Date: "2026-09-20T00:00:00Z", Title: "Amended consumer law", Summary: "Amended source rule", Tags: []string{},
			Authority: KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1},
			Scopes:    homeScope(), HomeDomainID: "fixture-law", SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		}},
	}
	amended.DomainRegistry = composeWorkingTreeManifest(t, home.RepoPath).DomainRegistry
	writeManifestShards(t, home.RepoPath, amended)
	queryPublicationCommit(t, home.RepoPath, "amend governing law")
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EstablishKnowledgeSourceSetProof(t.Context(), work); err != nil {
		t.Fatalf("source-set proof refused after a scoped rebuild: %v", err)
	}
	if err := checkMandatedLawsAtHome(t.Context(), s, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err != nil {
		t.Fatalf("mandated-law boundary refused a still-accepted amendment: %v", err)
	}
	stale, amendments, boundary := queryPublicationPinState(t, s, home, work)
	if stale != nil || boundary != nil {
		t.Fatalf("accepted amendment made the pin stale: stale=%+v boundary=%v", stale, boundary)
	}
	if want := []CompatibleLawAmendment{{LawID: "SRC-LAW", PinnedHash: pinned, CurrentHash: amended.Records[0].SHA256}}; !reflect.DeepEqual(amendments, want) {
		t.Fatalf("amendments = %+v, want %+v", amendments, want)
	}
	queryPublicationObserve(t, s, "qpub-consumer-product", home, "unpublished")
	law, err := s.QueryQ10(t.Context(), Q10Request{Product: "qpub-consumer-product", KnowledgeID: "SRC-LAW"})
	if err != nil || law.Status != "canonical" || law.Note == nil || law.Note.ContentHash != amended.Records[0].SHA256 || law.Publication == nil || law.Publication.Comparison != "unpublished" || law.Publication.ServedCommit != law.Note.CommitOID {
		t.Fatalf("amended law read = %+v, %v", law, err)
	}
	amended.Records = []KnowledgeRecord{}
	writeManifestShards(t, home.RepoPath, amended)
	queryPublicationCommit(t, home.RepoPath, "delete governing law")
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	if err := checkMandatedLawsAtHome(t.Context(), s, home.HomeProjectID, home.HomeLocatorID, []string{"SRC-LAW"}, nil, false); err == nil {
		t.Fatal("mandated-law boundary accepted a deleted law")
	}
	stale, _, boundary = queryPublicationPinState(t, s, home, work)
	if stale == nil || stale.OldLawID != "SRC-LAW" || stale.OldContentHash != pinned {
		t.Fatalf("deleted law left the pin current: %+v", stale)
	}
	var failure *Failure
	if !errors.As(boundary, &failure) || failure.Kind != KindStaleLawRevision || failure.StaleLawRevision == nil {
		t.Fatalf("consequential boundary = %v, want stale_law_revision", boundary)
	}
}
