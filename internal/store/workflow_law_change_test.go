package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The fixture Product's registry root is "root"; the three child Domains
// stand for Concord's own child Domains in the CD-0230 D4 cases.
const lawChangeHashA = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func seedLawChangeFixture(t *testing.T, workID string) *Store {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	hash := "sha256:" + strings.Repeat("b", 64)
	for _, domainID := range []string{"agent-surface", "workflow-engine", "durable-authority"} {
		lawChangeFold(t, s, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product',?,?,'test domain','root','current',?,'test')`, domainID, "Domain "+domainID, hash)
	}
	return s
}

// lawChangeFold runs one projection write under an open fold guard.
func lawChangeFold(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(query, args...); err != nil {
		_, _ = db.Exec(`DELETE FROM fold_guard`)
		t.Fatalf("%s: %v", query, err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// seedLawChangeLaw records one accepted law homed in home with its tier and
// applies_to_domain_ids.
func seedLawChangeLaw(t *testing.T, s *Store, lawID, tier, home string, appliesTo ...string) {
	t.Helper()
	lawChangeFold(t, s, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid,authority_tier) VALUES('project','workflow-law-locator',?,'decision','accepted',?,?,?,'test',?)`, lawID, ".concord/docs/decisions/"+lawID+".md", lawID, lawChangeHashA, tier)
	lawChangeFold(t, s, `INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'product',?,?,'test')`, lawID, home, lawChangeHashA)
	setLawChangeApplicability(t, s, lawID, appliesTo...)
}

func setLawChangeApplicability(t *testing.T, s *Store, lawID string, appliesTo ...string) {
	t.Helper()
	lawChangeFold(t, s, `DELETE FROM law_domain_applicability WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id=?`, lawID)
	for _, domainID := range appliesTo {
		lawChangeFold(t, s, `INSERT INTO law_domain_applicability(home_project_id,home_locator_id,law_id,product_id,domain_id,scanned_commit_oid) VALUES('project','workflow-law-locator',?,'product',?,'test')`, lawID, domainID)
	}
}

func seedLawChangeSize(t *testing.T, s *Store, workID string, size WorkSize) {
	t.Helper()
	lawChangeFold(t, s, `INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at) VALUES(?,?,1,1,?,'{}','{}','2026-10-19T00:00:00Z')`, "size-"+workID, workID, string(size))
}

func lawChangeTx(t *testing.T, s *Store) *sql.Tx {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func lawChangeDomains(t *testing.T, s *Store, workID string, laws []string, binding *WorkflowArchitectureBinding) []string {
	t.Helper()
	tx := lawChangeTx(t, s)
	domains, err := workflowLawChangeDomainsTx(context.Background(), tx, workID, laws, binding, nil)
	if err != nil {
		t.Fatalf("count the law change's Domains: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return domains
}

var lawChangeV1 = WorkflowDefinition{Ref: "workflow.law-change-test", Version: 1, ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1}

func lawChangeFields(t *testing.T, values map[string]any) map[string]json.RawMessage {
	t.Helper()
	fields := map[string]json.RawMessage{
		workflowLawChangeBehaviorField: json.RawMessage(`false`),
		workflowLawChangeSecurityField: json.RawMessage(`false`),
	}
	for name, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		fields[name] = raw
	}
	return fields
}

// lawChangePins pins each law at the fixture's seeded content hash.
func lawChangePins(laws ...string) []WorkflowLawRevision {
	pins := make([]WorkflowLawRevision, 0, len(laws))
	for _, lawID := range laws {
		pins = append(pins, WorkflowLawRevision{LawID: lawID, ContentHash: lawChangeHashA})
	}
	return pins
}

func TestLawChangeRequiresExplicitP12Declarations(t *testing.T) {
	workID := "law-change-declarations"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	for _, name := range []string{workflowLawChangeBehaviorField, workflowLawChangeSecurityField} {
		for _, raw := range []string{"", "null", `"false"`, "0"} {
			fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise"})
			if raw == "" {
				delete(fields, name)
			} else {
				fields[name] = json.RawMessage(raw)
			}
			_, err := deriveLawChange(t, s, workID, lawChangeV1, fields, []string{"law-agent"}, nil, []string{})
			if err == nil {
				t.Errorf("%s=%q admitted without an explicit boolean declaration", name, raw)
			}
		}
	}
}

func deriveLawChange(t *testing.T, s *Store, workID string, definition WorkflowDefinition, fields map[string]json.RawMessage, laws []string, binding *WorkflowArchitectureBinding, decisionDomains []string) (*WorkflowLawChange, error) {
	t.Helper()
	tx := lawChangeTx(t, s)
	change, err := deriveWorkflowLawChangeTx(context.Background(), tx, definition, workID, fields, laws, binding, decisionDomains)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	return change, err
}

// enactLawChange runs the completion check. A hand-built fact without law_ids
// changes the laws the contract names.
func enactLawChange(t *testing.T, s *Store, workID string, change *WorkflowLawChange, laws []string, binding *WorkflowArchitectureBinding) error {
	t.Helper()
	if change.LawIDs == nil {
		change.LawIDs = uniqueSorted(workflowChangedLawIDs(laws, binding))
	}
	tx := lawChangeTx(t, s)
	err := checkWorkflowLawChangeEnactmentTx(context.Background(), tx, workID, change)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	return err
}

// TestLawChangeCountsChildDomainsOfChangedLaw holds the CD-0230 D4 Domain
// count: each changed law contributes its home and applies_to_domain_ids, the
// root Domain is never counted, and a registry change counts each modified
// Domain and both ends of each modified relation.
func TestLawChangeCountsChildDomainsOfChangedLaw(t *testing.T) {
	t.Parallel()
	workID := "law-change-count"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-workflow", "derived", "workflow-engine")
	seedLawChangeLaw(t, s, "law-root", "derived", "root")
	seedLawChangeLaw(t, s, "law-root-reaching", "derived", "root", "workflow-engine", "durable-authority")
	seedLawChangeLaw(t, s, "law-agent-self", "derived", "agent-surface", "agent-surface")
	cases := []struct {
		name    string
		laws    []string
		binding *WorkflowArchitectureBinding
		want    []string
	}{
		// CD-0063's installer amendment: one law homed in agent-surface. The
		// shipped code is the work's footprint and is not an input.
		{name: "one child-homed law", laws: []string{"law-agent"}, want: []string{"agent-surface"}},
		// One child-homed law and one root-homed law count one Domain.
		{name: "child and root homes", laws: []string{"law-agent", "law-root"}, want: []string{"agent-surface"}},
		{name: "two child homes", laws: []string{"law-agent", "law-workflow"}, want: []string{"agent-surface", "workflow-engine"}},
		{name: "root law's applicability counts", laws: []string{"law-root-reaching"}, want: []string{"durable-authority", "workflow-engine"}},
		{name: "applicability inside the home counts once", laws: []string{"law-agent-self"}, want: []string{"agent-surface"}},
		{name: "root law alone", laws: []string{"law-root"}, want: []string{}},
		{name: "relation between two child Domains", binding: &WorkflowArchitectureBinding{DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "agent-surface", Kind: "depends_on", TargetDomainID: "durable-authority"}}}, want: []string{"agent-surface", "durable-authority"}},
		{name: "added law homed in a child", binding: &WorkflowArchitectureBinding{LawAdditions: []WorkflowLawAddition{{LawID: "law-new", HomeDomainID: "workflow-engine"}}}, want: []string{"workflow-engine"}},
		{name: "registry edit of the root", binding: &WorkflowArchitectureBinding{DomainModifies: []string{"root"}}, want: []string{}},
	}
	for _, testCase := range cases {
		if got := lawChangeDomains(t, s, workID, testCase.laws, testCase.binding); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("%s: counted %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// TestLawChangeSeriousness holds P12 on the recorded fact: more than one child
// Domain, either agent declaration, a UI/UX decision, or crucial size.
func TestLawChangeSeriousness(t *testing.T) {
	t.Parallel()
	one := []string{"agent-surface"}
	cases := []struct {
		name    string
		change  WorkflowLawChange
		domains []string
		want    bool
	}{
		{name: "one Domain", change: WorkflowLawChange{DomainIDs: one}, domains: []string{}, want: false},
		{name: "two Domains", change: WorkflowLawChange{DomainIDs: []string{"agent-surface", "workflow-engine"}}, domains: []string{}, want: true},
		{name: "removes user-visible behavior", change: WorkflowLawChange{DomainIDs: one, RemovesUserVisibleBehavior: true}, domains: []string{}, want: true},
		{name: "security or data safety", change: WorkflowLawChange{DomainIDs: one, TouchesSecurityOrDataSafe: true}, domains: []string{}, want: true},
		{name: "UI/UX", change: WorkflowLawChange{DomainIDs: one}, domains: []string{"ui_ux"}, want: true},
		{name: "crucial size", change: WorkflowLawChange{DomainIDs: []string{}, Crucial: true}, domains: []string{}, want: true},
	}
	for _, testCase := range cases {
		if got := workflowLawChangeSerious(&testCase.change, testCase.domains); got != testCase.want {
			t.Errorf("%s: serious = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// TestLawChangeDerivationRecordsReasonAndSeriousness holds the approval half:
// a consequence_v1 contract that changes law records its reason with the
// derived Domains and seriousness, a missing reason refuses, every law change
// at crucial size is serious, and legacy or law-free contracts carry no fact.
func TestLawChangeDerivationRecordsReasonAndSeriousness(t *testing.T) {
	t.Parallel()
	workID := "law-change-derive"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-workflow", "derived", "workflow-engine")
	reason := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "the installer moved"})

	change, err := deriveLawChange(t, s, workID, lawChangeV1, reason, []string{"law-agent"}, nil, []string{})
	if err != nil {
		t.Fatalf("a non-serious law change with a reason refused: %v", err)
	}
	want := &WorkflowLawChange{Reason: "the installer moved", LawIDs: []string{"law-agent"}, DomainIDs: []string{"agent-surface"}}
	if !reflect.DeepEqual(change, want) {
		t.Fatalf("recorded %+v, want %+v", change, want)
	}

	pinned := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "the installer moved", workflowLawChangeTextPinsField: lawChangePins("law-workflow", "law-agent")})
	change, err = deriveLawChange(t, s, workID, lawChangeV1, pinned, []string{"law-agent", "law-workflow"}, nil, []string{})
	if err != nil || !change.Serious || !reflect.DeepEqual(change.TextPins, lawChangePins("law-agent", "law-workflow")) {
		t.Fatalf("a change of laws homed in two child Domains = %+v, %v; want a serious fact with sorted text pins", change, err)
	}

	if _, err := deriveLawChange(t, s, workID, lawChangeV1, map[string]json.RawMessage{}, []string{"law-agent"}, nil, []string{}); err == nil {
		t.Fatal("a law change without a recorded reason was admitted")
	} else {
		requireFailureKind(t, err, KindInvalidPayload, "missing reason")
	}

	if _, err := deriveLawChange(t, s, workID, WorkflowDefinition{Ref: "workflow.legacy", Version: 1}, reason, []string{"law-agent"}, nil, nil); err == nil {
		t.Fatal("a legacy contract accepted law_change fields")
	}
	if change, err := deriveLawChange(t, s, workID, lawChangeV1, map[string]json.RawMessage{}, nil, nil, []string{}); err != nil || change != nil {
		t.Fatalf("a contract that changes no law = %+v, %v; want no fact", change, err)
	}

	seedLawChangeSize(t, s, workID, WorkSizeCrucial)
	requireFailureKind(t, func() error {
		_, err := deriveLawChange(t, s, workID, lawChangeV1, reason, []string{"law-agent"}, nil, []string{})
		return err
	}(), KindInvalidPayload, "crucial change without text pins")
	change, err = deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "the installer moved", workflowLawChangeTextPinsField: lawChangePins("law-agent")}), []string{"law-agent"}, nil, []string{})
	if err != nil || !change.Crucial || !change.Serious {
		t.Fatalf("a one-Domain law change at crucial size = %+v, %v; want a serious crucial fact", change, err)
	}
}

// TestLawChangeConsentFollowsSeriousness holds the consent half: under
// consequence_v1 a serious law change requires the operator's approval on
// approve_contract and supersede_contract, a non-serious one requires none,
// and the legacy policy refuses the fact rather than answering from it.
func TestLawChangeConsentFollowsSeriousness(t *testing.T) {
	t.Parallel()
	definition := lawChangeV1
	definition.ActionDefinitions = []WorkflowActionDefinition{{ID: "approve_contract"}, {ID: "supersede_contract"}}
	cases := []struct {
		action  string
		serious bool
		want    WorkflowApprovalConsent
	}{
		{action: "approve_contract", serious: false, want: WorkflowConsentNone},
		{action: "approve_contract", serious: true, want: WorkflowConsentRequired},
		{action: "supersede_contract", serious: false, want: WorkflowConsentNone},
		{action: "supersede_contract", serious: true, want: WorkflowConsentRequired},
	}
	for _, testCase := range cases {
		result, err := ClassifyWorkflowApproval(definition, testCase.action, WorkflowApprovalFacts{ProposedDecisionDomains: []string{}, SeriousLawChange: testCase.serious})
		if err != nil {
			t.Fatalf("%s serious=%v: %v", testCase.action, testCase.serious, err)
		}
		if result.Consent != testCase.want {
			t.Errorf("%s serious=%v: consent %s, want %s", testCase.action, testCase.serious, result.Consent, testCase.want)
		}
	}
	legacy := definition
	legacy.ApprovalPolicy = ""
	legacy.ActionDefinitions = []WorkflowActionDefinition{{ID: "approve_contract", Approval: ActionApprovalRequired}}
	if _, err := ClassifyWorkflowApproval(legacy, "approve_contract", WorkflowApprovalFacts{SeriousLawChange: true}); err == nil {
		t.Fatal("the legacy policy answered from a law-change fact")
	}
}

// TestLawChangeBoundaryDerivesTheConstructorsFact holds that the consent
// boundary reads the same seriousness the constructor records, from the same
// payload fields.
func TestLawChangeBoundaryDerivesTheConstructorsFact(t *testing.T) {
	t.Parallel()
	workID := "law-change-boundary"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-workflow", "derived", "workflow-engine")
	for _, testCase := range []struct {
		laws []string
		want bool
	}{{laws: []string{"law-agent"}, want: false}, {laws: []string{"law-agent", "law-workflow"}, want: true}} {
		values := map[string]any{"law_modifies": testCase.laws, "decision_domains": []string{}, workflowLawChangeReasonField: "revise"}
		if testCase.want {
			values[workflowLawChangeTextPinsField] = lawChangePins(testCase.laws...)
		}
		fields := lawChangeFields(t, values)
		tx := lawChangeTx(t, s)
		serious, err := workflowProposedLawChangeSeriousTx(context.Background(), tx, lawChangeV1, workID, "approval", fields)
		_ = tx.Rollback()
		if err != nil || serious != testCase.want {
			t.Errorf("laws %v: serious = %v, %v; want %v", testCase.laws, serious, err, testCase.want)
		}
	}
}

// TestLawChangeEnactmentRequiresDerivedTierAndOneDomain holds the completion
// half for a change the operator did not approve: each changed accepted law
// must carry the derived tier, including a record that held the legislated
// cohort default, and the Domains before and after the change must still
// number at most one.
func TestLawChangeEnactmentRequiresDerivedTierAndOneDomain(t *testing.T) {
	t.Parallel()
	nonSerious := func() *WorkflowLawChange {
		return &WorkflowLawChange{Reason: "revise", DomainIDs: []string{"agent-surface"}}
	}
	t.Run("unresolved after-state refuses", func(t *testing.T) {
		workID := "law-change-enact-missing"
		s := seedLawChangeFixture(t, workID)
		requireFailureKind(t, enactLawChange(t, s, workID, nonSerious(), []string{"law-missing"}, nil), KindMissingEvidence, "unresolved changed law")
	})
	t.Run("crucial size at enactment requires approval", func(t *testing.T) {
		workID := "law-change-enact-crucial"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		seedLawChangeSize(t, s, workID, WorkSizeCrucial)
		requireFailureKind(t, enactLawChange(t, s, workID, nonSerious(), []string{"law-agent"}, nil), KindApprovalRequired, "crucial enactment")
	})

	t.Run("derived tier is admitted", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-derived"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		if err := enactLawChange(t, s, workID, nonSerious(), []string{"law-agent"}, nil); err != nil {
			t.Fatalf("a derived non-serious change refused at completion: %v", err)
		}
	})

	t.Run("a cohort record must become derived", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-cohort"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface")
		requireFailureKind(t, enactLawChange(t, s, workID, nonSerious(), []string{"law-agent"}, nil), KindMissingEvidence, "legislated tier after an unapproved change")
		lawChangeFold(t, s, `UPDATE law_subjects SET authority_tier='derived' WHERE law_id='law-agent'`)
		if err := enactLawChange(t, s, workID, nonSerious(), []string{"law-agent"}, nil); err != nil {
			t.Fatalf("the record carrying derived after the change refused: %v", err)
		}
	})

	t.Run("an added law must be derived", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-added"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-new", "legislated", "agent-surface")
		binding := &WorkflowArchitectureBinding{LawAdditions: []WorkflowLawAddition{{LawID: "law-new", HomeDomainID: "agent-surface"}}}
		requireFailureKind(t, enactLawChange(t, s, workID, nonSerious(), nil, binding), KindMissingEvidence, "legislated added law")
	})

	t.Run("a law moved between child Domains counts both", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-moved"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		lawChangeFold(t, s, `UPDATE law_domain_homes SET domain_id='workflow-engine' WHERE law_id='law-agent'`)
		requireFailureKind(t, enactLawChange(t, s, workID, nonSerious(), []string{"law-agent"}, nil), KindApprovalRequired, "moved law")
	})

	t.Run("narrowed applicability still counts the dropped Domain", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-narrowed"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface", "workflow-engine")
		change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "narrow", workflowLawChangeTextPinsField: lawChangePins("law-agent")}), []string{"law-agent"}, nil, []string{})
		if err != nil || !change.Serious {
			t.Fatalf("a law whose applicability reaches a second child Domain = %+v, %v; want serious before narrowing", change, err)
		}
		setLawChangeApplicability(t, s, "law-agent")
		if got := lawChangeDomains(t, s, workID, []string{"law-agent"}, nil); !reflect.DeepEqual(got, []string{"agent-surface"}) {
			t.Fatalf("after narrowing the projection counts %v; the approval-time fact is what keeps the dropped Domain", got)
		}
	})

	t.Run("removing laws homed in two child Domains is serious", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-removed"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface")
		seedLawChangeLaw(t, s, "law-workflow", "legislated", "workflow-engine")
		change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "retire", workflowLawChangeTextPinsField: lawChangePins("law-agent", "law-workflow")}), []string{"law-agent", "law-workflow"}, nil, []string{})
		if err != nil || !change.Serious {
			t.Fatalf("removing laws homed in two child Domains = %+v, %v; want serious", change, err)
		}
	})

	t.Run("a superseded revision also carries derived", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-superseded"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface")
		lawChangeFold(t, s, `UPDATE law_subjects SET status='superseded' WHERE law_id='law-agent'`)
		requireFailureKind(t, enactLawChange(t, s, workID, nonSerious(), []string{"law-agent"}, nil), KindMissingEvidence, "superseded revision without derived tier")
	})

}

// TestLawChangeEnactmentMatchesApprovedText holds the completion half for a
// serious change: the enacted text must be the text the operator approved.
// The approved text fixes the tier, so a pinned legislated record passes; any
// other bytes, a missing record, or a different registry refuse.
func TestLawChangeEnactmentMatchesApprovedText(t *testing.T) {
	t.Parallel()
	serious := func(pins []WorkflowLawRevision, registryHash string) *WorkflowLawChange {
		return &WorkflowLawChange{Reason: "revise", DomainIDs: []string{"agent-surface"}, TouchesSecurityOrDataSafe: true, Serious: true, TextPins: pins, RegistryContentHash: registryHash}
	}
	workID := "law-change-enact-approved"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface")
	if err := enactLawChange(t, s, workID, serious(lawChangePins("law-agent"), ""), []string{"law-agent"}, nil); err != nil {
		t.Fatalf("the approved text refused at completion: %v", err)
	}
	other := []WorkflowLawRevision{{LawID: "law-agent", ContentHash: "sha256:" + strings.Repeat("c", 64)}}
	requireFailureKind(t, enactLawChange(t, s, workID, serious(other, ""), []string{"law-agent"}, nil), KindApprovalRequired, "enacted text differs from the approved text")
	requireFailureKind(t, enactLawChange(t, s, workID, serious(lawChangePins("law-gone"), ""), []string{"law-gone"}, nil), KindMissingEvidence, "approved law missing from the projection")

	var registryHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries WHERE product_id='product'`).Scan(&registryHash); err != nil {
		t.Fatal(err)
	}
	edit := &WorkflowArchitectureBinding{DomainModifies: []string{"agent-surface"}}
	if err := enactLawChange(t, s, workID, serious(nil, registryHash), nil, edit); err != nil {
		t.Fatalf("the approved registry refused at completion: %v", err)
	}
	requireFailureKind(t, enactLawChange(t, s, workID, serious(nil, "sha256:"+strings.Repeat("d", 64)), nil, edit), KindApprovalRequired, "enacted registry differs from the approved registry")
}

// TestLawChangeTextPinsBindTheChange holds the approval half of the exact-text
// rule: a serious change pins each changed law exactly once and the registry
// exactly when it edits the registry; a non-serious change carries no pins.
func TestLawChangeTextPinsBindTheChange(t *testing.T) {
	t.Parallel()
	workID := "law-change-pins"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-workflow", "derived", "workflow-engine")
	two := []string{"law-agent", "law-workflow"}
	hash := "sha256:" + strings.Repeat("e", 64)
	derive := func(values map[string]any, laws []string, binding *WorkflowArchitectureBinding) error {
		values[workflowLawChangeReasonField] = "revise"
		_, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, values), laws, binding, []string{})
		return err
	}
	for name, values := range map[string]map[string]any{
		"no pins":              {},
		"one law missing":      {workflowLawChangeTextPinsField: lawChangePins("law-agent")},
		"an extra law":         {workflowLawChangeTextPinsField: lawChangePins("law-agent", "law-workflow", "law-other")},
		"a duplicate law":      {workflowLawChangeTextPinsField: lawChangePins("law-agent", "law-agent", "law-workflow")},
		"an empty list":        {workflowLawChangeTextPinsField: []WorkflowLawRevision{}},
		"a malformed hash":     {workflowLawChangeTextPinsField: []WorkflowLawRevision{{LawID: "law-agent", ContentHash: lawChangeHashA}, {LawID: "law-workflow", ContentHash: "sha256:short"}}},
		"a stray registry pin": {workflowLawChangeTextPinsField: lawChangePins(two...), workflowLawChangeRegistryHashField: hash},
	} {
		requireFailureKind(t, derive(values, two, nil), KindInvalidPayload, name)
	}
	if err := derive(map[string]any{workflowLawChangeTextPinsField: lawChangePins(two...)}, two, nil); err != nil {
		t.Fatalf("exact pins refused: %v", err)
	}

	relation := &WorkflowArchitectureBinding{DomainRelationModifies: []WorkflowDomainRelationModification{{SourceDomainID: "agent-surface", Kind: "depends_on", TargetDomainID: "workflow-engine"}}}
	requireFailureKind(t, derive(map[string]any{}, nil, relation), KindInvalidPayload, "a serious registry edit without a registry pin")
	requireFailureKind(t, derive(map[string]any{workflowLawChangeRegistryHashField: "sha256:short"}, nil, relation), KindInvalidPayload, "a malformed registry pin")
	if err := derive(map[string]any{workflowLawChangeRegistryHashField: hash}, nil, relation); err != nil {
		t.Fatalf("a pinned serious registry edit refused: %v", err)
	}

	requireFailureKind(t, derive(map[string]any{workflowLawChangeTextPinsField: lawChangePins("law-agent")}, []string{"law-agent"}, nil), KindInvalidPayload, "text pins on a non-serious change")
}

