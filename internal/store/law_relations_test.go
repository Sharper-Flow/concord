package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func lawTestRecord(id, kind, status, path string, relations ...KnowledgeRelation) KnowledgeRecord {
	authority := KnowledgeAuthority{Tier: "derived"}
	if kind == "constitution" || kind == "decision" {
		authority = KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1}
	}
	return KnowledgeRecord{
		ID: id, Kind: kind, Path: path, Status: status, Date: "2026-08-11T00:00:00Z",
		Authority: authority,
		Title:     id, Summary: "law test record", Tags: []string{},
		Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
		SHA256: "sha256:" + strings.Repeat("a", 64), LawRelations: relations,
	}
}

func lawTestManifest(records ...KnowledgeRecord) KnowledgeManifest {
	for index := range records {
		if records[index].Status == "accepted" && manifestLawBearingKinds[records[index].Kind] {
			records[index].HomeDomainID = "product-root:concord"
			records[index].ProductWideRationale = "Fixture law binds every child Domain."
		}
	}
	return KnowledgeManifest{
		SchemaVersion: "1.2", SupportedKinds: []string{"lesson", "decision", "spec"}, IndexedKinds: []string{"lesson", "decision", "spec"}, Records: records,
		DomainRegistry: KnowledgeDomainRegistry{SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord", Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}}},
	}
}

func TestKnowledgeManifestRelationsRequireCurrentSchema(t *testing.T) {
	t.Parallel()
	valid := lawTestManifest(
		lawTestRecord("base", "decision", "accepted", ".concord/docs/decisions/CD-0001-base.md", KnowledgeRelation{Kind: "refines", TargetID: "detail"}),
		lawTestRecord("detail", "spec", "accepted", ".concord/docs/detail.md"),
	)
	if err := validateKnowledgeManifest(valid); err != nil {
		t.Fatalf("valid 1.2 relation rejected: %v", err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := parseKnowledgeManifest(encoded); err != nil || parsed.SchemaVersion != "1.2" || len(parsed.Records[0].LawRelations) != 1 {
		t.Fatalf("strict 1.2 parser result=%+v err=%v", parsed, err)
	}
	for _, version := range []string{"1.0", "1.1"} {
		legacy := valid
		legacy.SchemaVersion = version
		assertFailureKind(t, validateKnowledgeManifest(legacy), KindInvalidNoteProof)
	}
}

func TestKnowledgeManifestRelationsRejectInvalidGraphs(t *testing.T) {
	t.Parallel()
	base := func(relations ...KnowledgeRelation) KnowledgeManifest {
		return lawTestManifest(
			lawTestRecord("a", "decision", "accepted", ".concord/docs/decisions/CD-0001-a.md", relations...),
			lawTestRecord("b", "spec", "accepted", ".concord/docs/b.md"),
		)
	}
	for name, manifest := range map[string]KnowledgeManifest{
		"unknown target": base(KnowledgeRelation{Kind: "refines", TargetID: "missing"}),
		"self":           base(KnowledgeRelation{Kind: "refines", TargetID: "a"}),
		"unknown kind":   base(KnowledgeRelation{Kind: "binds", TargetID: "b"}),
		"reverse conflict": lawTestManifest(
			lawTestRecord("a", "decision", "accepted", ".concord/docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "b"}),
			lawTestRecord("b", "decision", "accepted", ".concord/docs/decisions/CD-0002-b.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "a"}),
		),
		"cycle": lawTestManifest(
			lawTestRecord("a", "decision", "accepted", ".concord/docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "refines", TargetID: "b"}),
			lawTestRecord("b", "decision", "accepted", ".concord/docs/decisions/CD-0002-b.md", KnowledgeRelation{Kind: "refines", TargetID: "a"}),
		),
		"mixed directed cycle": lawTestManifest(
			lawTestRecord("a", "decision", "accepted", ".concord/docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "refines", TargetID: "b"}),
			lawTestRecord("b", "decision", "accepted", ".concord/docs/decisions/CD-0002-b.md", KnowledgeRelation{Kind: "subordinate_to", TargetID: "a"}),
		),
		"lesson endpoint": lawTestManifest(
			lawTestRecord("lesson", "lesson", "published", ".concord/docs/lesson.md", KnowledgeRelation{Kind: "refines", TargetID: "a"}),
			lawTestRecord("a", "decision", "accepted", ".concord/docs/decisions/CD-0001-a.md"),
		),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateKnowledgeManifest(manifest); err == nil {
				t.Fatal("invalid law relation accepted")
			}
		})
	}
}

func TestRebuildKnowledgeIndexProjectsAndRollsBackLawRelations(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	records := []KnowledgeRecord{
		lawTestRecord("law-a", "decision", "accepted", ".concord/docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "law-b"}),
		lawTestRecord("law-b", "spec", "accepted", ".concord/docs/law-b.md"),
	}
	for i, record := range records {
		content := record.ID + "\n"
		writeKnowledgeFile(t, repo, record.Path, content)
		sum := sha256.Sum256([]byte(content))
		records[i].SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	}
	writeLawManifest(t, repo, records)
	commit := commitKnowledgeRepo(t, repo, "typed laws")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "law-project", HomeLocatorID: "law-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	var source, target, scanned, declarer string
	if err := s.DatabaseForTesting().QueryRow(`SELECT source_law_id,target_law_id,scanned_commit_oid,declarer_law_id FROM law_relations`).Scan(&source, &target, &scanned, &declarer); err != nil {
		t.Fatal(err)
	}
	if source != "law-a" || target != "law-b" || scanned != commit || declarer != "law-a" {
		t.Fatalf("projected conflict = %s -> %s at %s declared by %s", source, target, scanned, declarer)
	}
	before := lawProjectionSnapshot(t, s)
	bad := records
	bad[0].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: "missing"}}
	writeLawManifest(t, repo, bad)
	commitKnowledgeRepo(t, repo, "invalid typed law")
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err == nil {
		t.Fatal("invalid law relation rebuild succeeded")
	}
	if after := lawProjectionSnapshot(t, s); after != before {
		t.Fatalf("failed law rebuild changed projection: before=%s after=%s", before, after)
	}
}

// TestRebuildRecordsTheDeclarerOfAConflictStoredInIDOrder holds that the
// projection keeps which record declared a conflict when the pair is stored in
// ID order with the declarer second, and that a demand-driven freshness check
// rebuilds a conflict row an older writer left without a declarer.
func TestRebuildRecordsTheDeclarerOfAConflictStoredInIDOrder(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	records := []KnowledgeRecord{
		lawTestRecord("law-z", "spec", "accepted", ".concord/docs/law-z.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "law-b"}),
		lawTestRecord("law-b", "spec", "accepted", ".concord/docs/law-b.md"),
	}
	for i, record := range records {
		content := record.ID + "\n"
		writeKnowledgeFile(t, repo, record.Path, content)
		sum := sha256.Sum256([]byte(content))
		records[i].SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	}
	writeLawManifest(t, repo, records)
	commitKnowledgeRepo(t, repo, "declared conflict")
	s := openTemp(t)
	ctx := context.Background()
	home := KnowledgeHome{HomeProjectID: "law-project", HomeLocatorID: "law-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "concord", home)
	declared := func() (string, string, sql.NullString) {
		t.Helper()
		var source, target string
		var declarer sql.NullString
		if err := s.DatabaseForTesting().QueryRow(`SELECT source_law_id,target_law_id,declarer_law_id FROM law_relations WHERE kind='conflicts_with'`).Scan(&source, &target, &declarer); err != nil {
			t.Fatal(err)
		}
		return source, target, declarer
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	if source, target, declarer := declared(); source != "law-b" || target != "law-z" || declarer.String != "law-z" {
		t.Fatalf("projected conflict = %s -> %s declared by %v, want law-b -> law-z declared by law-z", source, target, declarer)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE law_relations SET declarer_law_id=NULL; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, _, declarer := declared(); declarer.String != "law-z" {
		t.Fatalf("a fresh check left a conflict without its declarer: %v", declarer)
	}
}

func TestMandatedLawBoundaryChecksUnknownConflictAndAmendmentSubset(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	anchorHomePair(t, s, "p", "l")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','a','decision','accepted','.concord/docs/decisions/CD-0001-a.md','A','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit'),('p','l','b','spec','accepted','.concord/docs/b.md','B','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit'); INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid,declarer_law_id) VALUES('p','l','a','conflicts_with','b','commit','a'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"missing"}, nil, true); err == nil {
		t.Fatal("unknown mandated law accepted")
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, nil, true); err == nil {
		t.Fatal("unresolved conflict accepted without amendment endpoint")
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Fatalf("explicit amendment path rejected: %v", err)
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a", "b"}, []string{"a"}, false); err == nil {
		t.Fatal("completion-style check accepted unresolved conflict")
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", []string{"a"}, []string{"b"}, true); err == nil {
		t.Fatal("undeclared law modification accepted")
	}
}

func TestMandatedLawCheckFailsClosedOnConflictOverflow(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	anchorHomePair(t, s, "p", "l")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 32)
	for i := range ids {
		ids[i] = "law-" + string(rune('a'+i))
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l',?,'decision','accepted',?,'law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit')`, ids[i], ".concord/docs/decisions/CD-0001-"+ids[i]+".md"); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for i := range ids {
		for j := i + 1; j < len(ids) && count < 33; j++ {
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l',?,'conflicts_with',?,'commit')`, ids[i], ids[j]); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := checkMandatedLawsAtHome(ctx, s, "p", "l", ids, ids, true); err == nil {
		t.Fatal("amendment check silently accepted an overflow")
	} else {
		assertFailureKind(t, err, KindInvalidPayload)
	}
}

func TestLawBoundaryVersionGatesV22(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, workID string) (*Store, string) {
		t.Helper()
		s := openTemp(t)
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/law-boundary")
		digest := workflowFixtureDigest(t)
		events := []Event{
			workflowEvent("law-boundary-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/law-boundary", "actor_class": "agent"}),
			workflowEvent("law-boundary-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		}
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
			t.Fatal(err)
		}
		return s, actor
	}
	approve := func(s *Store, workID, actor string) error {
		ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if err != nil {
			return err
		}
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		fields, err := workflowActionObject(mustJSONValue(map[string]any{
			"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "rigor_class": "prototype_internal",
		}))
		if err != nil {
			return err
		}
		events, err := workflowApproveContractEvents(ctx, tx, workflowFixtureDefinition(t, 1).Definition, WorkflowActionExecutionRequest{WorkID: workID, Now: time.Unix(10, 0).UTC()}, actor, fields, "law-boundary-contract-"+workID, 4)
		if err != nil {
			return err
		}
		if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: events, ExpectedVersions: workVersion(workID, 4)}, newWriteScope(tx)); err != nil {
			return err
		}
		return tx.Commit()
	}
	t.Run("2.2 contract fails unknown law before persistence", func(t *testing.T) {
		s, actor := setup(t, "law-boundary-unknown")
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM law_subjects; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		err := approve(s, "law-boundary-unknown", actor)
		assertFailureKind(t, err, KindProjectionNotFound)
		var failure *Failure
		if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "no law record") || strings.Contains(failure.RecoveryAction, "rebuild") {
			t.Fatalf("approval must preserve the record diagnosis without inferring lag: %v", err)
		}
		assertTableCount(t, s, "workflow_contracts", 0)
	})
	t.Run("2.2 contract passes accepted Git law projection", func(t *testing.T) {
		s, actor := setup(t, "law-boundary-valid")
		if err := approve(s, "law-boundary-valid", actor); err != nil {
			t.Fatal(err)
		}
		var boundary int
		if err := s.DatabaseForTesting().QueryRow(`SELECT law_boundary_version FROM workflow_contracts WHERE work_id='law-boundary-valid'`).Scan(&boundary); err != nil || boundary != 1 {
			t.Fatalf("2.2 law boundary version=%d err=%v, want 1", boundary, err)
		}
	})
}

func writeLawManifest(t *testing.T, repo string, records []KnowledgeRecord) {
	t.Helper()
	manifest := lawTestManifest(records...)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(data)+"\n")
}

func lawProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var subject, relation string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(group_concat(law_id||'|'||status||'|'||content_hash,''),'') FROM law_subjects ORDER BY law_id`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(group_concat(source_law_id||'|'||kind||'|'||target_law_id||'|'||scanned_commit_oid||'|'||COALESCE(declarer_law_id,''),''),'') FROM law_relations ORDER BY source_law_id,target_law_id`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	return subject + "\n" + relation
}
