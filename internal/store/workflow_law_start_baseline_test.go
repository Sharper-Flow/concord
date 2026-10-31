package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/workflowproof"
)

// Naming a law after editing it cannot turn the edit into before-state.
func TestLawChangePreNamingUsesOwningStart(t *testing.T) {
	const workID = "law-before-naming"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	lawChangeConstructorItem(t, s, workID, false)
	editLawChangeLaw(t, s, "law-agent")
	change, err := deriveLawChange(t, s, workID, lawChangeV1Generic, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(change.LawBefore, func(s workflowLawState) bool { return s.LawID == "law-agent" })
	if i < 0 || change.LawBefore[i].ContentHash != lawChangeHashA {
		t.Fatalf("pre-naming edit became original state: %+v", change.LawBefore)
	}
}

// A non-Product-changing definition is not an exemption from registry scope.
func TestLawChangeGenericRegistryEditCannotComplete(t *testing.T) {
	const workID = "law-generic-registry"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	lawChangeConstructorItem(t, s, workID, false)
	change, err := deriveLawChange(t, s, workID, lawChangeV1Generic, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lawChangeFold(t, s, `UPDATE domains SET purpose='edited without a binding' WHERE domain_id='workflow-engine'`)
	requireFailureKind(t, enactLawChange(t, s, workID, change, nil), KindOutcomeMismatch, "generic registry edit")
}

func TestLawChangeUnnamedNewAndRemovedLawsRefuse(t *testing.T) {
	for _, mode := range []string{"edited", "added", "removed"} {
		t.Run(mode, func(t *testing.T) {
			const id = "law-all-source-inventory"
			s := seedLawChangeFixture(t, id)
			seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
			startLawChange(t, s, id)
			switch mode {
			case "edited":
				editLawChangeLaw(t, s, "law-agent")
			case "added":
				seedLawChangeLaw(t, s, "law-new", "derived", "agent-surface")
			case "removed":
				lawChangeFold(t, s, `DELETE FROM law_domain_homes WHERE law_id='law-agent'`)
				lawChangeFold(t, s, `DELETE FROM law_subjects WHERE law_id='law-agent'`)
			}
			requireFailureKind(t, enactLawChange(t, s, id, nil, nil), KindOutcomeMismatch, "law-free contract changed a source law")
			change, err := deriveLawChange(t, s, id, lawChangeV1, nil, nil, nil)
			if err != nil || change != nil {
				t.Fatalf("recovery must not adopt an undeclared delta: change=%+v err=%v", change, err)
			}
			requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindOutcomeMismatch, "recovery does not exclude an unattributed delta from completion")
		})
	}
}

func TestLawChangeStartAbsenceAndSourceGraph(t *testing.T) {
	t.Run("no home stays explicitly absent", func(t *testing.T) {
		s := openTemp(t)
		seedWork(t, s, "no-home")
		startLawChange(t, s, "no-home")
		if err := enactLawChange(t, s, "no-home", nil, nil); err != nil {
			t.Fatal(err)
		}
		seedWorkflowLaw(t, s)
		requireFailureKind(t, enactLawChange(t, s, "no-home", nil, nil), KindMissingEvidence, "new home has no original baseline")
	})
	t.Run("no registry does not stop lawless work", func(t *testing.T) {
		s := openTemp(t)
		seedWork(t, s, "no-registry")
		seedWorkflowLaw(t, s)
		startLawChange(t, s, "no-registry")
		if err := enactLawChange(t, s, "no-registry", nil, nil); err != nil {
			t.Fatal(err)
		}
		requireFailureKind(t, enactLawChange(t, s, "no-registry", nil, nil), KindOutcomeMismatch, "new registry is not original state")
	})
	t.Run("in-flight item has no invented start", func(t *testing.T) {
		s := seedLawChangeFixture(t, "old-item")
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		_, err := deriveLawChange(t, s, "old-item", lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"}), []string{"law-agent"}, nil)
		requireFailureKind(t, err, KindMissingEvidence, "missing start baseline")
		requireFailureKind(t, enactLawChange(t, s, "old-item", nil, nil), KindMissingEvidence, "completion cannot infer historic start")
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_law_baselines`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invented baseline: %d %v", count, err)
		}
	})
}

func TestLawChangeRegistryRootAndRelationIdentity(t *testing.T) {
	before := &workflowLawRegistrySnapshot{RootDomainID: "root", Entries: []workflowLawRegistryPin{}}
	after := &workflowLawRegistrySnapshot{RootDomainID: "new-root", Entries: []workflowLawRegistryPin{}}
	binding := &WorkflowArchitectureBinding{DomainModifies: []string{"root"}}
	requireFailureKind(t, checkWorkflowLawRegistryScope(before, after, binding), KindOutcomeMismatch, "new root not declared")
	binding.DomainModifies = append(binding.DomainModifies, "new-root")
	if err := checkWorkflowLawRegistryScope(before, after, binding); err != nil {
		t.Fatal(err)
	}
	after.RootDomainID = "root"
	after.Entries = []workflowLawRegistryPin{{DomainID: "a", Kind: "depends_on", TargetDomainID: "b", ContentHash: lawChangeHashA}}
	binding.DomainModifies = []string{"a", "b"}
	requireFailureKind(t, checkWorkflowLawRegistryScope(before, after, binding), KindOutcomeMismatch, "endpoints are not a relation declaration")
	binding.DomainRelationModifies = []WorkflowDomainRelationModification{{SourceDomainID: "a", Kind: "depends_on", TargetDomainID: "b"}}
	if err := checkWorkflowLawRegistryScope(before, after, binding); err != nil {
		t.Fatal(err)
	}
}

func TestLawChangeBaselineMigrationPreservesHistoricalFacts(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "law-start.db"), migrationPrefixBefore(t, "workflow_owning_start_law_baseline"))
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('historic-law','task','Historic law item','in_progress',0,1,'t','t');`)
	actor := DeriveWorkflowActorRef("principal/test", "client/test", "agent/test", "session/test")
	execSixClassSQL(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/test','client/test','agent/test','session/test','agent','t')`, actor)
	execSixClassSQL(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_change) VALUES('historic-law',1,'Historical law contract','internal_sqlite','[]','[]','t',?,'[]','{ "reason": "retained", "serious": true, "crucial": true, "law_ids": ["law-old"], "law_before": [], "registry_before": null }')`, actor)
	execSixClassSQL(t, db, `DELETE FROM write_guard;`)
	before := sixClassSnapshot(t, db, "work_items", "workflow_contracts")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := sixClassSnapshot(t, db, "work_items", "workflow_contracts"); !reflect.DeepEqual(got, before) {
		t.Fatalf("migration changed facts: %v", got)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workflow_law_baselines`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration invented start snapshots: %d %v", count, err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_law_baselines(work_id,baseline_json) VALUES('historic-law','{}')`); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
		t.Fatalf("baseline insert guard: %v", err)
	}
	execSixClassSQL(t, db, `INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_law_baselines(work_id,baseline_json) VALUES('historic-law','{}');`)
	if _, err := db.Exec(`UPDATE workflow_law_baselines SET baseline_json='{"laws":[]}'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("baseline is mutable: %v", err)
	}
	execSixClassSQL(t, db, `DELETE FROM write_guard;`)
	var historical WorkflowLawChange
	if err := json.Unmarshal([]byte(`{"reason":"retained","serious":true,"crucial":true,"predecessor_serious":true,"law_ids":["law-old"],"law_before":[],"registry_before":null}`), &historical); err != nil {
		t.Fatal(err)
	}
	if historical.Version != 0 || historical.RegistryBefore != nil {
		t.Fatalf("historical decoding fabricated proof: %+v", historical)
	}
}

// The verified snapshot certifies one commit: the baseline inventory is the
// snapshot's own inventory, so an enacted-folder delta can only appear as a
// moved head, and the boundary refuses the stale proof instead of comparing
// projections (the projection-drift simulations retired with the index).
func TestLawChangeBaselineCoversVerifiedHome(t *testing.T) {
	s, home := seedKnowledgeHomeProduct(t, "law-start-product", KnowledgeHome{HomeProjectID: "law-start-home", HomeLocatorID: "law-start-loc", HeadRef: "HEAD"}, "SRC-LAW", ".concord/docs/decisions/CD-0901-home.md", "Home law")
	const id = "law-start-source-work"
	seedKnowledgeWork(t, s, id, "Source inventory work")
	lawChangeFold(t, s, `UPDATE work_projects SET project_id=? WHERE work_id=? AND role='primary'`, home.HomeProjectID, id)
	startLawChange(t, s, id)
	verified, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	tx := lawChangeTx(t, s)
	err = checkWorkflowLawChangeEnactmentTx(t.Context(), tx, id, nil, nil)
	_ = tx.Rollback()
	requireFailureKind(t, err, KindInvalidOperation, "unverified all-source law inventory")
	tx = lawChangeTx(t, s)
	err = checkWorkflowLawChangeEnactmentTx(verified, tx, id, nil, nil)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("verified snapshot refused a law-free contract: %v", err)
	}
	// PM1 §3.3: a commit that moves the head without touching the committed
	// law content is part of the verified state and admits the enactment.
	writeKnowledgeFile(t, home.RepoPath, "unrelated.go", "package synthetic\n")
	commitKnowledgeRepo(t, home.RepoPath, "move past the verified home without touching law")
	tx = lawChangeTx(t, s)
	err = checkWorkflowLawChangeEnactmentTx(verified, tx, id, nil, nil)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("unrelated head movement refused the enactment check: %v", err)
	}
	// A commit that moves the law content itself invalidates the snapshot.
	lawPath := ".concord/law/decisions/SRC-LAW-fixture.md"
	original := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+lawPath)
	writeKnowledgeFile(t, home.RepoPath, lawPath, strings.Replace(original, "Fixture folder law", "Amended fixture law", 1))
	commitKnowledgeRepo(t, home.RepoPath, "amend the committed law")
	tx = lawChangeTx(t, s)
	err = checkWorkflowLawChangeEnactmentTx(verified, tx, id, nil, nil)
	_ = tx.Rollback()
	requireFailureKind(t, err, KindInvalidOperation, "moved law content cannot certify a law-free contract")
}

func TestLawChangeBaselineInitializationRollsBackWithOwner(t *testing.T) {
	const id = "law-baseline-atomic"
	s := seedLawChangeFixture(t, id)
	startLawChange(t, s, id)
	version := readWorkVersion(t, s, id)
	registered, err := LiveWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: id, Definition: registered, Actor: lawChangeConstructorActor})
	})
	requireFailureKind(t, err, KindProjectionConflict, "duplicate owning-start baseline")
	if !strings.Contains(err.Error(), "cannot record the owning start law baseline") {
		t.Fatalf("initialization stopped before the duplicate baseline insert: %v", err)
	}
	var instances int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=?`, id).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if instances != 0 || readWorkVersion(t, s, id) != version {
		t.Fatal("failed baseline capture left partial initialization")
	}
}

func TestLawChangeNewLawRequiresAdditionScope(t *testing.T) {
	const id = "law-new-scope"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-existing", "derived", "agent-surface")
	startLawChange(t, s, id)
	seedLawChangeLaw(t, s, "law-new", "derived", "agent-surface")
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "add a law"})
	_, err := deriveLawChange(t, s, id, lawChangeV1Generic, fields, []string{"law-new"}, nil)
	requireFailureKind(t, err, KindOutcomeMismatch, "new law cannot masquerade as an existing derived revision")
	binding := &WorkflowArchitectureBinding{LawAdditions: []WorkflowLawAddition{{LawID: "law-new", HomeDomainID: "agent-surface"}}}
	change, err := deriveLawChange(t, s, id, lawChangeV1, fields, nil, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.LawBefore) != 1 || !change.LawBefore[0].Absent {
		t.Fatalf("new law was not absent at start: %+v", change)
	}
	if err := enactLawChange(t, s, id, change, binding); err != nil {
		t.Fatal(err)
	}
	binding.LawAdditions = append(binding.LawAdditions, WorkflowLawAddition{LawID: "law-existing", HomeDomainID: "agent-surface"})
	_, err = deriveLawChange(t, s, id, lawChangeV1, fields, nil, binding)
	requireFailureKind(t, err, KindInvalidPayload, "existing law cannot masquerade as an addition")
}

// An empty mandate must not hide a committed law edit behind the verified
// snapshot: a law-content commit between the verification and the enactment
// check invalidates the proof with or without a retained verification
// context, while an unrelated commit admits (PM1 §3.3).
func TestLawBaselineSingleSourceRejectsInvisibleStaleEdit(t *testing.T) {
	s, home := seedKnowledgeHomeProduct(t, "single-baseline-product", KnowledgeHome{HomeProjectID: "single-baseline-home", HomeLocatorID: "single-baseline-loc", HeadRef: "HEAD"}, "HOME-LAW", ".concord/docs/decisions/CD-0901-home.md", "Home law")
	const id = "single-baseline-work"
	seedKnowledgeWork(t, s, id, "Single-source baseline work")
	lawChangeFold(t, s, `UPDATE work_projects SET project_id=? WHERE work_id=? AND role='primary'`, home.HomeProjectID, id)
	verified, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	startLawChange(t, s, id)
	path := ".concord/law/decisions/HOME-LAW-fixture.md"
	original := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+path)
	writeKnowledgeFile(t, home.RepoPath, path, strings.Replace(original, "Fixture folder law", "Changed law outside the empty contract", 1))
	commitKnowledgeRepo(t, home.RepoPath, "change an unmentioned law")
	for _, withProof := range []bool{false, true} {
		ctx := t.Context()
		if withProof {
			ctx = verified
		}
		tx := lawChangeTx(t, s)
		err = checkWorkflowLawChangeEnactmentTx(ctx, tx, id, nil, nil)
		_ = tx.Rollback()
		if err == nil {
			t.Errorf("law-free completion trusted a stale single-source snapshot (withProof=%v)", withProof)
		} else {
			requireFailureKind(t, err, KindInvalidOperation, "single-source proof freshness")
			message := "was not verified before this transaction"
			if withProof {
				message = "moved its committed law content after its verification"
			}
			if !strings.Contains(err.Error(), message) {
				t.Errorf("wrong single-source refusal (withProof=%v): %v", withProof, err)
			}
		}
	}
}