// TestLawChangeShapeValidation holds the replay-safe shape check: the fold
// refuses a recorded fact whose seriousness does not follow its own facts.
func TestLawChangeShapeValidation(t *testing.T) {
	t.Parallel()
	agent := []string{"law-agent"}
	if err := validateWorkflowLawChangeShape(&WorkflowLawChange{Reason: "revise", LawIDs: agent, DomainIDs: []string{"agent-surface"}}, []string{}, agent, nil); err != nil {
		t.Fatalf("a well-formed fact refused: %v", err)
	}
	for name, change := range map[string]WorkflowLawChange{
		"lowered seriousness":        {Reason: "revise", LawIDs: agent, DomainIDs: []string{"agent-surface", "workflow-engine"}},
		"lowered inherited serious":  {Reason: "revise", LawIDs: agent, DomainIDs: []string{"agent-surface"}, PredecessorSerious: true},
		"missing reason":             {LawIDs: agent, DomainIDs: []string{}},
		"unsorted Domains":           {Reason: "revise", LawIDs: agent, DomainIDs: []string{"workflow-engine", "agent-surface"}, Serious: true},
		"null Domains":               {Reason: "revise", LawIDs: agent},
		"null law_ids":               {Reason: "revise", DomainIDs: []string{"agent-surface"}},
		"law_ids omit a changed law": {Reason: "revise", LawIDs: []string{}, DomainIDs: []string{"agent-surface"}},
		"unpinned serious":           {Reason: "revise", LawIDs: agent, DomainIDs: []string{"agent-surface"}, Crucial: true, Serious: true},
		"pinned non-serious":         {Reason: "revise", LawIDs: agent, DomainIDs: []string{"agent-surface"}, TextPins: lawChangePins("law-agent")},
	} {
		if err := validateWorkflowLawChangeShape(&change, []string{}, agent, nil); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
	pinned := WorkflowLawChange{Reason: "revise", LawIDs: agent, DomainIDs: []string{"agent-surface"}, Crucial: true, Serious: true, TextPins: lawChangePins("law-agent")}
	requireFailureKind(t, validateWorkflowLawChangeShape(&pinned, []string{}, agent, nil), KindApprovalRequired, "a pinned serious fact without consumed approval")
	pinned.Approval = lawChangeApprovalProof()
	if err := validateWorkflowLawChangeShape(&pinned, []string{}, agent, nil); err != nil {
		t.Fatalf("a pinned serious fact refused: %v", err)
	}
}

func lawChangeApprovalProof() *workflowLawChangeApproval {
	ref := strings.Repeat("c", 64)
	return &workflowLawChangeApproval{
		workflowApprovalBinding: workflowApprovalBinding{ApprovalRef: ref, OperationDigest: lawChangeHashA, ScopeJSON: `{}`, VersionsJSON: `{"work":1}`, Consequence: "workflow_action"},
		Operator:                WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/operator", AgentRef: "approval:" + ref, SessionRef: "session/operator", ActorClass: ActorOperator},
	}
}

func TestLawChangeApprovalRequiresConsumedIdentity(t *testing.T) {
	workID := "law-change-approval"
	s := seedLawChangeFixture(t, workID)
	seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
	proof := lawChangeApprovalProof()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,'active',?,'[]','[]','[]','2026-09-10T00:00:00Z')`, proof.Operator.ClientRef, proof.Operator.PrincipalRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,'2026-09-10T00:00:00Z','2026-09-11T00:00:00Z',1,0,'fixture',?)`, proof.ApprovalRef, proof.OperationDigest, proof.ScopeJSON, proof.VersionsJSON, proof.Consequence, proof.Operator.PrincipalRef, proof.Operator.ClientRef, proof.Operator.SessionRef, lawChangeHashA); err != nil {
		t.Fatal(err)
	}
	request := WorkflowActionExecutionRequest{OperatorApprovalRef: proof.ApprovalRef, ApprovalOperationDigest: proof.OperationDigest, ApprovalScopeJSON: proof.ScopeJSON, ApprovalVersionsJSON: proof.VersionsJSON, ApprovalConsequence: proof.Consequence}
	change := &WorkflowLawChange{Reason: "revise", LawIDs: []string{"law-agent"}, DomainIDs: []string{"agent-surface"}, Crucial: true, Serious: true, TextPins: lawChangePins("law-agent")}
	bind := func(request WorkflowActionExecutionRequest) error {
		tx := lawChangeTx(t, s)
		err := bindWorkflowLawChangeApprovalTx(context.Background(), tx, request, change)
		_ = tx.Rollback()
		return err
	}
	requireFailureKind(t, bind(request), KindApprovalRequired, "unconsumed approval")
	if _, err := db.Exec(`UPDATE agent_approvals SET used_count=1 WHERE approval_ref=?`, proof.ApprovalRef); err != nil {
		t.Fatal(err)
	}
	wrong := request
	wrong.ApprovalOperationDigest = "sha256:" + strings.Repeat("d", 64)
	requireFailureKind(t, bind(wrong), KindUnauthorized, "approval for a different operation")
	if err := bind(request); err != nil {
		t.Fatalf("consumed approval refused: %v", err)
	}
	if !reflect.DeepEqual(change.Approval, proof) {
		t.Fatalf("recorded proof %+v, want consumed identity %+v", change.Approval, proof)
	}
	change.Approval.Operator.ActorClass = ActorAgent
	requireFailureKind(t, validateWorkflowLawChangeApproval(change), KindUnauthorized, "agent relabeled as operator")
	change.Approval = proof
	change.Approval.Operator.PrincipalRef = "principal/other"
	seedLawChangeSize(t, s, workID, WorkSizeCrucial)
	tx := lawChangeTx(t, s)
	err := validateLiveWorkflowLawChangeTx(context.Background(), tx, workID, workflowContractApprovedPayload{LawChange: change, LawModifies: []string{"law-agent"}})
	_ = tx.Rollback()
	requireFailureKind(t, err, KindUnauthorized, "operator differs from consumed approval")
}

