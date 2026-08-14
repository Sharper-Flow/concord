package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func lawTestRecord(id, kind, status, path string, relations ...KnowledgeRelation) KnowledgeRecord {
	return KnowledgeRecord{
		ID: id, Kind: kind, Path: path, Status: status, Date: "2026-08-11T00:00:00Z",
		Title: id, Summary: "law test record", Tags: []string{},
		Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, ComponentIDs: []string{}, TagIDs: []string{}},
		SHA256: "sha256:" + strings.Repeat("a", 64), LawRelations: relations,
	}
}

func TestKnowledgeManifestV11RelationsAndV10Compatibility(t *testing.T) {
	old := KnowledgeManifest{SchemaVersion: "1.0", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, Records: []KnowledgeRecord{lawTestRecord("old", "decision", "accepted", "docs/decisions/CD-0001-old.md")}}
	if err := validateKnowledgeManifest(old); err != nil {
		t.Fatalf("1.0 manifest rejected: %v", err)
	}
	valid := KnowledgeManifest{SchemaVersion: "1.1", SupportedKinds: []string{"decision", "spec"}, IndexedKinds: []string{"decision", "spec"}, Records: []KnowledgeRecord{
		lawTestRecord("base", "decision", "accepted", "docs/decisions/CD-0001-base.md", KnowledgeRelation{Kind: "refines", TargetID: "detail"}),
		lawTestRecord("detail", "spec", "accepted", "docs/detail.md"),
	}}
	if err := validateKnowledgeManifest(valid); err != nil {
		t.Fatalf("valid 1.1 relation rejected: %v", err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := parseKnowledgeManifest(encoded); err != nil || parsed.SchemaVersion != "1.1" || len(parsed.Records[0].LawRelations) != 1 {
		t.Fatalf("strict 1.1 parser result=%+v err=%v", parsed, err)
	}
	withRelation := old
	withRelation.SchemaVersion = "1.0"
	withRelation.Records[0].LawRelations = []KnowledgeRelation{{Kind: "refines", TargetID: "other"}}
	assertFailureKind(t, validateKnowledgeManifest(withRelation), KindInvalidNoteProof)
}

func TestKnowledgeManifestV11RelationsRejectInvalidGraphsAndSupersessionMismatch(t *testing.T) {
	base := func(relations ...KnowledgeRelation) KnowledgeManifest {
		return KnowledgeManifest{SchemaVersion: "1.1", SupportedKinds: []string{"decision", "spec"}, IndexedKinds: []string{"decision", "spec"}, Records: []KnowledgeRecord{
			lawTestRecord("a", "decision", "accepted", "docs/decisions/CD-0001-a.md", relations...),
			lawTestRecord("b", "spec", "accepted", "docs/b.md"),
		}}
	}
	for name, manifest := range map[string]KnowledgeManifest{
		"unknown target": base(KnowledgeRelation{Kind: "refines", TargetID: "missing"}),
		"self":           base(KnowledgeRelation{Kind: "refines", TargetID: "a"}),
		"unknown kind":   base(KnowledgeRelation{Kind: "binds", TargetID: "b"}),
		"reverse conflict": {
			SchemaVersion: "1.1", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, Records: []KnowledgeRecord{
				lawTestRecord("a", "decision", "accepted", "docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "b"}),
				lawTestRecord("b", "decision", "accepted", "docs/decisions/CD-0002-b.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "a"}),
			},
		},
		"cycle": {
			SchemaVersion: "1.1", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, Records: []KnowledgeRecord{
				lawTestRecord("a", "decision", "accepted", "docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "refines", TargetID: "b"}),
				lawTestRecord("b", "decision", "accepted", "docs/decisions/CD-0002-b.md", KnowledgeRelation{Kind: "refines", TargetID: "a"}),
			},
		},
		"mixed directed cycle": {
			SchemaVersion: "1.1", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, Records: []KnowledgeRecord{
				lawTestRecord("a", "decision", "accepted", "docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "refines", TargetID: "b"}),
				lawTestRecord("b", "decision", "accepted", "docs/decisions/CD-0002-b.md", KnowledgeRelation{Kind: "subordinate_to", TargetID: "a"}),
			},
		},
		"supersession mismatch": {
			SchemaVersion: "1.1", SupportedKinds: []string{"decision"}, IndexedKinds: []string{"decision"}, Records: []KnowledgeRecord{
				lawTestRecord("old", "decision", "superseded", "docs/decisions/CD-0001-old.md", KnowledgeRelation{Kind: "supersedes", TargetID: "new"}),
				lawTestRecord("new", "decision", "accepted", "docs/decisions/CD-0002-new.md"),
			},
		},
		"lesson endpoint": {
			SchemaVersion: "1.1", SupportedKinds: []string{"lesson", "decision"}, IndexedKinds: []string{"lesson", "decision"}, Records: []KnowledgeRecord{
				lawTestRecord("lesson", "lesson", "published", "docs/lesson.md", KnowledgeRelation{Kind: "refines", TargetID: "a"}),
				lawTestRecord("a", "decision", "accepted", "docs/decisions/CD-0001-a.md"),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateKnowledgeManifest(manifest); err == nil {
				t.Fatal("invalid law relation accepted")
			}
		})
	}
}

func TestRebuildKnowledgeIndexProjectsAndRollsBackLawRelations(t *testing.T) {
	repo := initKnowledgeRepo(t)
	records := []KnowledgeRecord{
		lawTestRecord("law-a", "decision", "accepted", "docs/decisions/CD-0001-a.md", KnowledgeRelation{Kind: "conflicts_with", TargetID: "law-b"}),
		lawTestRecord("law-b", "spec", "accepted", "docs/law-b.md"),
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
	authorizeKnowledgeLocator(t, s, home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	var source, target, scanned string
	if err := s.DB().QueryRow(`SELECT source_law_id,target_law_id,scanned_commit_oid FROM law_relations`).Scan(&source, &target, &scanned); err != nil {
		t.Fatal(err)
	}
	if source != "law-a" || target != "law-b" || scanned != commit {
		t.Fatalf("projected conflict = %s -> %s at %s", source, target, scanned)
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

func TestMandatedLawBoundaryChecksUnknownConflictAndAmendmentSubset(t *testing.T) {
	s := openTemp(t)
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','a','decision','accepted','docs/decisions/CD-0001-a.md','A','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit'),('p','l','b','spec','accepted','docs/b.md','B','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit'); INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l','a','conflicts_with','b','commit'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"missing"}, nil, true); err == nil {
		t.Fatal("unknown mandated law accepted")
	}
	if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a", "b"}, nil, true); err == nil {
		t.Fatal("unresolved conflict accepted without amendment endpoint")
	}
	if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a", "b"}, []string{"a"}, true); err != nil {
		t.Fatalf("explicit amendment path rejected: %v", err)
	}
	if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a", "b"}, []string{"a"}, false); err == nil {
		t.Fatal("completion-style check accepted unresolved conflict")
	}
	if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", []string{"a"}, []string{"b"}, true); err == nil {
		t.Fatal("undeclared law modification accepted")
	}
}

func TestLawConflictQueriesFailClosedOnOverflow(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 32)
	for i := range ids {
		ids[i] = "law-" + string(rune('a'+i))
		if _, err := s.DB().Exec(`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l',?,'decision','accepted',?,'law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','commit')`, ids[i], "docs/decisions/CD-0001-"+ids[i]+".md"); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for i := range ids {
		for j := i + 1; j < len(ids) && count < 33; j++ {
			if _, err := s.DB().Exec(`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l',?,'conflicts_with',?,'commit')`, ids[i], ids[j]); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	if _, err := s.DB().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryLawConflictsAtHome(ctx, "p", "l", ids); err == nil {
		t.Fatal("conflict query silently truncated an overflow")
	} else {
		assertFailureKind(t, err, KindInvalidPayload)
	}
	if err := s.CheckMandatedLawsAtHome(ctx, "p", "l", ids, ids, true); err == nil {
		t.Fatal("amendment check silently accepted an overflow")
	} else {
		assertFailureKind(t, err, KindInvalidPayload)
	}
}

func TestLawBoundaryVersionPreservesLegacyContractsAndGatesV22(t *testing.T) {
	setup := func(t *testing.T, workID string) (*Store, string) {
		t.Helper()
		s := openTemp(t)
		seedWork(t, s, workID)
		actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/law-boundary")
		digest := legacyImplementationDigest(t)
		events := []Event{
			workflowEvent("law-boundary-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/law-boundary", "actor_class": "agent"}),
			workflowEvent("law-boundary-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
		}
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
			t.Fatal(err)
		}
		return s, actor
	}
	contract := func(workID, actor string, boundary int) Event {
		payload := map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "rigor_class": "prototype/internal", "consequence_class": "internal_sqlite"}
		if boundary != 0 {
			payload["law_boundary_version"] = boundary
		}
		return workflowEventWithActor("law-boundary-contract-"+workID, WorkflowContractApproved, workID, actor, payload)
	}
	t.Run("legacy 2.1 contract replays without law projection", func(t *testing.T) {
		s, actor := setup(t, "law-boundary-legacy")
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{contract("law-boundary-legacy", actor, 0)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "law-boundary-legacy"): 4}}); err != nil {
			t.Fatal(err)
		}
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatalf("legacy contract did not replay: %v", err)
		}
		var boundary int
		if err := s.DB().QueryRow(`SELECT law_boundary_version FROM workflow_contracts WHERE work_id='law-boundary-legacy'`).Scan(&boundary); err != nil || boundary != 0 {
			t.Fatalf("legacy law boundary version=%d err=%v, want 0", boundary, err)
		}
	})
	t.Run("2.2 contract fails unknown law before persistence", func(t *testing.T) {
		s, actor := setup(t, "law-boundary-unknown")
		seedWorkflowLaw(t, s)
		if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM law_subjects; DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
		err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{contract("law-boundary-unknown", actor, 1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "law-boundary-unknown"): 4}})
		assertFailureKind(t, err, KindProjectionNotFound)
		assertTableCount(t, s, "workflow_contracts", 0)
	})
	t.Run("2.2 contract passes accepted Git law projection", func(t *testing.T) {
		s, actor := setup(t, "law-boundary-valid")
		seedWorkflowLaw(t, s)
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{contract("law-boundary-valid", actor, 1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "law-boundary-valid"): 4}}); err != nil {
			t.Fatal(err)
		}
		var boundary int
		if err := s.DB().QueryRow(`SELECT law_boundary_version FROM workflow_contracts WHERE work_id='law-boundary-valid'`).Scan(&boundary); err != nil || boundary != 1 {
			t.Fatalf("2.2 law boundary version=%d err=%v, want 1", boundary, err)
		}
	})
}

func writeLawManifest(t *testing.T, repo string, records []KnowledgeRecord) {
	t.Helper()
	manifest := KnowledgeManifest{SchemaVersion: "1.1", SupportedKinds: []string{"decision", "spec"}, IndexedKinds: []string{"decision", "spec"}, Records: records}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(data)+"\n")
}

func lawProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var subject, relation string
	if err := s.DB().QueryRow(`SELECT COALESCE(group_concat(law_id||'|'||status||'|'||content_hash,''),'') FROM law_subjects ORDER BY law_id`).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COALESCE(group_concat(source_law_id||'|'||kind||'|'||target_law_id||'|'||scanned_commit_oid,''),'') FROM law_relations ORDER BY source_law_id,target_law_id`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	return subject + "\n" + relation
}