// Initialization admits only an established verification whose committed law
// content still matches the transaction head: a missing proof refuses, and a
// proof whose law moved after establishment refuses; an unrelated commit
// admits (PM1 §3.3). Initialization rolls back with its owner either way.
func TestLawBaselineInitializationRequiresFreshSourceProof(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "verified"} {
		t.Run(mode, func(t *testing.T) {
			s, home := seedKnowledgeHomeProduct(t, "initialize-proof-product", KnowledgeHome{HomeProjectID: "initialize-proof-home", HomeLocatorID: "initialize-proof-loc", HeadRef: "HEAD"}, "HOME-LAW", ".concord/docs/decisions/CD-0901-home.md", "Home law")
			const id = "initialize-proof-work"
			seedKnowledgeWork(t, s, id, "Initialized law inventory")
			lawChangeFold(t, s, `UPDATE work_projects SET project_id=? WHERE work_id=? AND role='primary'`, home.HomeProjectID, id)
			ctx := context.Background()
			if mode != "missing" {
				var err error
				ctx, err = s.EstablishKnowledgeSourceSetProofForProject(ctx, home.HomeProjectID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stale" {
				path := ".concord/law/decisions/HOME-LAW-fixture.md"
				original := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+path)
				writeKnowledgeFile(t, home.RepoPath, path, strings.Replace(original, "Fixture folder law", "Amended after start proof", 1))
				commitKnowledgeRepo(t, home.RepoPath, "move the committed law after start proof")
			}
			definition, err := LiveWorkflowDefinitionForRef("workflow.implementation")
			if err != nil {
				t.Fatal(err)
			}
			err = s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: id, Definition: definition, Actor: lawChangeConstructorActor})
			})
			if mode == "verified" {
				if err != nil {
					t.Fatal(err)
				}
				var count int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id=?`, id).Scan(&count); err != nil || count != 1 {
					t.Fatalf("verified initialization omitted its baseline: count=%d error=%v", count, err)
				}
				return
			}
			requireFailureKind(t, err, KindInvalidOperation, "unverified owning start")
			message := "was not verified before this transaction"
			if mode == "stale" {
				message = "moved its committed law content after its verification"
			}
			if !strings.Contains(err.Error(), message) {
				t.Fatalf("wrong %s initialization refusal: %v", mode, err)
			}
			var count int
			if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM workflow_instances WHERE work_id=?)+(SELECT count(*) FROM workflow_law_baselines WHERE work_id=?)`, id, id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unverified initialization left effects: count=%d error=%v", count, err)
			}
		})
	}
}

