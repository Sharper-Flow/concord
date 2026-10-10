package store

import (
	"context"
	"database/sql"
	"reflect"
	"strconv"
	"testing"
)

// synthExecPath creates corruption through a foreign connection without
// foreign keys, while retaining the canonical projection write guards.
func synthExecPath(t *testing.T, path string, queries ...string) {
	t.Helper()
	db, err := sql.Open(driverName, "file:"+path+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM fold_guard`) }()
	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("path synthetic query: %v\n%s", err, q)
		}
	}
}

func retireResearchForTest(t *testing.T, s *Store, req RetireResearchPacksRequest) (RetireResearchPacksResult, error) {
	t.Helper()
	var out RetireResearchPacksResult
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		out, err = RetireResearchPacksWithinTx(context.Background(), tx, req)
		return err
	})
	return out, err
}

func assertRetirementPackCount(t *testing.T, s *Store, packID string, want int) {
	t.Helper()
	var got int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id=?`, packID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pack %s count=%d, want %d", packID, got, want)
	}
}

func TestResearchRetirementTerminalOwnersEligible(t *testing.T) {
	for _, lifecycle := range []string{"completed", "cancelled", "superseded"} {
		t.Run(lifecycle, func(t *testing.T) {
			s := openTemp(t)
			seedResearchWork(t, s, "owner", "successor")
			pack := createSimplePack(t, s, "terminal-retirement", "owner")
			event := operationEventForResearch("retirement-terminal", "work.transitioned", "owner", map[string]any{"from": "needed", "to": lifecycle, "reason": "done", "expected_version": 2, "resulting_version": 3})
			if lifecycle == "superseded" {
				event = operationEventForResearch("retirement-terminal", "work.superseded", "owner", map[string]any{"successor": "successor", "superseded": "owner", "reason": "done", "expected_version": 2, "resulting_version": 3})
			}
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 2}}); err != nil {
				t.Fatal(err)
			}
			out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 1}}})
			want := RetireResearchPacksResult{Candidates: []ResearchRetirementCandidateResult{{PackID: pack.PackID, OwnerWorkID: "owner", ExpectedVersion: 1, CurrentVersion: 1, Classification: ResearchRetirementRetired}}}
			if err != nil || !reflect.DeepEqual(out, want) {
				t.Fatalf("retirement=%+v err=%v, want %+v", out, err, want)
			}
			assertRetirementPackCount(t, s, pack.PackID, 0)
		})
	}
}

func TestResearchRetirementCascadesContent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "retirement-content", "owner")
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("retirement-finding"), PackID: pack.PackID, ExpectedVersion: 1, Finding: ResearchFinding{FindingID: "finding", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, Scopes: ResearchScopes{Mode: "explicit", ProductIDs: []string{"product"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("retirement-source"), PackID: pack.PackID, ExpectedVersion: 2, Source: ResearchSource{SourceID: "source", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Source", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if err := BindResearchFindingSource(ctx, s, ResearchFindingSourceRequest{Identity: researchIdentity("retirement-citation"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, FindingID: "finding", SourceID: "source"}); err != nil {
		t.Fatal(err)
	}
	tables := []string{"active_research_packs", "active_research_revisions", "active_research_findings", "active_research_sources", "active_research_finding_sources", "active_research_finding_scopes"}
	for _, table := range tables {
		if got := countRows(t, s, table); got != 1 {
			t.Fatalf("fixture %s count=%d, want 1", table, got)
		}
	}
	terminalizeResearchOwner(t, s, "owner")
	out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 4}}})
	if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementRetired {
		t.Fatalf("retirement=%+v err=%v", out, err)
	}
	for _, table := range tables {
		if got := countRows(t, s, table); got != 0 {
			t.Fatalf("retired %s count=%d, want 0", table, got)
		}
	}
}

