package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
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

func enactLawChange(t *testing.T, s *Store, workID string, change *WorkflowLawChange, laws []string, binding *WorkflowArchitectureBinding) error {
	t.Helper()
	tx := lawChangeTx(t, s)
	err := checkWorkflowLawChangeEnactmentTx(context.Background(), tx, workID, change, []string{}, laws, binding)
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
	want := &WorkflowLawChange{Reason: "the installer moved", DomainIDs: []string{"agent-surface"}}
	if !reflect.DeepEqual(change, want) {
		t.Fatalf("recorded %+v, want %+v", change, want)
	}

	change, err = deriveLawChange(t, s, workID, lawChangeV1, reason, []string{"law-agent", "law-workflow"}, nil, []string{})
	if err != nil || !change.Serious {
		t.Fatalf("a change of laws homed in two child Domains = %+v, %v; want a serious fact", change, err)
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
	change, err = deriveLawChange(t, s, workID, lawChangeV1, reason, []string{"law-agent"}, nil, []string{})
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
		fields := lawChangeFields(t, map[string]any{"law_modifies": testCase.laws, "decision_domains": []string{}, workflowLawChangeReasonField: "revise"})
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
		change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "narrow"}), []string{"law-agent"}, nil, []string{})
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
		change, err := deriveLawChange(t, s, workID, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "retire"}), []string{"law-agent", "law-workflow"}, nil, []string{})
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

	t.Run("an operator-approved change is not re-checked", func(t *testing.T) {
		t.Parallel()
		workID := "law-change-enact-approved"
		s := seedLawChangeFixture(t, workID)
		seedLawChangeLaw(t, s, "law-agent", "legislated", "agent-surface")
		serious := &WorkflowLawChange{Reason: "revise", DomainIDs: []string{"agent-surface"}, TouchesSecurityOrDataSafe: true, Serious: true}
		if err := enactLawChange(t, s, workID, serious, []string{"law-agent"}, nil); err != nil {
			t.Fatalf("an operator-approved change was held to the derived tier: %v", err)
		}
	})
}

// TestLawChangeShapeValidation holds the replay-safe shape check: the fold
// refuses a recorded fact whose seriousness does not follow its own facts.
func TestLawChangeShapeValidation(t *testing.T) {
	t.Parallel()
	if err := validateWorkflowLawChangeShape(&WorkflowLawChange{Reason: "revise", DomainIDs: []string{"agent-surface"}}, []string{}); err != nil {
		t.Fatalf("a well-formed fact refused: %v", err)
	}
	for name, change := range map[string]WorkflowLawChange{
		"lowered seriousness": {Reason: "revise", DomainIDs: []string{"agent-surface", "workflow-engine"}},
		"missing reason":      {DomainIDs: []string{}},
		"unsorted Domains":    {Reason: "revise", DomainIDs: []string{"workflow-engine", "agent-surface"}, Serious: true},
		"null Domains":        {Reason: "revise"},
	} {
		if err := validateWorkflowLawChangeShape(&change, []string{}); err == nil {
			t.Errorf("%s: admitted", name)
		}
	}
}