func TestLawChangeReplayKeepsRecordedApproval(t *testing.T) {
	const workID = "law-change-replay"
	definition := approvalRouteDefinition(t)
	registry, registered := newResolutionRegistry(t, definition)
	s, owner := seedResolutionSelection(t, workID, registry, registered)
	seedIssue31DomainRegistry(t, s)
	seedLawChangeLaw(t, s, "law-agent", "derived", "root")
	change := &WorkflowLawChange{Reason: "revise", LawIDs: []string{"law-agent"}, DomainIDs: []string{}, Serious: true, TouchesSecurityOrDataSafe: true, TextPins: lawChangePins("law-agent"), Approval: lawChangeApprovalProof()}
	version := readWorkVersion(t, s, workID)
	event := workflowTypedEvent("law-replay", WorkflowContractApproved, workID, ownerRefForTest(t, owner), time.Unix(30, 0), version, map[string]any{
		"contract_version": 1, "premise": "The law change preserves its exact approved text.",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law", "immutable_subject_ref": "commit:law", "expected_result": "pass"}}},
		"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		"spec_mandate": []string{"law-agent"}, "law_modifies": []string{"law-agent"}, "law_revisions": lawChangePins("law-agent"), "law_boundary_version": 1,
		"decision_domains": []string{}, "law_change": change,
	})
	for _, vector := range []struct {
		name        string
		source      int
		omit        bool
		wantFailure bool
	}{
		{name: "current source cannot omit law change", source: 6, omit: true, wantFailure: true},
		{name: "old source cannot forge law change", source: 5, wantFailure: true},
		{name: "old source retains historical absence", source: 5, omit: true},
	} {
		t.Run(vector.name, func(t *testing.T) {
			candidate := event
			candidate.replaySourcePayloadVersion = vector.source
			if vector.omit {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(candidate.Payload, &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "law_change")
				candidate.Payload = mustJSONValue(fields)
			}
			tx := lawChangeTx(t, s)
			scope, err := beginFold(context.Background(), tx)
			if err != nil {
				t.Fatal(err)
			}
			if err := scope.bindRegistry(tx, registry); err != nil {
				t.Fatal(err)
			}
			err = foldWorkflowContractApproved(workflowReplayContext(context.Background()), tx, candidate, scope)
			_ = tx.Rollback()
			if vector.wantFailure {
				requireFailureKind(t, err, KindInvalidPayload, vector.name)
			} else if err != nil {
				t.Fatalf("historical absence refused: %v", err)
			}
		})
	}
	// No mutable approval row exists. The live fold must refuse; replay uses
	// the proof recorded at the original live admission instead.
	tx := lawChangeTx(t, s)
	scope, err := beginFold(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.bindRegistry(tx, registry); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, validateLiveWorkflowLawChangeTx(context.Background(), tx, workID, workflowContractApprovedPayload{LawChange: change, LawModifies: []string{"law-agent"}}), KindApprovalRequired, "live admission without the consumed approval")
	if err := foldWorkflowContractApproved(workflowReplayContext(context.Background()), tx, event, scope); err != nil {
		t.Fatalf("recorded proof refused during replay: %v", err)
	}
	var encoded string
	if err := tx.QueryRow(`SELECT law_change FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var restored WorkflowLawChange
	if err := json.Unmarshal([]byte(encoded), &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&restored, change) {
		t.Fatalf("replay changed the consent proof: got %+v, want %+v", restored, change)
	}
}

func TestLawChangeRegistryCountUsesEnactedEntries(t *testing.T) {
	for _, vector := range []struct {
		name     string
		query    string
		wantKind FailureKind
	}{
		{name: "root and one child still count one", query: `UPDATE domains SET purpose='amended' WHERE domain_id IN ('root','agent-surface')`},
		{name: "undeclared second entry counts", query: `UPDATE domains SET purpose='amended' WHERE domain_id IN ('agent-surface','workflow-engine')`, wantKind: KindApprovalRequired},
		{name: "added relation counts both ends", query: `INSERT INTO domain_architecture_relations(home_project_id,home_locator_id,product_id,source_domain_id,kind,target_domain_id,state,registry_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','product','agent-surface','depends_on','workflow-engine','active',content_hash,'test' FROM domain_registries WHERE product_id='product'`, wantKind: KindApprovalRequired},
		{name: "removed child counts its before-state", query: `DELETE FROM domains WHERE domain_id='workflow-engine'`, wantKind: KindApprovalRequired},
	} {
		t.Run(vector.name, func(t *testing.T) {
			const workID = "law-change-registry-delta"
			s := seedLawChangeFixture(t, workID)
			// The authored scope names one child only. The completion count
			// must also include any other entry the actual registry changed.
			binding := &WorkflowArchitectureBinding{DomainModifies: []string{"agent-surface"}}
			change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "revise registry"}), nil, binding, []string{})
			if err != nil || change.Serious || change.RegistryBefore == nil {
				t.Fatalf("one-child registry contract: %+v, %v", change, err)
			}
			lawChangeFold(t, s, vector.query)
			err = enactLawChange(t, s, workID, change, nil, binding)
			if vector.wantKind != "" {
				requireFailureKind(t, err, vector.wantKind, vector.name)
			} else if err != nil {
				t.Fatalf("one-child enacted registry refused: %v", err)
			}
		})
	}
}

