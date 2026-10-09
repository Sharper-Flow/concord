package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// oracleCapablePinForWork reports whether the work's pinned definition
// declares the acceptance-oracle member, so shared test helpers author the
// oracle exactly where the pin requires it and nowhere else.
func oracleCapablePinForWork(t *testing.T, s *Store, workID string) bool {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatalf("read pinned definition for %s: %v", workID, err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s@%d is not registered", ref, version)
	}
	return workflowOwnerOracleActive(registered.Definition)
}

// acceptanceOracleFieldsForTest authors one valid, authority-joined oracle
// from the work's live fixture state: the active contract's first approved
// predicate (seeded when the fixture approved none), the primary Project,
// the contract's first affected Domain or the Product's root Domain (with a
// registry seeded when the fixture registered none), and exact preparation
// ledger fixtures. Shared job-recording
// helpers call this only on oracle-capable pins.
// acceptanceOracleFieldsForTest authors the fixture oracle fields and seeds
// their trusted native preparations against the work's qualified verify
// subject, so a ready job records under the full admission contract.
func acceptanceOracleFieldsForTest(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	fields := acceptanceOracleFieldsWithoutPreparationsForTest(t, s, workID)
	seedOraclePreparationFieldsFixture(t, s, workID, fields)
	return fields
}

// acceptanceOracleFieldsWithoutPreparationsForTest authors the fixture oracle
// fields only. Routes that refuse the oracle member, or record jobs whose
// readiness is never validated, use this variant and seed no preparation.
func acceptanceOracleFieldsWithoutPreparationsForTest(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&contractVersion); err != nil {
		t.Fatalf("read active contract for %s: %v", workID, err)
	}
	predicate := "predicate:oracle-fixture"
	var seeded string
	err := db.QueryRow(`SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal,predicate_id LIMIT 1`, workID, contractVersion).Scan(&seeded)
	switch err {
	case nil:
		predicate = seeded
	case sql.ErrNoRows:
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatalf("open fold guard for %s: %v", workID, err)
		}
		if _, err := db.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,0,'check','{"kind":"check","check_ref":"check:oracle-fixture","immutable_subject_ref":"commit:oracle-fixture","expected_result":"pass"}')`, workID, contractVersion, predicate); err != nil {
			t.Fatalf("seed one approved predicate for %s: %v", workID, err)
		}
		if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatalf("close fold guard for %s: %v", workID, err)
		}
	default:
		t.Fatalf("read contract predicates for %s: %v", workID, err)
	}
	var project string
	if err := db.QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatalf("read primary Project for %s: %v", workID, err)
	}
	var product string
	if err := db.QueryRow(`SELECT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND wp.role='primary' LIMIT 1`, workID).Scan(&product); err != nil {
		t.Fatalf("resolve Product for %s: %v", workID, err)
	}
	registryHash := "sha256:" + strings.Repeat("b", 64)
	var rootDomain string
	if err := db.QueryRow(`SELECT root_domain_id,content_hash FROM domain_registries WHERE product_id=?`, product).Scan(&rootDomain, &registryHash); err == sql.ErrNoRows {
		rootDomain = "root"
		registryHash = "sha256:" + strings.Repeat("b", 64)
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatalf("open fold guard for %s: %v", workID, err)
		}
		if _, err := db.Exec(`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,'oracle-fixture-locator',?,'root','1.0',?,'test')`, product, project, product+"-key", registryHash); err != nil {
			t.Fatalf("seed registry for %s: %v", workID, err)
		}
		if _, err := db.Exec(`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES(?,'oracle-fixture-locator',?,'root','Root','oracle fixture','current',?,'test')`, project, product, registryHash); err != nil {
			t.Fatalf("seed root Domain for %s: %v", workID, err)
		}
		if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatalf("close fold guard for %s: %v", workID, err)
		}
	} else if err != nil {
		t.Fatalf("read Domain registry for %s: %v", product, err)
	}
	domain := rootDomain
	var binding int
	if err := db.QueryRow(`SELECT 1 FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&binding); err == nil {
		var affected string
		if err := db.QueryRow(`SELECT domain_id FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? ORDER BY domain_id LIMIT 1`, workID, contractVersion).Scan(&affected); err == nil {
			domain = affected
		} else if err != sql.ErrNoRows {
			t.Fatalf("read affected Domains for %s: %v", workID, err)
		}
	} else if err != sql.ErrNoRows {
		t.Fatalf("read architecture binding for %s: %v", workID, err)
	}
	fields := map[string]any{
		"owners": []map[string]any{{
			"owner_id": "owner:fixture", "domain_id": domain,
			"mechanism":     map[string]any{"project_id": project, "path": "internal/store/worker_jobs.go", "entry_point": "foldWorkerJobRecorded"},
			"obligation":    "the fixture oracle obligates the recorded revision",
			"predicate_ids": []string{predicate},
			"law_bindings":  []map[string]any{},
		}},
		"cases": []map[string]any{{
			"case_id": "case:fixture", "owner_id": "owner:fixture", "entry_point": "record",
			"input_class": "fixture recording", "expected_state": "revision recorded",
			"control_ids": []string{"control:fixture"},
		}},
		"controls": []map[string]any{{
			"control_id": "control:fixture", "owner_id": "owner:fixture",
			"predicate_ids": []string{predicate}, "case_ids": []string{"case:fixture"},
			"recipe_source": map[string]any{"kind": "repository_file", "project_id": project, "path": "scripts/oracle_check.sh", "commit_oid": testOracleCommit()},
			"argv":          oracleGoArgv("fixture"), "cwd": "internal/store",
			"expected_result": "pass", "required_evidence_role": "reported",
			"readiness_evidence_refs": []string{},
		}},
	}
	return fields
}

