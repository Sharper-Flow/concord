package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// inRelocationTx runs fn in one transaction that writes nothing.
func inRelocationTx(t *testing.T, s *Store, fn func(*Transaction, *sql.Tx) error) error {
	t.Helper()
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "relocation_test")
		if err != nil {
			return err
		}
		return fn(transaction, tx)
	})
}

// A pending relocation request is answered from current state: the pending
// source request, the replay target, and the landing state all hold with the
// event log unreachable. A second request on the
// same work item reopens the request its landing completed.
func TestSessionRelocationRequestReadersIgnoreTheLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	if _, err := s.ClaimWorktree(ctx, req); err != nil {
		t.Fatal(err)
	}
	source := claimPath(s)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{vacateRequestEvent("relocation-request-1", source)}}); err != nil {
		t.Fatal(err)
	}

	assertEventLogAbsent(t, s)
	if err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
		pending, err := PendingSessionVacateSourceRequestTx(ctx, transaction, "project-w", source, "ses-x")
		if err != nil || pending.WorkID != "work-w" || pending.DestinationDirectory != "/data/repo-main" {
			t.Fatalf("pending request=%+v err=%v", pending, err)
		}
		target, err := ResolveSessionVacateReplayTargetTx(ctx, transaction, "project-w", "/data/repo-main", "ses-x")
		if err != nil || target.WorkID != "work-w" || target.SourceDirectory != source {
			t.Fatalf("replay target=%+v err=%v", target, err)
		}
		landed, blocker, err := sessionVacateLandingStateTx(ctx, tx, "work-w", "ses-x")
		if err != nil || landed || !strings.Contains(blocker, "waits for its verified landing") {
			t.Fatalf("landing state landed=%v blocker=%q err=%v, want pending", landed, blocker, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertEventLogAbsent(t, s)

	if _, err := s.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{
		WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	assertEventLogAbsent(t, s)
	if err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
		if _, err := PendingSessionVacateSourceRequestTx(ctx, transaction, "project-w", source, "ses-x"); failureKind(err) != KindProjectionNotFound {
			t.Fatalf("err=%v, want projection_not_found once the landing completed the request", err)
		}
		if _, err := ResolveSessionVacateReplayTargetTx(ctx, transaction, "project-w", "/data/repo-main", "ses-x"); err != nil {
			t.Fatalf("completed replay err=%v, want the committed target", err)
		}
		landed, blocker, err := sessionVacateLandingStateTx(ctx, tx, "work-w", "ses-x")
		if err != nil || !landed {
			t.Fatalf("landing state landed=%v blocker=%q err=%v, want landed", landed, blocker, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertEventLogAbsent(t, s)

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{vacateRequestEvent("relocation-request-2", source)}}); err != nil {
		t.Fatal(err)
	}

	assertEventLogAbsent(t, s)
	if err := inRelocationTx(t, s, func(transaction *Transaction, tx *sql.Tx) error {
		if _, err := PendingSessionVacateSourceRequestTx(ctx, transaction, "project-w", source, "ses-x"); err != nil {
			t.Fatalf("second request err=%v, want it pending: the earlier landing does not complete it", err)
		}
		landed, _, err := sessionVacateLandingStateTx(ctx, tx, "work-w", "ses-x")
		if err != nil || landed {
			t.Fatalf("landing state landed=%v err=%v, want the second request pending", landed, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A verified placement and per-claim landings come from current
// state: after a vacate and a re-claim, each claim generation holds its own
// landing, the placement names the re-claimed Project only once the
// re-claim lands, and the retirement gate sees the re-claim's rows, all with
// the event log unreachable.
func TestSessionClaimLandingReadersIgnoreTheLog(t *testing.T) {
	t.Parallel()
	f := setupProjectHandoffFixture(t)
	ctx := context.Background()
	bindSessionToProjectWorktree(t, f.store, f.targetTree, f.targetSession)
	var firstClaim string
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT claim_op_id FROM worktree_entries WHERE path=? AND state='active'`, filepath.Clean(f.targetTree)).Scan(&firstClaim); err != nil {
		t.Fatal(err)
	}
	destination := projectMainCheckout(t, f.targetTree)
	request, err := json.Marshal(sessionVacatedPayload{WorkID: f.workID, ProjectID: f.targetProject, SessionRef: f.targetSession, SourceDirectory: filepath.Clean(f.targetTree), DestinationDirectory: filepath.Clean(destination)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, f.store, Operation{Events: []Event{{
		EventID: "relocation-vacate-target", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: f.targetSession, OccurredAt: time.Unix(50, 0).UTC(), PayloadVersion: 2, Payload: request,
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{WorkID: f.workID, SessionRef: f.targetSession, LandedDirectory: filepath.Clean(destination), HostPID: os.Getpid(), Now: time.Unix(51, 0).UTC()}); err != nil {
		t.Fatal(err)
	}
	baseOut, err := (ExecGitRunner{}).Run(ctx, destination, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, f.workID)
	if _, err := f.store.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: f.workID, ProjectID: f.targetProject, DefaultRef: "origin/main", PrincipalRef: "principal-2",
		RequestID: "relocation-reclaim", ExpectedVersion: version, Now: time.Unix(52, 0).UTC(), Runner: ExecGitRunner{},
	}); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := f.store.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: "relocation-op-2", WorkID: f.workID, ProjectID: f.targetProject,
		BaseSHA: strings.TrimSpace(string(baseOut)), PrincipalRef: "principal-2", RequestID: "relocation-req-2",
		SessionRef: f.targetSession, ExpectedVersion: version + 1, Now: time.Unix(52, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Before the re-claim lands, its occupancy row proves no placement.
	assertEventLogAbsent(t, f.store)
	if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
		placement, err := verifiedSessionPlacementTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || placement != "" {
			t.Fatalf("placement=%q err=%v, want none before the re-claim lands", placement, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertEventLogAbsent(t, f.store)

	if _, err := f.store.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: f.workID, SessionRef: f.targetSession, LandedDirectory: reclaimed.Entry.Path, HostPID: os.Getpid(), Now: time.Unix(53, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	assertEventLogAbsent(t, f.store)
	if err := inRelocationTx(t, f.store, func(_ *Transaction, tx *sql.Tx) error {
		placement, err := verifiedSessionPlacementTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || placement != f.targetProject {
			t.Fatalf("placement=%q err=%v, want %s", placement, err, f.targetProject)
		}
		for _, claim := range []string{firstClaim, reclaimed.Entry.ClaimOpID} {
			landed, err := sessionLandedInClaimTx(ctx, tx, f.workID, f.targetSession, claim)
			if err != nil || !landed {
				t.Fatalf("claim %s landed=%v err=%v, want its own recorded landing", claim, landed, err)
			}
		}
		landed, blocker, err := sessionVacateLandingStateTx(ctx, tx, f.workID, f.targetSession)
		if err != nil || landed || !strings.Contains(blocker, "occupancy") {
			t.Fatalf("landing state landed=%v blocker=%q err=%v, want refused by the re-claim's rows", landed, blocker, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// relocationFactRows renders the relocation state tables in key order.
func relocationFactRows(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, query := range []string{
		`SELECT 'v|'||work_id||'|'||session_ref||'|'||project_id||'|'||source_directory||'|'||destination_directory||'|'||request_seq||'|'||landed FROM session_vacate_requests ORDER BY work_id, session_ref, project_id, source_directory`,
		`SELECT 'c|'||work_id||'|'||session_ref||'|'||project_id||'|'||claim_op_id FROM session_claim_landings ORDER BY work_id, session_ref, project_id, claim_op_id`,
	} {
		rows, err := s.DatabaseForTesting().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

// Migration 141 carries existing stores' relocation state: the backfill
// derives exactly the rows the writers record, so a pending request, a
// completed one, and a claim landing keep their meaning across the upgrade.
// A request recorded before the landing-verified release leaves no row.
func TestSessionRelocationBackfillMatchesTheFolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, git, _ := worktreeFixture(t)
	req := baseClaim(git)
	req.SessionRef = "ses-x"
	claim, err := s.ClaimWorktree(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	source := claimPath(s)
	claimLanding, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: source, HostPID: os.Getpid(), Now: time.Unix(45, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	corpus := []Event{{EventID: "work-w:session-claim-landed:ses-x:1", Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "ses-x", OccurredAt: time.Unix(45, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(sessionClaimLandedPayload{WorkID: "work-w", ProjectID: "project-w", ClaimOpID: claim.Entry.ClaimOpID, SessionRef: "ses-x", SourceDirectories: claimLanding.ReleasedSources, LandedDirectory: source, HostPID: os.Getpid(), HostPIDStart: claimLanding.HostPIDStart})}}
	first := vacateRequestEvent("backfill-request-1", source)
	corpus = append(corpus, first)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{first}}); err != nil {
		t.Fatal(err)
	}
	landing, err := s.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{WorkID: "work-w", SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(60, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	landingEvent := func(work string, result SessionVacateLandingResult, at int64) Event {
		return Event{EventID: work + ":session-vacate-landed:ses-x:1", Kind: "work.session_vacate_landed", SubjectType: SubjectWorkItem, SubjectID: work, Actor: "ses-x", OccurredAt: time.Unix(at, 0).UTC(), PayloadVersion: 1,
			Payload: mustJSONValue(sessionVacateLandedPayload{WorkID: work, ProjectID: "project-w", SessionRef: "ses-x", SourceDirectories: result.ReleasedSources, DestinationDirectory: "/data/repo-main", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), HostPIDStart: result.HostPIDStart})}
	}
	corpus = append(corpus, landingEvent("work-w", landing, 60))
	second := vacateRequestEvent("backfill-request-2", source)
	corpus = append(corpus, second)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{second}}); err != nil {
		t.Fatal(err)
	}
	other := vacateRequestEvent("backfill-other-work", source)
	other.SubjectID = "work-relocation-other"
	other.Payload = mustJSONValue(sessionVacatedPayload{WorkID: other.SubjectID, ProjectID: "project-w", SessionRef: "ses-x", SourceDirectory: source, DestinationDirectory: "/data/repo-main"})
	corpus = append(corpus, other)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{other}}); err != nil {
		t.Fatal(err)
	}
	otherLanding, err := s.RecordSessionVacateLanding(ctx, SessionVacateLandingRequest{WorkID: other.SubjectID, SessionRef: "ses-x", LandedDirectory: "/data/repo-main", HostPID: os.Getpid(), Now: time.Unix(62, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	corpus = append(corpus, landingEvent(other.SubjectID, otherLanding, 62))
	folded := relocationFactRows(t, s)
	if len(folded) != 3 || !strings.HasSuffix(folded[0], "|1") || !strings.HasSuffix(folded[1], "|0") {
		t.Fatalf("folded rows=%v, want the landed cross-work request, pending second request, and one claim landing", folded)
	}
	// CD-0242 D2 removes the current-store replay construction. Migration141
	// still consumes a complete, explicit relocation population at prefix140.
	path := filepath.Join(t.TempDir(), "relocation-140.db")
	db := openMigratedTo(t, path, 140)
	var currentPopulation int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind IN ('work.session_vacated','work.session_vacate_landed','work.session_claim_landed')`).Scan(&currentPopulation); err != nil || currentPopulation != len(corpus) {
		t.Fatalf("current relocation population=%d,%v; corpus=%d", currentPopulation, err, len(corpus))
	}
	for _, event := range corpus {
		var seq int64
		var digest string
		if err := s.db.QueryRow(`SELECT effect_seq,payload_digest FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&seq, &digest); err != nil {
			t.Fatal(err)
		}
		if digest != historicalFixturePayloadDigest(t, string(event.Payload)) {
			t.Fatalf("explicit relocation source %s differs from live route", event.EventID)
		}
		if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`, seq, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			t.Fatal(err)
		}
	}
	excluded := []Event{
		{EventID: "backfill-v1", Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "ses-old", OccurredAt: time.Unix(61, 0).UTC(), PayloadVersion: 1,
			Payload: jsonRaw(`{"work_id":"work-w","project_id":"project-w","session_ref":"ses-old","source_directory":"` + source + `","destination_directory":"/data/repo-main","landed_directory":"/data/repo-main"}`)},
		{EventID: "backfill-unbound-claim", Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "ses-old", OccurredAt: time.Unix(63, 0).UTC(), PayloadVersion: 1,
			Payload: mustJSONValue(sessionClaimLandedPayload{WorkID: "work-w", ProjectID: "project-w", SessionRef: "ses-old", LandedDirectory: source})},
	}
	insertHistoricalEvents(t, db, excluded)
	before := histEventRows(t, &Store{db: db})
	for _, row := range before {
		t.Logf("migration141 source seq=%d id=%s kind=%s actor=%s time=%s digest=%s", row.Seq, row.EventID, row.Kind, row.Actor, row.OccurredAt, historicalFixturePayloadDigest(t, row.Payload))
	}
	if len(before) != len(corpus)+2 {
		t.Fatalf("historical relocation population=%d, want %d", len(before), len(corpus)+2)
	}
	advanceHistoricalPrefix(t, db, 141)
	upgraded := &Store{db: db, path: path}
	if got := relocationFactRows(t, upgraded); !reflect.DeepEqual(got, folded) {
		t.Fatalf("migration141 latest-request/landing selection: got %v, want %v", got, folded)
	}
	var sequencesMatch bool
	if err := db.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM session_vacate_requests r WHERE r.request_seq<>(SELECT max(seq) FROM domain_events e WHERE e.kind='work.session_vacated' AND e.payload_version=2 AND e.subject_id=r.work_id AND json_extract(e.payload,'$.session_ref')=r.session_ref AND json_extract(e.payload,'$.project_id')=r.project_id AND json_extract(e.payload,'$.source_directory')=r.source_directory))`).Scan(&sequencesMatch); err != nil || !sequencesMatch {
		t.Fatalf("request sequences match their writing effects=%v err=%v", sequencesMatch, err)
	}
	if got := histEventRows(t, upgraded); !reflect.DeepEqual(got, before) {
		t.Fatal("relocation migration changed original bytes, actor, identity, sequence or time")
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertHistoricalReceipts(t, db, before)
	assertEventLogAbsent(t, upgraded)
	if backfilled := relocationFactRows(t, upgraded); strings.Join(backfilled, "\n") != strings.Join(folded, "\n") {
		t.Fatalf("backfill rows:\n%s\nfolded rows:\n%s", strings.Join(backfilled, "\n"), strings.Join(folded, "\n"))
	}
	for _, row := range before {
		if row.Kind == "work.session_vacated" && row.Version == 2 {
			var p sessionVacatedPayload
			if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
				t.Fatal(err)
			}
			if row.EventID != second.EventID && row.SubjectID == "work-w" {
				continue
			}
			var seq int64
			if err := db.QueryRow(`SELECT request_seq FROM session_vacate_requests WHERE work_id=? AND session_ref=? AND project_id=? AND source_directory=?`, row.SubjectID, p.SessionRef, p.ProjectID, p.SourceDirectory).Scan(&seq); err != nil || seq != row.Seq {
				t.Fatalf("%s request sequence=%d,%v; want %d", row.EventID, seq, err, row.Seq)
			}
		}
	}
	queries := []string{
		`SELECT json_array(work_id,session_ref,project_id,source_directory,destination_directory,landed) FROM session_vacate_requests ORDER BY work_id,session_ref,project_id,source_directory`,
		`SELECT json_array(work_id,session_ref,project_id,claim_op_id) FROM session_claim_landings ORDER BY work_id,session_ref,project_id,claim_op_id`,
	}
	assertCorpusSelectionRed(t, historicalFactCorpus{events: append(corpus, excluded...), parents: func(*testing.T, *Store, string) {}}, 141, "SELECT max(o.seq)", "SELECT min(o.seq)", queries, workerFactsQueryRows(t, s, queries))
}