func TestResearchRetirementActivePinsProtectAndTerminalReleaseFences(t *testing.T) {
	for _, required := range []bool{false, true} {
		name := "optional"
		if required {
			name = "required"
		}
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			seedResearchWork(t, s, "owner", "consumer")
			pack := createSimplePack(t, s, "retirement-pin", "owner")
			if _, err := BindResearchConsumer(context.Background(), s, BindResearchConsumerRequest{Identity: researchIdentity("retirement-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: required}}); err != nil {
				t.Fatal(err)
			}
			terminalizeResearchOwner(t, s, "owner")
			req := RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 2}}}
			out, err := retireResearchForTest(t, s, req)
			if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementProtected || out.Candidates[0].ProtectionReason != "active_pin" {
				t.Fatalf("protected retirement=%+v err=%v", out, err)
			}
			assertRetirementPackCount(t, s, pack.PackID, 1)
			if got := countRows(t, s, "active_research_consumers"); got != 1 {
				t.Fatalf("protected consumer count=%d, want 1", got)
			}
			terminalizeResearchOwner(t, s, "consumer")
			out, err = retireResearchForTest(t, s, req)
			if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementVersionConflict || out.Candidates[0].CurrentVersion != 3 {
				t.Fatalf("released stale retirement=%+v err=%v", out, err)
			}
			assertRetirementPackCount(t, s, pack.PackID, 1)
			req.Candidates[0].ExpectedVersion = 3
			out, err = retireResearchForTest(t, s, req)
			if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementRetired {
				t.Fatalf("released retirement=%+v err=%v", out, err)
			}
			assertRetirementPackCount(t, s, pack.PackID, 0)
		})
	}
}

func TestResearchRetirementDryRunMixedBatchHasNoWrites(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "terminal", "active", "stale")
	eligible := createSimplePack(t, s, "retirement-eligible", "terminal")
	active := createSimplePack(t, s, "retirement-active", "active")
	stale := createSimplePack(t, s, "retirement-stale", "stale")
	terminalizeResearchOwner(t, s, "terminal")
	terminalizeResearchOwner(t, s, "stale")
	req := RetireResearchPacksRequest{ProductID: "product", DryRun: true, Candidates: []ResearchRetirementCandidate{
		{PackID: eligible.PackID, ExpectedVersion: 1},
		{PackID: active.PackID, ExpectedVersion: 1},
		{PackID: stale.PackID, ExpectedVersion: 2},
	}}
	var dry RetireResearchPacksResult
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "retirement-dry-run-test")
		if err != nil {
			return err
		}
		var before, after int64
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
			return err
		}
		dry, err = RetireResearchPacksWithinTx(ctx, transaction, req)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil {
			return err
		}
		if before != after {
			t.Errorf("dry run wrote to the database: changes %d -> %d", before, after)
		}
		return nil
	})
	want := RetireResearchPacksResult{DryRun: true, Candidates: []ResearchRetirementCandidateResult{
		{PackID: eligible.PackID, OwnerWorkID: "terminal", ExpectedVersion: 1, CurrentVersion: 1, Classification: ResearchRetirementEligible},
		{PackID: active.PackID, OwnerWorkID: "active", ExpectedVersion: 1, CurrentVersion: 1, Classification: ResearchRetirementProtected, ProtectionReason: "owner_active"},
		{PackID: stale.PackID, OwnerWorkID: "stale", ExpectedVersion: 2, CurrentVersion: 1, Classification: ResearchRetirementVersionConflict},
	}}
	if err != nil || !reflect.DeepEqual(dry, want) {
		t.Fatalf("dry run=%+v err=%v, want %+v", dry, err, want)
	}
	for _, candidate := range req.Candidates {
		assertRetirementPackCount(t, s, candidate.PackID, 1)
	}
	req.DryRun = false
	out, err := retireResearchForTest(t, s, req)
	want.DryRun = false
	want.Candidates[0].Classification = ResearchRetirementRetired
	if err != nil || !reflect.DeepEqual(out, want) {
		t.Fatalf("real retirement=%+v err=%v, want %+v", out, err, want)
	}
	assertRetirementPackCount(t, s, eligible.PackID, 0)
	assertRetirementPackCount(t, s, active.PackID, 1)
	assertRetirementPackCount(t, s, stale.PackID, 1)
}