// oracleAuthorityFixture seeds one pinned law revision and one more current
// Domain so the authority refusals can name a real mismatch rather than a
// missing table row.
func oracleAuthorityFixture(t *testing.T, s *Store, workID string) {
	t.Helper()
	lawHash := "sha256:" + strings.Repeat("a", 64)
	registryHash := "sha256:" + strings.Repeat("b", 64)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,'spec:oracle',?);
		DELETE FROM fold_guard`, workID, lawHash); err != nil {
		t.Fatalf("seed pinned law revision: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','edge','Edge','outside the binding','current',?,'test');
		DELETE FROM fold_guard`, registryHash); err != nil {
		t.Fatalf("seed outside-scope Domain: %v", err)
	}
}

// A valid oracle records under the live authority joins, and every
// unjoined authority refuses without recording a revision.
func TestOwnerOracleAuthorAuthorityJoins(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-authority"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	oracleAuthorityFixture(t, s, workID)
	bootstrapOracleFixtureSubject(t, s, workID)
	pinnedLawHash := "sha256:" + strings.Repeat("a", 64)
	otherLawHash := "sha256:" + strings.Repeat("c", 64)
	baseFields := func() map[string]any {
		fields := map[string]any{
			"job_id": "job:authority", "objective": "carry the oracle authority obligation",
			"stopping_condition": "the oracle controls pass with authority joined",
			"ready":              true, "readiness_evidence": []string{"evidence:return-route-verification"},
			"acceptance_oracle": map[string]any{
				"owners": []map[string]any{{
					"owner_id": "owner:authority", "domain_id": "root",
					"mechanism":     map[string]any{"project_id": "project", "path": "internal/store/worker_jobs.go", "entry_point": "foldWorkerJobRecorded"},
					"obligation":    "the authority join binds parent approval",
					"predicate_ids": []string{"predicate:return-route"},
					"law_bindings":  []map[string]any{{"source": map[string]any{"kind": "knowledge", "source_id": "source:oracle", "law_id": "spec:oracle", "content_hash": pinnedLawHash}, "clause": "D1"}},
				}},
				"cases": []map[string]any{{
					"case_id": "case:authority", "owner_id": "owner:authority", "entry_point": "record",
					"input_class": "authority recording", "expected_state": "revision recorded",
					"control_ids": []string{"control:authority"},
				}},
				"controls": []map[string]any{{
					"control_id": "control:authority", "owner_id": "owner:authority",
					"predicate_ids": []string{"predicate:return-route"}, "case_ids": []string{"case:authority"},
					"recipe_source": map[string]any{"kind": "repository_file", "project_id": "project", "path": "scripts/oracle_check.sh", "commit_oid": testOracleCommit()},
					"argv":          oracleGoArgv("authority"), "cwd": "internal/store",
					"expected_result": "pass", "required_evidence_role": "reported",
					"readiness_evidence_refs": []string{"evidence:return-route-verification"},
				}},
			},
		}
		seedOraclePreparationFieldsFixture(t, s, workID, fields["acceptance_oracle"].(map[string]any))
		return fields
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, baseFields()); err != nil {
		t.Fatalf("fully joined oracle refused: %v", err)
	}
	revisionCount := func() int {
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_job_revisions WHERE work_id=?`, workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for _, refusal := range []struct {
		name     string
		mutation func(fields map[string]any)
		wantText string
	}{
		{"unapproved predicate", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			oracle["owners"].([]map[string]any)[0]["predicate_ids"] = []string{"predicate:unapproved"}
			oracle["controls"].([]map[string]any)[0]["predicate_ids"] = []string{"predicate:unapproved"}
		}, "is not approved by the active parent contract"},
		{"unknown domain", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			oracle["owners"].([]map[string]any)[0]["domain_id"] = "unknown-domain"
		}, "unknown Domain"},
		{"domain outside affected scope", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			oracle["owners"].([]map[string]any)[0]["domain_id"] = "edge"
		}, "outside the approved affected Domain scope"},
		{"law hash mismatch", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			binding := oracle["owners"].([]map[string]any)[0]["law_bindings"].([]map[string]any)[0]
			binding["source"].(map[string]any)["content_hash"] = otherLawHash
		}, "did not pin"},
		{"unpinned law", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			binding := oracle["owners"].([]map[string]any)[0]["law_bindings"].([]map[string]any)[0]
			binding["source"].(map[string]any)["law_id"] = "spec:never-pinned"
		}, "did not pin"},
		{"nonmember project", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			oracle["owners"].([]map[string]any)[0]["mechanism"].(map[string]any)["project_id"] = "project-other"
		}, "not a member Project"},
		{"naked readiness evidence", func(fields map[string]any) {
			oracle := fields["acceptance_oracle"].(map[string]any)
			oracle["controls"].([]map[string]any)[0]["readiness_evidence_refs"] = []string{"evidence:never-bound-anywhere"}
		}, "not its exact qualified native preparation"},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			fields := baseFields()
			fields["job_id"] = "job:authority-" + strings.ReplaceAll(refusal.name, " ", "-")
			refusal.mutation(fields)
			err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, fields)
			if err == nil || !strings.Contains(err.Error(), refusal.wantText) {
				t.Fatalf("authority refusal %q error = %v, want text %q", refusal.name, err, refusal.wantText)
			}
			if got := revisionCount(); got != 1 {
				t.Fatalf("refused author %q recorded %d revisions; a refusal must leave no job mutation", refusal.name, got)
			}
		})
	}
}

// A pre-oracle pin keeps its oracle-free authoring route and refuses the
// oracle member: released definitions retain both behaviors under their
// frozen digests.
func TestOwnerOracleLegacyPinStaysOracleFree(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-legacy-pin"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 21, "repair")
	s := fixture.store
	defer s.Close()
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": "job:legacy", "objective": "legacy job without an oracle",
		"stopping_condition": "the legacy checks pass", "ready": false,
	}); err != nil {
		t.Fatalf("legacy pin refused an oracle-free job: %v", err)
	}
	err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": "job:legacy-oracle", "objective": "legacy job with an oracle",
		"stopping_condition": "the legacy checks pass", "ready": false,
		"acceptance_oracle": acceptanceOracleFieldsWithoutPreparationsForTest(t, s, workID),
	})
	if err == nil || !strings.Contains(err.Error(), "acceptance_oracle\" is not declared for action") {
		t.Fatalf("legacy pin oracle error = %v, want the pinned-definition refusal", err)
	}
}

// The oracle member is omitempty in the digest content: an oracle-free
// payload serializes to exactly the bytes the pre-oracle derivation
// produced, so every historical digest still verifies.
func TestOwnerOracleLegacyDigestUnchanged(t *testing.T) {
	t.Parallel()
	readiness := &WorkerJobReadiness{Ready: true, Evidence: []string{"evidence:coordinator-ready"}}
	payload := WorkerJobRecordedPayload{
		JobID: "job:digest-legacy", Revision: 1, ContractVersion: 1,
		Objective: "objective", StoppingCondition: "stopping", ProjectScope: "project",
		PathScope: []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{"go test ./internal/store/"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{}, ReservedIntegration: "reserved",
		Readiness: readiness,
	}
	legacy := struct {
		JobID               string                  `json:"job_id"`
		Revision            int64                   `json:"revision"`
		ContractVersion     int64                   `json:"contract_version"`
		Objective           string                  `json:"objective"`
		StoppingCondition   string                  `json:"stopping_condition"`
		ProjectScope        string                  `json:"project_scope,omitempty"`
		PathScope           []string                `json:"path_scope"`
		PredicateIDs        []string                `json:"predicate_ids"`
		Checks              []string                `json:"checks"`
		Prerequisites       []WorkerJobPrerequisite `json:"prerequisites"`
		UnresolvedRefs      []string                `json:"unresolved_refs"`
		ReservedIntegration string                  `json:"reserved_integration,omitempty"`
		Readiness           *WorkerJobReadiness     `json:"readiness,omitempty"`
	}{payload.JobID, payload.Revision, payload.ContractVersion, payload.Objective, payload.StoppingCondition, payload.ProjectScope, payload.PathScope, payload.PredicateIDs, payload.Checks, payload.Prerequisites, payload.UnresolvedRefs, payload.ReservedIntegration, payload.Readiness}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := DeriveWorkerJobDigest(payload); got != want {
		t.Fatalf("oracle-free digest = %s, want the legacy derivation %s", got, want)
	}
	// Carrying an oracle changes the digest: the oracle is immutable job
	// content, and a revision's oracle can never be swapped under one
	// digest.
	payload.AcceptanceOracle = testOracleGraph()
	payload.AcceptanceOracle.Owners[0].Mechanism.ProjectID = "project"
	if swapped := DeriveWorkerJobDigest(payload); swapped == want {
		t.Fatal("an oracle-carrying payload kept the oracle-free digest")
	}
}

// The recorded oracle projects onto the view and the packet, the reader
// returns it under the exact binding, and a tampered packet oracle refuses
// at the dispatch-time job binding.
func TestOwnerOracleImmutableProjection(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-projection"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	bootstrapOracleFixtureSubject(t, s, workID)
	oracle := acceptanceOracleFieldsForTest(t, s, workID)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": "job:projection", "objective": "project the oracle to every lane",
		"stopping_condition": "the packet binds the recorded oracle",
		"checks":             []string{"go test ./internal/store/"},
		"ready":              true, "readiness_evidence": []string{"evidence:return-route-verification"},
		"acceptance_oracle": oracle,
	}); err != nil {
		t.Fatalf("record oracle-bearing job: %v", err)
	}
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil || len(views) != 1 {
		t.Fatalf("recorded views = %#v, error = %v", err, views)
	}
	if views[0].AcceptanceOracle == nil || len(views[0].AcceptanceOracle.Owners) != 1 || views[0].AcceptanceOracle.Owners[0].OwnerID != "owner:fixture" {
		t.Fatalf("view oracle = %#v, want the recorded one-oracle graph", views[0].AcceptanceOracle)
	}
	read, err := readWorkerJobOracle(context.Background(), s.DatabaseForTesting(), workID, views[0].Binding)
	if err != nil || read == nil {
		t.Fatalf("event-reader oracle = %#v, error = %v", read, err)
	}
	readJSON, readErr := json.Marshal(read)
	viewJSON, viewErr := json.Marshal(views[0].AcceptanceOracle)
	if readErr != nil || viewErr != nil || string(readJSON) != string(viewJSON) {
		t.Fatalf("event-reader oracle = %s, want the view oracle %s", readJSON, viewJSON)
	}
	if _, err := readWorkerJobOracle(context.Background(), s.DatabaseForTesting(), workID, WorkerJobBinding{JobID: views[0].Binding.JobID, Revision: 1, Digest: "sha256:" + strings.Repeat("e", 64)}); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("mismatched-digest oracle read error = %v, want the digest refusal", err)
	}
	if _, err := readWorkerJobOracle(context.Background(), s.DatabaseForTesting(), workID, WorkerJobBinding{JobID: "job:unrecorded", Revision: 1, Digest: views[0].Binding.Digest}); err == nil || !strings.Contains(err.Error(), "names no recorded revision") {
		t.Fatalf("unrecorded oracle read error = %v, want the missing-revision refusal", err)
	}
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	lane := reviewGateLane(t, "implementation")
	attemptID := "attempt:" + workID
	packet := joinPacketFor(t, s, workID, "repair", attemptID, lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = packetJobFromView(views[0])
	if _, err := validateWorkerPacketJob(context.Background(), s.DatabaseForTesting(), definition, workID, lane, mustJSONValue(packet)); err != nil {
		t.Fatalf("recorded-oracle packet refused: %v", err)
	}
	claimed := packetJobFromView(views[0])
	claimed.AcceptanceOracle.Controls[0].Argv = oracleGoArgv("tampered")
	packet["inputs"].(map[string]any)["worker_job"] = claimed
	if _, err := validateWorkerPacketJob(context.Background(), s.DatabaseForTesting(), definition, workID, lane, mustJSONValue(packet)); err == nil || !strings.Contains(err.Error(), "content does not match the recorded revision") {
		t.Fatalf("tampered-oracle packet error = %v, want the content-mismatch refusal", err)
	}
}

// A recorded revision is immutable through its oracle: a re-recording with
// changed oracle content under a recomputed digest refuses, a byte-identical
// re-recording folds, and the authority joins never re-run on replay — the
// retained historical input decides even after the Domain registry moved on.
func TestOwnerOracleImmutableRevisionAndReplay(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-immutable"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	bootstrapOracleFixtureSubject(t, s, workID)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": "job:immutable", "objective": "keep the oracle immutable",
		"stopping_condition": "the recorded oracle never changes",
		"ready":              false,
		"acceptance_oracle":  acceptanceOracleFieldsForTest(t, s, workID),
	}); err != nil {
		t.Fatalf("record oracle-bearing job: %v", err)
	}
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil || len(views) != 1 {
		t.Fatalf("recorded views = %#v, error = %v", views, err)
	}
	var raw []byte
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerJobRecorded).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original WorkerJobRecordedPayload
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	// Changed oracle content with an honestly recomputed digest still
	// refuses: a revision never changes content in place.
	var tampered WorkerJobRecordedPayload
	if err := json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.AcceptanceOracle.Controls[0].Argv = oracleGoArgv("changed")
	tampered.Digest = DeriveWorkerJobDigest(tampered)
	if err := applyWorkerJobEventForTest(t, s, workID, tampered); err == nil || !strings.Contains(err.Error(), "cannot change content in place") {
		t.Fatalf("tampered re-record error = %v, want the immutability refusal", err)
	}
	// Replay is shape-only over the retained input: after the joined Domain
	// goes non-current, the byte-identical event still folds — an author
	// re-recording the same oracle under the retired registry would refuse,
	// but the retained historical input never consults it.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE domains SET status='deprecated' WHERE product_id='product' AND domain_id='root'; DELETE FROM fold_guard`); err != nil {
		t.Fatalf("deprecate the fixture Domain: %v", err)
	}
	if err := applyWorkerJobEventForTest(t, s, workID, original); err != nil {
		t.Fatalf("byte-identical re-record after registry retirement refused: %v", err)
	}
}

