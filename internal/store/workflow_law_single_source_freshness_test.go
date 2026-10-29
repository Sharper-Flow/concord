package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/workflowproof"
)

// An empty mandate must not hide a committed law edit behind stale projection
// rows, whether the caller omitted proof or retained proof from before the edit.
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
	var path string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM law_subjects WHERE home_project_id=? AND law_id='HOME-LAW'`, home.HomeProjectID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, home.RepoPath, path, "# Changed law outside the empty contract\n")
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
			t.Errorf("law-free completion trusted stale single-source projection (withProof=%v)", withProof)
		} else {
			requireFailureKind(t, err, KindInvalidOperation, "single-source proof freshness")
			message := "was not verified before this transaction"
			if withProof {
				message = "moved to a new commit after its verification"
			}
			if !strings.Contains(err.Error(), message) {
				t.Errorf("wrong single-source refusal (withProof=%v): %v", withProof, err)
			}
		}
	}
}

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
				writeKnowledgeFile(t, home.RepoPath, "unrelated.go", "package synthetic\n")
				commitKnowledgeRepo(t, home.RepoPath, "move after start proof")
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
				message = "moved to a new commit after its verification"
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

func TestLawProofStandaloneHomeKeepsLegacyRead(t *testing.T) {
	s := seedLawChangeFixture(t, "standalone-law")
	lawChangeFold(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='product'`)
	tx := lawChangeTx(t, s)
	if err := checkMandatedLawsTxAtHome(t.Context(), tx, "project", "workflow-law-locator", []string{"spec:one"}, nil, false); err != nil {
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

func TestBootstrapInitializesVerifiedProductLawBaseline(t *testing.T) {
	ctx := t.Context()
	s := openTemp(t)
	repo := initBootstrapStoreRepo(t)
	seedBootstrapStoreAuthority(t, s, repo)
	lawRepo := initKnowledgeRepo(t)
	writeManifestFixture(t, lawRepo, manifestFixture{ID: "BOOTSTRAP-LAW", Kind: "decision", Path: ".concord/docs/decisions/CD-0903-bootstrap-law.md", Status: "accepted", Date: "2026-09-20T00:00:00Z", Title: "Bootstrap law", Summary: "A separate Product law home", Scopes: homeScope()})
	commitKnowledgeRepo(t, lawRepo, "Product law before bootstrap")
	home := KnowledgeHome{HomeProjectID: "bootstrap-law-project", HomeLocatorID: "bootstrap-law-locator", RepoPath: lawRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-bootstrap", home, "project-bootstrap", home.HomeProjectID)
	// No explicit rebuild: native bootstrap must prepare the designated home,
	// not confuse it with the execution Project's canonical checkout.
	request := bootstrapStoreRequest()
	request.IdempotencyKey = "bootstrap-verified-law"
	operationID, workID, digest, err := CanonicalBootstrapIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	location, err := s.LocateWorktree(ctx, request.ProjectID, workID, request.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepareBootstrapMode(ctx, request, operationID, workID, digest, false, request, location, ExecGitRunner{}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var baseline workflowLawBaseline
	if err := json.Unmarshal([]byte(raw), &baseline); err != nil || baseline.Home.ProjectID != home.HomeProjectID || !strings.Contains(raw, "bootstrap-law-project/BOOTSTRAP-LAW") {
		t.Fatalf("bootstrap did not pin its verified Product law home: %s error=%v", raw, err)
	}
}