// recordLawChangeContract records contract version as the item's active
// contract with the given law change and supersedes every earlier one.
func recordLawChangeContract(t *testing.T, s *Store, workID string, version int64, change *WorkflowLawChange) {
	t.Helper()
	encoded, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	actor := "actor:" + strings.Repeat("a", 64)
	lawChangeFold(t, s, `INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal/law','client/law','agent/law','session/law','agent','2026-10-19T00:00:00Z')`, actor)
	lawChangeFold(t, s, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_change) VALUES(?,?,'The law change fixture.','internal_sqlite','[]','[]','2026-10-19T00:00:00Z',?,'[]',?)`, workID, version, actor, string(encoded))
	lawChangeFold(t, s, `UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version<? AND superseded_by IS NULL`, version, workID, version)
}

// TestLawChangeSuccessorKeepsTheItemBaseline holds that the before-state
// belongs to the work item, not to one contract version: a successor counts
// the enacted state against the baseline its predecessors recorded, so a
// routine or re-pin successor cannot lower the count of a change already
// made, and a cumulative serious change needs the operator's approval of the
// cumulative after-state text.
func TestLawChangeSuccessorKeepsTheItemBaseline(t *testing.T) {
	t.Parallel()
	consent := lawChangeV1
	consent.ActionDefinitions = []WorkflowActionDefinition{{ID: "approve_contract"}, {ID: "supersede_contract"}}

	t.Run("a law moved across a routine successor counts both homes", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-successor-move"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "derived", "agent-surface")
		reason := map[string]any{workflowLawChangeReasonField: "move the law"}
		v1, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, reason), []string{"law-agent"}, nil, []string{})
		if err != nil || v1.Serious || !reflect.DeepEqual(v1.DomainIDs, []string{"agent-surface"}) {
			t.Fatalf("v1 = %+v, %v; want a non-serious change in agent-surface", v1, err)
		}
		recordLawChangeContract(t, s, workID, 1, v1)
		lawChangeFold(t, s, `UPDATE law_domain_homes SET domain_id='workflow-engine' WHERE law_id='law-agent'`)
		requireFailureKind(t, enactLawChange(t, s, workID, v1, nil, nil), KindApprovalRequired, "v1 completion of the moved law")

		_, err = deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, reason), []string{"law-agent"}, nil, []string{})
		requireFailureKind(t, err, KindInvalidPayload, "a routine successor without the cumulative text pins")

		pinned := map[string]any{"law_modifies": []string{"law-agent"}, "decision_domains": []string{}, workflowLawChangeReasonField: "move the law", workflowLawChangeTextPinsField: lawChangePins("law-agent")}
		tx := lawChangeTx(t, s)
		serious, err := workflowProposedLawChangeSeriousTx(context.Background(), tx, lawChangeV1, workID, "successor", lawChangeFields(t, pinned))
		_ = tx.Rollback()
		if err != nil || !serious {
			t.Fatalf("successor consent boundary serious = %v, %v; want serious", serious, err)
		}
		result, err := ClassifyWorkflowApproval(consent, "supersede_contract", WorkflowApprovalFacts{SeriousLawChange: serious})
		if err != nil || result.Consent != WorkflowConsentRequired {
			t.Fatalf("successor consent = %+v, %v; want the operator's approval", result, err)
		}

		v2, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, pinned), []string{"law-agent"}, nil, []string{})
		if err != nil || !v2.Serious || !reflect.DeepEqual(v2.DomainIDs, []string{"agent-surface", "workflow-engine"}) {
			t.Fatalf("v2 = %+v, %v; want a serious change counting both homes", v2, err)
		}

		narrowed, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, pinned), nil, nil, []string{})
		if err != nil || narrowed == nil || !narrowed.Serious || !reflect.DeepEqual(narrowed.LawIDs, []string{"law-agent"}) {
			t.Fatalf("a successor naming no law = %+v, %v; want the item's serious change kept", narrowed, err)
		}
		tx = lawChangeTx(t, s)
		err = validateLiveWorkflowLawChangeTx(context.Background(), tx, workID, workflowContractApprovedPayload{})
		_ = tx.Rollback()
		requireFailureKind(t, err, KindInvalidPayload, "a successor dropping the recorded law change")
	})

	t.Run("a registry re-pin successor keeps the first before-state", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-successor-repin"
		s := seedLawChangeFixture(t, workID)
		binding := &WorkflowArchitectureBinding{DomainModifies: []string{"agent-surface"}}
		reason := map[string]any{workflowLawChangeReasonField: "revise registry"}
		v1, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, reason), nil, binding, []string{})
		if err != nil || v1.Serious || v1.RegistryBefore == nil {
			t.Fatalf("v1 = %+v, %v; want a non-serious registry edit with its before-state", v1, err)
		}
		recordLawChangeContract(t, s, workID, 1, v1)
		lawChangeFold(t, s, `UPDATE domains SET purpose='amended' WHERE domain_id IN ('agent-surface','workflow-engine')`)
		var enacted string
		if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM domain_registries WHERE product_id='product'`).Scan(&enacted); err != nil {
			t.Fatal(err)
		}
		requireFailureKind(t, enactLawChange(t, s, workID, v1, nil, nil), KindApprovalRequired, "v1 completion of the undeclared second entry")

		_, err = deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, reason), nil, binding, []string{})
		requireFailureKind(t, err, KindInvalidPayload, "a re-pin successor without the cumulative registry pin")

		repin := map[string]any{workflowLawChangeReasonField: "revise registry", workflowLawChangeRegistryHashField: enacted}
		v2, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, repin), nil, binding, []string{})
		if err != nil || !v2.Serious || !reflect.DeepEqual(v2.DomainIDs, []string{"agent-surface", "workflow-engine"}) {
			t.Fatalf("v2 = %+v, %v; want a serious change counting both changed entries", v2, err)
		}
		if !reflect.DeepEqual(v2.RegistryBefore, v1.RegistryBefore) {
			t.Fatalf("the re-pin successor took a new before-state %+v; want the first %+v", v2.RegistryBefore, v1.RegistryBefore)
		}
		if err := enactLawChange(t, s, workID, v2, nil, nil); err != nil {
			t.Fatalf("the approved cumulative registry refused at completion: %v", err)
		}
	})
}