// A standalone law home keeps its demand-driven preparation: the verification
// runs before the transaction and its load failure is consumed by the
// mandate; a readable folder answers.
func TestLawProofStandaloneHomeKeepsLegacyRead(t *testing.T) {
	s := seedLawChangeFixture(t, "standalone-law")
	lawChangeFold(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='product'`)
	ctx, err := s.establishKnowledgeSourceSetProofAtHome(t.Context(), "project", "workflow-law-locator")
	if err != nil {
		t.Fatal(err)
	}
	tx := lawChangeTx(t, s)
	if err := checkMandatedLawsTxAtHome(ctx, tx, "project", "workflow-law-locator", []string{"spec:one"}, nil, false); err != nil {
		t.Fatal(err)
	}
}

// An older current pin remains readable. The existing outside-repair route
// can reconcile real delivery, but cannot invent an owning-start snapshot or
// turn a resume into ordinary completion evidence.
func TestLawBaselineMissingCurrentPinKeepsOutsideRepairRoute(t *testing.T) {
	const id = "pre-baseline-current-pin"
	s, _ := lawUpgradePrefixForDefinition(t, id, "proposal", 0, 0, "workflow.implementation")
	if _, err := ReadWorkPin(t.Context(), s, id); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, enactLawChange(t, s, id, nil, nil), KindMissingEvidence, "cannot infer owning start from a current pin")
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, id, "hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatal(err)
	}
	request := outsideRepairTestRequest(t, s, id, "reconcile")
	if err := outsideRepairApply(t, s, request, WorkflowOutsideRepairReconciled, workflowproof.Evidence{}); err == nil {
		t.Fatal("outside repair replaced missing delivery evidence with the current pin")
	}
	if err := outsideRepairApply(t, s, request, WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil || pin.Lifecycle != "completed" || pin.OutsideRepairDisposition == nil || pin.OutsideRepairDisposition.State != OutsideRepairStateCompleted {
		t.Fatalf("outside-repair disposition: %+v error=%v", pin, err)
	}
	var invented int
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM workflow_law_baselines WHERE work_id=?)+(SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind='workflow.completed')`, id, id).Scan(&invented); err != nil || invented != 0 {
		t.Fatalf("reconciliation invented ordinary workflow proof: count=%d error=%v", invented, err)
	}
}