// The recorded oracle is one digest-covered member: an event claiming the
// oracle-free digest while carrying oracle content refuses at the register
// boundary, so no fabricated pairing of digest and oracle lands.
func TestOwnerOracleDigestCoversOracleContent(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-digest"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	bootstrapOracleFixtureSubject(t, s, workID)
	oracle := acceptanceOracleFieldsForTest(t, s, workID)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": "job:digest-cover", "objective": "the digest covers the oracle",
		"stopping_condition": "the derived digest matches oracle content",
		"ready":              false,
		"acceptance_oracle":  oracle,
	}); err != nil {
		t.Fatalf("record oracle-bearing job: %v", err)
	}
	var raw []byte
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerJobRecorded).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload WorkerJobRecordedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AcceptanceOracle == nil {
		t.Fatal("recorded event carries no oracle")
	}
	if derived := DeriveWorkerJobDigest(payload); derived != payload.Digest {
		t.Fatalf("recorded digest %s does not cover the oracle content (derived %s)", payload.Digest, derived)
	}
	payload.AcceptanceOracle.Cases[0].ExpectedState = "fabricated state"
	if err := applyWorkerJobEventForTest(t, s, workID, payload); err == nil || !strings.Contains(err.Error(), "digest does not match the derived content digest") {
		t.Fatalf("swapped-oracle digest error = %v, want the derived-digest refusal", err)
	}
}

// oracleRecordHelperWitness pins the shared-helper behavior this slice
// depends on: on an oracle-capable pin the helper authors a valid oracle,
// and the recorded revision carries it end to end.
func TestOwnerOracleSharedHelperRecordsOracle(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-shared-helper"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	if !oracleCapablePinForWork(t, s, workID) {
		t.Fatal("the newest break-fix pin is not oracle-capable")
	}
	bootstrapOracleFixtureSubject(t, s, workID)
	job := &WorkerJobBinding{JobID: "job:helper", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil || len(views) != 1 {
		t.Fatalf("helper-recorded views = %#v, error = %v", views, err)
	}
	if views[0].AcceptanceOracle == nil {
		t.Fatal("shared helper recorded no oracle on an oracle-capable pin")
	}
	if !views[0].Ready {
		t.Fatalf("helper-recorded revision is not dispatch-ready: %#v", views[0])
	}
}