func TestResearchRetirementWrongScopePrecedesEveryDeletion(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	otherOwner := operationEvent("retirement-other-owner", "work.created", SubjectWorkItem, "other-owner", map[string]any{"work_kind": "research", "title": "Other owner", "priority": 1})
	otherOwner.PayloadVersion = 2
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		operationEvent("retirement-other-product", "product.created", SubjectProduct, "other-product", map[string]any{"display_name": "Other", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"}),
		operationEvent("retirement-other-project", "project.created", SubjectProject, "other-project", map[string]any{"display_name": "Other"}),
		operationEvent("retirement-other-product-project", "product_project.added", SubjectProduct, "other-product", map[string]any{"product_id": "other-product", "project_id": "other-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
		otherOwner,
		operationEvent("retirement-other-owner-project", "work_project.added", SubjectWorkItem, "other-owner", map[string]any{"work_id": "other-owner", "project_id": "other-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "other-product"): 0, VersionRef(SubjectProject, "other-project"): 0, VersionRef(SubjectWorkItem, "other-owner"): 0}}); err != nil {
		t.Fatal(err)
	}
	local := createSimplePack(t, s, "retirement-local", "owner")
	foreign := createSimplePack(t, s, "retirement-foreign", "other-owner")
	terminalizeResearchOwner(t, s, "owner")
	terminalizeResearchOwner(t, s, "other-owner")
	for _, dryRun := range []bool{false, true} {
		var refusal error
		// Commit even after the refusal to prove that scope validation precedes
		// deletion, rather than relying only on the caller's rollback.
		if err := s.Transact(ctx, func(transaction *Transaction) error {
			out, err := RetireResearchPacksWithinTx(ctx, transaction, RetireResearchPacksRequest{ProductID: "product", DryRun: dryRun, Candidates: []ResearchRetirementCandidate{{PackID: local.PackID, ExpectedVersion: 1}, {PackID: foreign.PackID, ExpectedVersion: 1}}})
			refusal = err
			if len(out.Candidates) != 0 {
				t.Errorf("wrong-scope batch exposed results: %+v", out)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		assertFailureKind(t, refusal, KindUnauthorized)
		assertRetirementPackCount(t, s, local.PackID, 1)
		assertRetirementPackCount(t, s, foreign.PackID, 1)
	}
}

func TestResearchRetirementValidatesWholeCandidateList(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "retirement-validation", "owner")
	terminalizeResearchOwner(t, s, "owner")
	candidate := ResearchRetirementCandidate{PackID: pack.PackID, ExpectedVersion: 1}
	tooMany := make([]ResearchRetirementCandidate, 101)
	for i := range tooMany {
		tooMany[i] = candidate
	}
	for _, tc := range []struct {
		name string
		req  RetireResearchPacksRequest
		kind FailureKind
	}{
		{"empty", RetireResearchPacksRequest{ProductID: "product"}, KindInvalidOperation},
		{"too_many", RetireResearchPacksRequest{ProductID: "product", Candidates: tooMany}, KindInvalidOperation},
		{"missing_product", RetireResearchPacksRequest{Candidates: []ResearchRetirementCandidate{candidate}}, KindInvalidOperation},
		{"duplicate", RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{candidate, candidate}}, KindInvalidOperation},
		{"invalid_version", RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{candidate, {PackID: "invalid", ExpectedVersion: 0}}}, KindInvalidOperation},
		{"empty_pack", RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{candidate, {ExpectedVersion: 1}}}, KindInvalidOperation},
		{"missing_pack", RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{candidate, {PackID: "missing", ExpectedVersion: 1}}}, KindProjectionNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refusal error
			// A committed refusal must still leave the earlier valid candidate
			// untouched: validation must finish before the first deletion.
			if err := s.Transact(context.Background(), func(transaction *Transaction) error {
				_, refusal = RetireResearchPacksWithinTx(context.Background(), transaction, tc.req)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			assertFailureKind(t, refusal, tc.kind)
			assertRetirementPackCount(t, s, pack.PackID, 1)
		})
	}
}

func TestResearchRetirementAcceptsHundredCandidates(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	req := RetireResearchPacksRequest{ProductID: "product", DryRun: true}
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		for i := range 100 {
			pack, err := CreateResearchPackWithinTx(ctx, transaction, CreateResearchPackRequest{PackID: "bounded-pack-" + strconv.Itoa(i), OwnerWorkID: "owner", Revision: simpleResearchRevision()})
			if err != nil {
				return err
			}
			req.Candidates = append(req.Candidates, ResearchRetirementCandidate{PackID: pack.PackID, ExpectedVersion: pack.ExpectedVersion})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	for _, dryRun := range []bool{true, false} {
		req.DryRun = dryRun
		out, err := retireResearchForTest(t, s, req)
		if err != nil || len(out.Candidates) != 100 {
			t.Fatalf("100-candidate retirement count=%d err=%v", len(out.Candidates), err)
		}
		want := ResearchRetirementRetired
		if dryRun {
			want = ResearchRetirementEligible
		}
		for i, result := range out.Candidates {
			if result.PackID != req.Candidates[i].PackID || result.Classification != want {
				t.Fatalf("candidate %d=%+v, want %s %s", i, result, req.Candidates[i].PackID, want)
			}
		}
	}
	if got := countRows(t, s, "active_research_packs"); got != 0 {
		t.Fatalf("100-candidate retirement left %d packs", got)
	}
}
