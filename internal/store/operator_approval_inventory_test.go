package store

// The operator approval-point inventory regression.
//
// The inventory derives each operator approval point from its owning source,
// never from law prose. Ground truths:
//
//  1. Registered definition enumeration. Every builtin workflow definition in
//     ascending version history is walked for actions whose resolved Approval
//     is ActionApprovalRequired, and off-pin recovery resolution
//     (workflowRecoveryActionDefinition) contributes the triples it can
//     resolve for every registered definition. Resolved definition data, not
//     token grep, is the truth for registry points.
//  2. A Go AST census of internal/{agent,store} production sources. Test
//     files and Code generated files are excluded by rule, not by list. The
//     census counts: approval metadata declarations
//     (ActionApprovalRequired, WorkflowOperatorRequired,
//     RequireOperatorIdentity, including package-qualified references
//     resolved through the file's imports and composite-literal keys);
//     demand assignments to the consent flags ApprovalRequired,
//     requiresApproval, and approvalRequired, split per OR-disjunct and per
//     resolution kind into stable predicate branch identities, with
//     literal-true assignments keyed by their governing branch token; guard
//     returns carrying KindApprovalRequired or the "approval_required" error
//     kind; operator receipt-requirement guards on OperatorApprovalRef; law
//     admission owners (the validateWorkflowContractApprovalLaw /
//     validateDerivedLawModification / refuseNonDerivedLawModification
//     surfaces); the manifest approval axis (one point per gated
//     tool-surface operation behind the plan default); and
//     challenge/receipt/authority caller sites. The classifier's own consent
//     decisions count too: an assignment or return of WorkflowConsentRequired
//     in workflow_approval_policy.go is a demand point keyed by its governing
//     branch, while constant declarations, comparisons, and case-list
//     validation reads are classified as reads. Comparison reads of the same
//     names are classified as reads: reported in diagnostics, never points.
//
// Every census entry maps to exactly one contracts/conflict-rules.v1.json
// approval_points row through its owner triple {path,symbol,branch}, and
// every row must match a census entry, so set equality catches unlisted new
// branches and stale owners. The manifest axis is checked the same way: every
// gated tool-surface operation must be inventoried through its own row, and
// every conditional operation must map to the runtime planner the dispatch
// switch names, whose planner or source-delegated helper must carry an
// inventoried demand and its matching row. A census
// entry named in approvalInventoryAliases or
// approvalInventoryRetainedDeclarations is an explicit non-point,
// same-decision, or retained-unresolved mapping with a stated reason and an
// anchor; unmatched entries fail. Coverage is exact, so owner fields may not
// claim wildcards. The factored coverage checks run identically for the real
// catalog and for the synthetic omission controls, so an unlisted branch,
// caller, or caller-registered definition fails the same equality the real
// table must satisfy.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

const (
	approvalInventoryTablePath    = "../../contracts/conflict-rules.v1.json"
	approvalInventoryManifestPath = "../../contracts/agent-tool-surface.v1.json"
)

// approvalInventorySourceDirs are the owning packages the census walks,
// relative to this package's directory.
var approvalInventorySourceDirs = []string{"../../internal/store", "../../internal/agent"}

// approvalInventoryCalleeNames are the consent challenge, receipt
// consumption, authority-identity, and law-admission entry points whose call
// sites are inventory points.
var approvalInventoryCalleeNames = map[string]bool{
	"approvalChallengeEnvelopeTx":            true,
	"CreateApprovalChallengeTx":              true,
	"consumeApprovalTx":                      true,
	"ValidateAndConsumeApprovalTx":           true,
	"ConsumeApprovalTx":                      true,
	"workflowOperatorFromConsumedApprovalTx": true,
	"authorizeWorkflowOperatorApprovalTx":    true,
	"validateWorkflowContractApprovalLaw":    true,
	"validateDerivedLawModification":         true,
	"refuseNonDerivedLawModification":        true,
}

// approvalInventoryMetadataIdents are the consent-metadata identifiers whose
// declaration and return occurrences are inventory points.
var approvalInventoryMetadataIdents = map[string]bool{
	"ActionApprovalRequired":   true,
	"WorkflowOperatorRequired": true,
	"RequireOperatorIdentity":  true,
}

// approvalInventoryDemandFlags are the consent flags whose assignments are
// demand points. A struct field, local, or selector with one of these names
// assigned anything except the literal false demands consent.
var approvalInventoryDemandFlags = map[string]bool{
	"ApprovalRequired": true,
	"requiresApproval": true,
	"approvalRequired": true,
}

// approvalInventoryReceiptGuardIdents are the operator receipt references
// whose presence guards a store write: every comparison against them is a
// consent gate, not a read.
var approvalInventoryReceiptGuardIdents = map[string]bool{
	"OperatorApprovalRef": true,
}

// approvalInventoryAliases map census entries to non-points or to decisions
// inventoried at their anchored row. The design binds a caller's actor
// identity and a layered receipt recheck to no new decision, and a vocabulary
// constant decides nothing; challenge and consumption halves anchor to the
// decision row they serve instead of inflating the gap count.
var approvalInventoryAliases = map[string]string{
	"internal/store/workflow_registry.go\x00ActionApprovalRequired\x00ActionApprovalRequired":                                                                                        "vocabulary constant; names a class, decides nothing",
	"internal/store/workflow_evaluation.go\x00WorkflowOperatorRequired\x00WorkflowOperatorRequired":                                                                                  "vocabulary constant; names a class, decides nothing",
	"internal/agent/authority.go\x00ApprovalCheck\x00ApprovalCheck":                                                                                                                  "identity/receipt-integrity input declaration; the consume boundary at its caller is the point",
	"internal/store/workflow_approval_binding.go\x00workflowOperatorFromConsumedApprovalTx\x00operation requires a consumed operator approval":                                       "layered recheck of one already consumed receipt; no new decision",
	"internal/store/workflow_approval_binding.go\x00workflowOperatorFromConsumedApprovalTx\x00operation requires a consumed one-use operator approval":                               "layered recheck of one already consumed receipt; no new decision",
	"internal/agent/mutations.go\x00workflowActionMutation.authorizeTx\x00RequireOperatorIdentity":                                                                                   "identity/receipt-integrity parameter of the consume call anchored to workflow-action-consent-demand",
	"internal/agent/mutations.go\x00workflowActionMutation.execute\x00requires-approval":                                                                                             "copies the already classified consent/takeover answer into the transaction receiver; same decisions anchored to workflow-action-consent-demand and workflow-action-takeover-demand at workflowActionMutation.execute",
	"internal/store/workflow_outcome.go\x00validateDecisionRecord\x00question":                                                                                                       "audited operator-evidence consumer of the decision record (CD-0175); not independently minted consent",
	"internal/store/workflow.go\x00validateWorkflowContractApprovalLaw\x00validateDerivedLawModification":                                                                            "same derived-tier admissibility decision anchored to law-admission-6",
	"internal/store/workflow.go\x00validateWorkflowContractApprovalPolicy\x00validateWorkflowContractApprovalLaw":                                                                    "same derived-tier admissibility decision anchored to law-admission-6",
	"internal/store/workflow.go\x00foldSuppliedWorkflowSuccessorTx\x00validateDerivedLawModification":                                                                                "same derived-tier admissibility decision anchored to law-admission-6",
	"internal/store/law_boundary.go\x00validateDerivedLawModificationAtHome\x00refuseNonDerivedLawModification":                                                                      "same derived-tier admissibility decision anchored to law-admission-6",
	"internal/store/law_boundary.go\x00validateDerivedLawModificationAcrossSources\x00refuseNonDerivedLawModification":                                                               "same derived-tier admissibility decision anchored to law-admission-6",
	"internal/agent/mutations.go\x00runtime.workflowActionChallenge\x00CreateApprovalChallengeTx":                                                                                    "challenge half of the workflow-action consent anchored to workflow-action-consent-demand",
	"internal/agent/mutations.go\x00workflowActionMutation.authorizeTx\x00consumeApprovalTx":                                                                                         "receipt half of the workflow-action consent anchored to workflow-action-consent-demand",
	"internal/agent/mutations.go\x00runtime.retireResearchTx\x00approvalChallengeEnvelopeTx":                                                                                         "challenge half of cross-Product retirement consent anchored to research-retire-consume",
	"internal/agent/mutations.go\x00productProjectLinkMutation.applyTx\x00CreateApprovalChallengeTx":                                                                                 "challenge half anchored to manifest:concord_work_relate.product_project_add",
	"internal/agent/mutations.go\x00productProjectLinkMutation.applyTx\x00consumeApprovalTx":                                                                                         "receipt half anchored to manifest:concord_work_relate.product_project_add",
	"internal/agent/mutations.go\x00clientPolicyGrantMutation.applyTx\x00CreateApprovalChallengeTx":                                                                                  "challenge half anchored to manifest:concord_work_relate.client_policy_grant_request",
	"internal/agent/mutations.go\x00clientPolicyGrantMutation.applyTx\x00consumeApprovalTx":                                                                                          "receipt half anchored to manifest:concord_work_relate.client_policy_grant_request",
	"internal/agent/mutations.go\x00compactionMutation.publish\x00CreateApprovalChallengeTx":                                                                                         "challenge half anchored to manifest:concord_work_compact.publish",
	"internal/agent/mutations.go\x00compactionMutation.publish\x00consumeApprovalTx":                                                                                                 "receipt half anchored to manifest:concord_work_compact.publish",
	"internal/agent/mutations.go\x00mutationExecution.admitTx\x00approvalChallengeEnvelopeTx":                                                                                        "generic challenge half of the manifest plan consent anchored to execute-mutation-manifest-inherited-demand",
	"internal/agent/mutations.go\x00mutationExecution.admitTx\x00consumeApprovalTx":                                                                                                  "generic receipt half of the manifest plan consent anchored to execute-mutation-manifest-inherited-demand",
	"internal/agent/mutations.go\x00runtime.approvalChallengeEnvelopeTx\x00CreateApprovalChallengeTx":                                                                                "the single challenge creation path anchored to challenge-envelope-core-approval",
	"internal/agent/authority.go\x00Service.AcceptHostApprovalTx\x00ValidateAndConsumeApprovalTx":                                                                                    "receipt-integrity recheck anchored to the approval exchange decision at mutationExecution.executeTx",
	"internal/agent/authority.go\x00Service.consumeChallengeApprovalTx\x00ValidateAndConsumeApprovalTx":                                                                              "receipt-integrity recheck anchored to the approval exchange decision at mutationExecution.executeTx",
	"internal/agent/authority.go\x00Service.ValidateAndConsumeApprovalTx\x00ConsumeApprovalTx":                                                                                       "one-use consumption before the event exists; integrity layer of the exchange decision at mutationExecution.executeTx",
	"internal/store/outside_repair.go\x00applyOutsideRepairMutationTx\x00workflowOperatorFromConsumedApprovalTx":                                                                     "live admission half anchored to outside-repair-demand",
	"internal/store/outside_repair.go\x00validateOutsideRepairFold\x00authorizeWorkflowOperatorApprovalTx":                                                                           "fold recheck anchored to outside-repair-demand",
	"internal/store/workflow.go\x00requireWorkflowDuplicateRecoveryApprovalTx\x00authorizeWorkflowOperatorApprovalTx":                                                                "fold recheck anchored to registry-supersede-contract",
	"internal/store/workflow_domain_overlap.go\x00ResolveWorkflowDomainOverlapTx\x00workflowOperatorFromConsumedApprovalTx":                                                          "live admission half anchored to resolve-overlap-destructive-demand",
	"internal/store/work_size_override.go\x00OverrideWorkSizeTx\x00workflowOperatorFromConsumedApprovalTx":                                                                           "live admission half anchored to revise-intent-operator-size-demand",
	"internal/store/work_size_override.go\x00foldWorkSizeOverridden\x00authorizeWorkflowOperatorApprovalTx":                                                                          "fold recheck anchored to revise-intent-operator-size-demand",
	"internal/store/workflow_completion.go\x00workflowPremiseConfirmationAuthority\x00workflow premise confirmation is not operator typed#2":                                         "the consequence_v1 decision-bearing arm of the same operator-typed premise check anchored to completion-premise-not-typed",
	"internal/store/workflow_approval_policy.go\x00classifyLegacyWorkflowApproval\x00operator-required":                                                                              "classified consent source of the same decision anchored to workflow-action-consent-demand",
	"internal/store/workflow_approval_policy.go\x00classifyConsequenceV1WorkflowApproval\x00destructive":                                                                             "classified consent source of the same decision anchored to workflow-action-consent-demand",
	"internal/store/workflow_approval_policy.go\x00classifyConsequenceV1Decision\x00classifyConsequenceV1Decision":                                                                   "classified consent source of the same decision anchored to workflow-action-consent-demand",
	"internal/store/workflow_approval_policy.go\x00classifyConsequenceV1Decision\x00domains":                                                                                         "approve_contract arm of the serious law-change consent anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00bindWorkflowLawChangeApprovalTx\x00workflowOperatorFromConsumedApprovalTx":                                                             "live admission half anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00validateLiveWorkflowLawChangeTx\x00workflowOperatorFromConsumedApprovalTx":                                                             "live fold recheck anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00validateWorkflowLawChangeApproval\x00a serious law change requires the consumed operator approval binding":                             "fold shape recheck anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00checkWorkflowLawChangeEnactmentTx\x00every law change at crucial size requires the operator's approval of its exact text (CD-0230 D4)": "completion recheck of the crucial-size arm anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00checkWorkflowLawChangeEnactmentTx\x00checkWorkflowLawChangeEnactmentTx":                                                                "completion recheck of the enacted Domain count anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00checkWorkflowLawChangePinsEnactedTx\x00checkWorkflowLawChangePinsEnactedTx":                                                            "completion recheck of the approved law text anchored to serious-law-change-consent",
	"internal/store/workflow_law_change.go\x00checkWorkflowLawChangePinsEnactedTx\x00checkWorkflowLawChangePinsEnactedTx#2":                                                          "completion recheck of the approved registry anchored to serious-law-change-consent",
}

// approvalInventoryRetainedDeclarations witness retained consent declarations
// that resolve no registered ActionDefinitions triple today. The exemption is
// evidence-bound: a witnessed row may not claim live or caller_registered
// reachability, and the forward coverage check still refuses any triple the
// declaration ever produces, so the witness cannot hide newly reachable
// consent and is no excluded-owner shortcut.
var approvalInventoryRetainedDeclarations = map[string]string{}

// approvalInventoryClasses is the closed D5 class vocabulary.
var approvalInventoryClasses = map[string]bool{
	"ui_ux":                         true,
	"product_preference":            true,
	"serious_law":                   true,
	"destructive_verified_recovery": true,
}

// approvalInventoryReachabilities is the closed reachability vocabulary.
// One owner serves many definition contexts, so a row lists every class its
// branch covers; live, legacy_pinned, and retired stay distinct, and
// caller-registered definitions name their own class.
var approvalInventoryReachabilities = map[string]bool{
	"live":              true,
	"legacy_pinned":     true,
	"retired":           true,
	"caller_registered": true,
}

// approvalInventoryTable mirrors the table slice this inventory consumes.
type approvalInventoryTable struct {
	CatalogGeneration int                         `json:"catalog_generation"`
	SchemaVersion     string                      `json:"schema_version"`
	ApprovalPoints    []approvalInventoryPointRow `json:"approval_points"`
}

type approvalInventoryPointRow struct {
	ID             string                 `json:"id"`
	Owner          approvalInventoryOwner `json:"owner"`
	HoldingLaw     *string                `json:"holding_law"`
	Reachability   []string               `json:"reachability"`
	Classification json.RawMessage        `json:"classification"`
	Evidence       []string               `json:"evidence,omitempty"`
}

// approvalInventoryNamesUnresolvedHolder reports whether a null-holding
// row's gap reason names the unresolved holder.
func approvalInventoryNamesUnresolvedHolder(row approvalInventoryPointRow) bool {
	var gap struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(row.Classification, &gap); err != nil {
		return false
	}
	return strings.Contains(gap.Reason, "unresolved holder")
}

// approvalInventoryRowIsGap reports whether the row's classification names a
// gap rather than a D5 class.
func approvalInventoryRowIsGap(row approvalInventoryPointRow) bool {
	var class struct {
		Class string `json:"class"`
	}
	if err := json.Unmarshal(row.Classification, &class); err == nil && class.Class != "" {
		return false
	}
	var gap struct {
		GapIssue string `json:"gap_issue"`
	}
	return json.Unmarshal(row.Classification, &gap) == nil && gap.GapIssue != ""
}

type approvalInventoryOwner struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
	Branch string `json:"branch"`
}

func (o approvalInventoryOwner) key() string { return o.Path + "\x00" + o.Symbol + "\x00" + o.Branch }

// approvalInventoryPoint is one census-derived entry.
type approvalInventoryPoint struct {
	Kind   string // consent-metadata | demand-assignment | guard-return | caller | receipt-guard | law-admission
	Owner  approvalInventoryOwner
	Detail string // file:line for the failure report
}

type approvalInventoryManifestOperation struct {
	ID       string `json:"id"`
	Approval string `json:"approval"`
}

// censusVisitor carries the source context an occurrence needs to be
// classified. ast.Walk calls Visit(nil) after each visited node's children,
// which restores the frame pushed on entry, so context is always balanced.
type censusVisitor struct {
	fset            *token.FileSet
	relPath         string
	points          []approvalInventoryPoint
	reads           []string
	funcName        string
	mapKey          string
	compID          string
	branchToken     string
	storeAlias      string
	resolutionKinds map[string]string
	manifestGated   []approvalInventoryManifestOperation
	inBinary        int
	inSel           int
	inCall          int
	inCaseList      int
	ordinal         map[string]int
	stack           []func()
}

func (v *censusVisitor) push(restore func()) {
	v.stack = append(v.stack, restore)
}

func (v *censusVisitor) pop() {
	if len(v.stack) == 0 {
		return
	}
	v.stack[len(v.stack)-1]()
	v.stack = v.stack[:len(v.stack)-1]
}

// point records one census entry. Repeated identical sites in one function
// keep distinct keys through ordinals; everything else keys on stable
// structural identity so a reordered branch cannot silently inherit a
// neighbor's row.
func (v *censusVisitor) point(pos token.Pos, kind, base string) {
	key := v.funcName + "\x00" + kind + "\x00" + base
	v.ordinal[key]++
	display := base
	if display == "" {
		display = v.funcName
	}
	if v.ordinal[key] > 1 {
		display = fmt.Sprintf("%s#%d", display, v.ordinal[key])
	}
	line := v.fset.Position(pos).Line
	v.points = append(v.points, approvalInventoryPoint{Kind: kind, Owner: approvalInventoryOwner{
		Path: v.relPath, Symbol: v.funcName, Branch: display,
	}, Detail: fmt.Sprintf("%s:%d", v.relPath, line)})
}

func (v *censusVisitor) read(pos token.Pos, what string) {
	v.reads = append(v.reads, fmt.Sprintf("%s:%d %s", v.relPath, v.fset.Position(pos).Line, what))
}

func approvalInventoryCalleeName(expr ast.Expr) string {
	switch fun := expr.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

func approvalInventoryStringLit(expr ast.Expr) (string, bool) {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return strings.Trim(lit.Value, "`\""), true
	}
	return "", false
}

func approvalInventoryIdentName(expr ast.Expr) (string, bool) {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name, true
	}
	return "", false
}

func approvalInventoryIsFalseLiteral(expr ast.Expr) bool {
	name, ok := approvalInventoryIdentName(expr)
	return ok && name == "false"
}

// approvalInventoryDisjuncts splits an OR chain into its operand expressions.
func approvalInventoryDisjuncts(expr ast.Expr) []ast.Expr {
	if binary, ok := expr.(*ast.BinaryExpr); ok && binary.Op == token.LOR {
		return append(approvalInventoryDisjuncts(binary.X), approvalInventoryDisjuncts(binary.Y)...)
	}
	return []ast.Expr{expr}
}

// approvalInventoryBranchToken derives a stable branch identity from a
// disjunct or condition expression: the first selector field it names that is
// not the receipt reference itself, else the first identifier, hyphenated.
// Equality comparisons key on their right-hand side, so the demanded
// authority names the branch.
func approvalInventoryBranchToken(expr ast.Expr) string {
	if binary, ok := expr.(*ast.BinaryExpr); ok && (binary.Op == token.EQL || binary.Op == token.NEQ) {
		if token := approvalInventoryBranchToken(binary.Y); token != "" {
			return token
		}
	}
	var field, ident, literal string
	ast.Inspect(expr, func(node ast.Node) bool {
		switch named := node.(type) {
		case *ast.SelectorExpr:
			if field == "" && !approvalInventoryReceiptGuardIdents[named.Sel.Name] {
				field = named.Sel.Name
			}
		case *ast.Ident:
			if ident == "" && named.Name != "nil" && named.Name != "true" && named.Name != "false" && named.Name != "len" {
				ident = named.Name
			}
		case *ast.BasicLit:
			if literal == "" && named.Kind == token.STRING {
				literal = strings.Trim(named.Value, "`\"")
			}
		}
		return field == ""
	})
	chosen := field
	if chosen == "" {
		chosen = ident
	}
	if chosen == "" {
		chosen = literal
	}
	var builder strings.Builder
	previousLower := false
	for _, character := range chosen {
		upper := character >= 'A' && character <= 'Z'
		if upper && previousLower {
			builder.WriteByte('-')
		}
		if upper {
			builder.WriteRune(character - 'A' + 'a')
		} else {
			builder.WriteRune(character)
		}
		previousLower = !upper
	}
	return builder.String()
}

// Visit classifies one node and returns the visitor to descend with. A nil
// node is the post-children signal: restore the frame pushed on entry.
func (v *censusVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		v.pop()
		return nil
	}
	switch n := node.(type) {
	case *ast.File:
		v.storeAlias = approvalInventoryStoreAlias(n)
		v.push(func() {})
	case *ast.FuncDecl:
		name := n.Name.Name
		if recv := approvalInventoryReceiverType(n); recv != "" {
			name = recv + "." + name
		}
		savedFunc, savedOrdinal := v.funcName, v.ordinal
		v.funcName, v.ordinal = name, map[string]int{}
		v.push(func() { v.funcName, v.ordinal = savedFunc, savedOrdinal })
	case *ast.GenDecl:
		saved := v.funcName
		v.push(func() { v.funcName = saved })
	case *ast.ValueSpec:
		saved := v.funcName
		if len(n.Names) > 0 {
			v.funcName = n.Names[0].Name
		}
		// A declared consent constant is its own point, named by its own
		// declared name. Names are classified here and not re-walked, so one
		// declaration yields exactly one entry.
		for _, name := range n.Names {
			if approvalInventoryMetadataIdents[name.Name] {
				v.point(name.Pos(), "consent-metadata", name.Name)
			}
		}
		v.push(func() { v.funcName = saved })
		for _, value := range n.Values {
			ast.Walk(v, value)
		}
		v.pop()
		return nil
	case *ast.TypeSpec:
		saved := v.funcName
		v.funcName = n.Name.Name
		v.push(func() { v.funcName = saved })
	case *ast.IfStmt:
		saved := v.branchToken
		if token := approvalInventoryBranchToken(n.Cond); token != "" {
			v.branchToken = token
		} else if assign, ok := n.Init.(*ast.AssignStmt); ok && len(assign.Rhs) > 0 {
			if token := approvalInventoryBranchToken(assign.Rhs[0]); token != "" {
				v.branchToken = token
			}
		}
		v.push(func() { v.branchToken = saved })
	case *ast.CaseClause:
		saved := v.branchToken
		for _, item := range n.List {
			if value, ok := approvalInventoryStringLit(item); ok {
				v.branchToken = value
				break
			}
			v.branchToken = approvalInventoryBranchToken(item)
			if v.branchToken != "" {
				break
			}
		}
		// The case list compares against vocabulary constants: those
		// occurrences are validation reads, not new decisions. The clause
		// body walks outside that context, so a consent assignment inside a
		// case arm stays a demand.
		savedList := v.inCaseList
		v.inCaseList++
		for _, item := range n.List {
			ast.Walk(v, item)
		}
		v.inCaseList = savedList
		v.push(func() { v.branchToken = saved })
		for _, statement := range n.Body {
			ast.Walk(v, statement)
		}
		v.pop()
		return nil
	case *ast.AssignStmt:
		// A consent flag assigned anything but literal false is a demand,
		// whether the predicate is computed or literal. The manifest plan
		// default expands over every gated tool-surface operation so no
		// manifest-wide row can claim wildcard coverage; resolution-kind
		// comparisons expand over the closed kind set; OR disjuncts split
		// into stable per-branch identities; a literal true keys on the
		// governing branch token.
		for i, lhs := range n.Lhs {
			name := ""
			switch assigned := lhs.(type) {
			case *ast.Ident:
				name = assigned.Name
			case *ast.SelectorExpr:
				name = assigned.Sel.Name
			}
			if !approvalInventoryDemandFlags[name] || i >= len(n.Rhs) {
				continue
			}
			if approvalInventoryIsFalseLiteral(n.Rhs[i]) {
				v.read(n.Rhs[i].Pos(), name+" cleared")
				continue
			}
			v.classifyDemand(n.Rhs[i], n.Pos())
		}
		v.push(func() {})
	case *ast.CallExpr:
		callee := approvalInventoryCalleeName(n.Fun)
		kindArg := false
		for _, arg := range n.Args {
			if name, ok := approvalInventoryIdentName(arg); ok && name == "KindApprovalRequired" {
				kindArg = true
			}
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Trim(lit.Value, "`\"") == "approval_required" {
				kindArg = true
			}
		}
		saved := v.inCall
		v.inCall++
		v.push(func() { v.inCall = saved })
		walkChildrenOnly(n, v)
		v.inCall = saved
		v.pop()
		detail := ""
		if len(n.Args) > 2 {
			detail, _ = approvalInventoryStringLit(n.Args[2])
		}
		switch {
		case approvalInventoryLawAdmissionCallee(callee):
			v.point(n.Pos(), "law-admission", callee)
		case approvalInventoryCalleeNames[callee]:
			v.point(n.Pos(), "caller", callee)
		case kindArg:
			v.point(n.Pos(), "guard-return", detail)
		}
		return nil
	case *ast.KeyValueExpr:
		if key, ok := approvalInventoryStringLit(n.Key); ok {
			saved := v.mapKey
			v.mapKey = key
			v.push(func() { v.mapKey = saved })
			ast.Walk(v, n.Value)
			v.pop()
			return nil
		}
		// A metadata or consent key inside a composite literal is inventoried
		// under its own name, even when the literal is a call argument. The
		// manifest plan default is a composite key and expands like any
		// assignment.
		if name, ok := approvalInventoryIdentName(n.Key); ok {
			classified := false
			if approvalInventoryMetadataIdents[name] {
				v.point(n.Key.Pos(), "consent-metadata", name)
				classified = true
			} else if approvalInventoryDemandFlags[name] {
				classified = true
				if approvalInventoryIsFalseLiteral(n.Value) {
					v.read(n.Key.Pos(), name+" cleared")
				} else {
					v.classifyDemand(n.Value, n.Key.Pos())
				}
			}
			if classified {
				ast.Walk(v, n.Value)
				v.pop()
				return nil
			}
		}
		v.push(func() {})
	case *ast.CompositeLit:
		saved := v.compID
		for _, elt := range n.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "ID" {
					if value, ok := approvalInventoryStringLit(kv.Value); ok {
						v.compID = value
					}
				}
			}
		}
		v.push(func() { v.compID = saved })
	case *ast.BinaryExpr:
		saved := v.inBinary
		v.inBinary++
		v.push(func() { v.inBinary = saved })
	case *ast.SelectorExpr:
		// A package-qualified metadata reference resolves through the file's
		// imports: inside a comparison it is a read, elsewhere it is a point
		// like its same-package spelling.
		if name, ok := approvalInventoryIdentName(n.X); ok && name == v.storeAlias && approvalInventoryMetadataIdents[n.Sel.Name] {
			if v.inBinary > 0 {
				v.read(n.Sel.Pos(), n.Sel.Name+" read")
			} else {
				base := v.mapKey
				if base == "" {
					base = v.compID
				}
				v.point(n.Sel.Pos(), "consent-metadata", base)
			}
			return nil
		}
		saved := v.inSel
		v.inSel++
		v.push(func() { v.inSel = saved })
	case *ast.Ident:
		switch {
		case approvalInventoryReceiptGuardIdents[n.Name]:
			if v.inBinary > 0 {
				v.point(n.Pos(), "receipt-guard", v.branchToken)
			}
		case approvalInventoryMetadataIdents[n.Name]:
			switch {
			case v.inBinary > 0 || v.inSel > 0:
				v.read(n.Pos(), n.Name+" read")
			default:
				base := v.mapKey
				if base == "" {
					base = v.compID
				}
				v.point(n.Pos(), "consent-metadata", base)
			}
		case n.Name == "KindApprovalRequired":
			if v.inBinary > 0 && v.inCall == 0 {
				v.read(n.Pos(), "KindApprovalRequired comparison")
			}
			// Call-argument occurrences are classified at the call.
		case n.Name == "WorkflowConsentRequired":
			// A value assignment or return of the consent-required constant
			// is the classifier demanding consent: an inventory point keyed
			// by its governing branch. Comparisons, selector reads, call
			// arguments, and case-list validation reads are reads, never
			// new decisions.
			switch {
			case v.inBinary > 0 || v.inSel > 0 || v.inCall > 0 || v.inCaseList > 0:
				v.read(n.Pos(), n.Name+" read")
			default:
				base := v.branchToken
				if base == "" || base == "true" {
					base = v.funcName
				}
				v.point(n.Pos(), "demand-assignment", base)
			}
		case approvalInventoryDemandFlags[n.Name]:
			if v.inBinary > 0 || v.inSel > 0 || v.inCall > 0 {
				v.read(n.Pos(), n.Name+" read")
			}
			// Declarations and assignment targets are classified by their
			// own nodes, not here.
		}
		return nil
	case *ast.BasicLit:
		if n.Kind == token.STRING && strings.Trim(n.Value, "`\"") == "approval_required" && v.inCall == 0 {
			v.read(n.Pos(), "approval_required kind read")
		}
		return nil
	default:
		v.push(func() {})
	}
	return v
}

// walkChildrenOnly descends a call's Fun and arguments outside ast.Walk so
// the call-level classification runs after its children are visited.
func walkChildrenOnly(call *ast.CallExpr, v *censusVisitor) {
	ast.Walk(v, call.Fun)
	for _, arg := range call.Args {
		ast.Walk(v, arg)
	}
}

func approvalInventoryLawAdmissionCallee(callee string) bool {
	return callee == "validateWorkflowContractApprovalLaw" || callee == "validateDerivedLawModification" || callee == "refuseNonDerivedLawModification"
}

// classifyDemand records one demand site: the manifest plan default expands
// over every gated tool-surface operation, resolution-kind comparisons
// expand over the closed kind set, and OR disjuncts split into stable
// per-branch identities; a literal true keys on the governing branch token.
func (v *censusVisitor) classifyDemand(rhs ast.Expr, pos token.Pos) {
	if operations := v.manifestApprovalExpansion(rhs); operations != nil {
		for _, operation := range operations {
			v.point(pos, "demand-assignment", "manifest:"+operation.ID)
		}
		return
	}
	if kinds := v.resolutionKindExpansion(rhs); kinds != nil {
		for _, kind := range kinds {
			v.point(pos, "demand-assignment", "overlap-resolution:"+kind)
		}
		return
	}
	for _, disjunct := range approvalInventoryDisjuncts(rhs) {
		base := approvalInventoryBranchToken(disjunct)
		if base == "" || base == "true" {
			base = v.branchToken
		}
		v.point(pos, "demand-assignment", base)
	}
}

// manifestApprovalExpansion detects the manifest plan default: a demand
// assigned from the operation's declared approval class. It returns the
// gated manifest operations, or nil when the expression is not that
// assignment.
func (v *censusVisitor) manifestApprovalExpansion(rhs ast.Expr) []approvalInventoryManifestOperation {
	if len(v.manifestGated) == 0 {
		return nil
	}
	binary, ok := rhs.(*ast.BinaryExpr)
	if !ok || binary.Op != token.EQL {
		return nil
	}
	selector, ok := binary.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Approval" {
		return nil
	}
	call, ok := binary.Y.(*ast.CallExpr)
	if !ok || approvalInventoryCalleeName(call.Fun) != "ApprovalClass" || len(call.Args) != 1 {
		return nil
	}
	class, ok := approvalInventoryStringLit(call.Args[0])
	if !ok || class != "required" {
		return nil
	}
	required := make([]approvalInventoryManifestOperation, 0, len(v.manifestGated))
	for _, operation := range v.manifestGated {
		if operation.Approval == "required" {
			required = append(required, operation)
		}
	}
	return required
}

// resolutionKindExpansion detects a resolution-kind comparison and returns
// the consent kinds it demands, or nil when the expression is not one.
func (v *censusVisitor) resolutionKindExpansion(rhs ast.Expr) []string {
	binary, ok := rhs.(*ast.BinaryExpr)
	if !ok || binary.Op != token.NEQ || len(v.resolutionKinds) == 0 {
		return nil
	}
	var ident *ast.Ident
	switch left := binary.X.(type) {
	case *ast.Ident:
		ident = left
	case *ast.SelectorExpr:
		ident = left.Sel
	}
	if ident == nil || ident.Name != "ResolutionKind" {
		return nil
	}
	excluded := ""
	if value, ok := approvalInventoryStringLit(binary.Y); ok {
		excluded = value
	} else if name, ok := approvalInventoryIdentName(binary.Y); ok {
		excluded = v.resolutionKinds[name]
	}
	if excluded == "" {
		return nil
	}
	values := make([]string, 0, len(v.resolutionKinds))
	for _, value := range v.resolutionKinds {
		if value != excluded {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func approvalInventoryStoreAlias(file *ast.File) string {
	for _, importSpec := range file.Imports {
		path := strings.Trim(importSpec.Path.Value, "\"`")
		if strings.HasSuffix(path, "/internal/store") {
			if importSpec.Name != nil {
				return importSpec.Name.Name
			}
			return "store"
		}
	}
	return ""
}

func approvalInventoryReceiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch typ := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := typ.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return typ.Name
	}
	return ""
}

// approvalInventoryResolutionKinds parses the closed resolution-kind const
// block from the store overlap owner.
func approvalInventoryResolutionKinds(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("../../internal/store", "workflow_domain_overlap.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse resolution kinds: %v", err)
	}
	kinds := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "Resolution") || index >= len(spec.Values) {
				continue
			}
			if value, ok := approvalInventoryStringLit(spec.Values[index]); ok {
				kinds[name.Name] = value
			}
		}
		return true
	})
	if len(kinds) == 0 {
		t.Fatal("no resolution kinds found; the overlap demand axis is broken")
	}
	return kinds
}

// approvalInventoryGatedManifestOperations decodes the tool-surface manifest
// and returns every operation whose approval is required or conditional.
func approvalInventoryGatedManifestOperations(t *testing.T) []approvalInventoryManifestOperation {
	t.Helper()
	raw, err := os.ReadFile(approvalInventoryManifestPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v", approvalInventoryManifestPath, err)
	}
	var manifest struct {
		Operations []approvalInventoryManifestOperation `json:"operations"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("cannot decode %s: %v", approvalInventoryManifestPath, err)
	}
	var gated []approvalInventoryManifestOperation
	for _, operation := range manifest.Operations {
		if operation.Approval == "required" || operation.Approval == "conditional" {
			gated = append(gated, operation)
		}
	}
	if len(gated) == 0 {
		t.Fatal("manifest names no gated operations; the manifest axis is broken")
	}
	return gated
}

// approvalInventoryManifestConditionalPlanners parses the runtime dispatch
// and maps each operation id to its planner and source-linked helpers: case
// clauses name the operation id, and if-guarded dispatches name the
// operation id or its tool before calling exactly one planner.
func approvalInventoryManifestConditionalPlanners(t *testing.T, gated []approvalInventoryManifestOperation) map[string][]string {
	t.Helper()
	path := filepath.Join("../../internal/agent", "mutations.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse the mutation dispatch: %v", err)
	}
	return approvalInventoryConditionalPlannersFromAST(file, gated)
}

func approvalInventoryConditionalPlannersFromAST(file *ast.File, gated []approvalInventoryManifestOperation) map[string][]string {
	planners := map[string]string{}
	plannerName := func(call *ast.CallExpr) string {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if name := selector.Sel.Name; strings.HasPrefix(name, "plan") || strings.HasPrefix(name, "mutate") {
			return name
		}
		return ""
	}
	ast.Inspect(file, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok {
			return true
		}
		operation := ""
		for _, item := range clause.List {
			if value, ok := approvalInventoryStringLit(item); ok && strings.Contains(value, ".") {
				operation = value
				break
			}
		}
		if operation == "" {
			return true
		}
		ast.Inspect(clause, func(inner ast.Node) bool {
			if call, ok := inner.(*ast.CallExpr); ok {
				if name := plannerName(call); name != "" {
					planners[operation] = name
				}
			}
			return true
		})
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		literals := map[string]bool{}
		ast.Inspect(branch.Cond, func(inner ast.Node) bool {
			if basic, ok := inner.(*ast.BasicLit); ok {
				if value, ok := approvalInventoryStringLit(basic); ok {
					literals[value] = true
				}
			}
			return true
		})
		planner := ""
		ast.Inspect(branch.Body, func(inner ast.Node) bool {
			if call, ok := inner.(*ast.CallExpr); ok {
				planner = plannerName(call)
			}
			return true
		})
		if planner == "" {
			return true
		}
		for _, operation := range gated {
			if literals[operation.ID] || literals[strings.Split(operation.ID, ".")[0]] {
				if _, mapped := planners[operation.ID]; !mapped {
					planners[operation.ID] = planner
				}
			}
		}
		return true
	})
	return approvalInventoryResolvePlannerOwners(file, planners)
}

// Only returned calls on the declared receiver or a locally constructed
// receiver establish delegation. A matching method name elsewhere, a field
// read, or a callback does not establish an ownership edge. The wrapper's
// branches stay one planner scope, including publication and reconciliation.
func approvalInventoryResolvePlannerOwners(file *ast.File, planners map[string]string) map[string][]string {
	functions := map[string]*ast.FuncDecl{}
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok {
			name := fn.Name.Name
			if receiver := approvalInventoryReceiverType(fn); receiver != "" {
				name = receiver + "." + name
			}
			functions[name] = fn
		}
	}
	edges := map[string][]string{}
	for name, fn := range functions {
		if fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if _, callback := node.(*ast.FuncLit); callback {
				return false
			}
			returned, ok := node.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for _, result := range returned.Results {
				call, ok := result.(*ast.CallExpr)
				if !ok {
					continue
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				receiver, ok := selector.X.(*ast.Ident)
				if !ok || receiver.Obj == nil {
					continue
				}
				var typ ast.Expr
				switch declaration := receiver.Obj.Decl.(type) {
				case *ast.Field:
					// Parameters are not locally constructed delegation receivers.
					if fn.Recv != nil && len(fn.Recv.List) == 1 && fn.Recv.List[0] == declaration {
						typ = declaration.Type
					}
				case *ast.AssignStmt:
					if declaration.Tok == token.DEFINE && len(declaration.Lhs) == 1 && len(declaration.Rhs) == 1 {
						if literal, ok := declaration.Rhs[0].(*ast.CompositeLit); ok {
							typ = literal.Type
						}
					}
				}
				if pointer, ok := typ.(*ast.StarExpr); ok {
					typ = pointer.X
				}
				if ident, ok := typ.(*ast.Ident); ok {
					target := ident.Name + "." + selector.Sel.Name
					if functions[target] != nil {
						edges[name] = append(edges[name], target)
					}
				}
			}
			return false
		})
	}
	owners := map[string][]string{}
	for operation, planner := range planners {
		pending := []string{"runtime." + planner}
		seen := map[string]bool{}
		for len(pending) > 0 {
			owner := pending[0]
			pending = pending[1:]
			if seen[owner] || functions[owner] == nil {
				continue
			}
			seen[owner] = true
			owners[operation] = append(owners[operation], owner)
			pending = append(pending, edges[owner]...)
		}
	}
	return owners
}

// approvalInventoryCensus parses the production sources and returns the
// consent entries and the non-point reads it finds.
func approvalInventoryCensus(t *testing.T, gated []approvalInventoryManifestOperation) (points []approvalInventoryPoint, reads []string) {
	t.Helper()
	resolutionKinds := approvalInventoryResolutionKinds(t)
	for _, dir := range approvalInventorySourceDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("cannot read census source directory %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("cannot read census source %s: %v", path, err)
			}
			if strings.Contains(string(raw), "Code generated") && strings.Contains(string(raw), "DO NOT EDIT") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, raw, 0)
			if err != nil {
				t.Fatalf("cannot parse census source %s: %v", path, err)
			}
			visitor := &censusVisitor{fset: fset, relPath: strings.TrimPrefix(filepath.ToSlash(path), "../../"), ordinal: map[string]int{}, resolutionKinds: resolutionKinds, manifestGated: gated}
			ast.Walk(visitor, file)
			points = append(points, visitor.points...)
			reads = append(reads, visitor.reads...)
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Owner.key() < points[j].Owner.key() })
	return points, reads
}

func approvalInventoryPointKeys(points []approvalInventoryPoint) []string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(points))
	for _, point := range points {
		key := point.Owner.key()
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func approvalInventoryLoadTable(t *testing.T) approvalInventoryTable {
	t.Helper()
	raw, err := os.ReadFile(approvalInventoryTablePath)
	if err != nil {
		t.Fatalf("cannot read %s: %v", approvalInventoryTablePath, err)
	}
	var table approvalInventoryTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("cannot decode %s: %v", approvalInventoryTablePath, err)
	}
	return table
}

func approvalInventoryFormatPoints(points []approvalInventoryPoint) string {
	formatted := make([]string, 0)
	for _, key := range approvalInventoryPointKeys(points) {
		formatted = append(formatted, strings.ReplaceAll(key, "\x00", "/"))
	}
	return strings.Join(formatted, "; ")
}

// TestOperatorApprovalInventoryCensusDetectsSyntheticBranch proves the census
// catches an unlisted new consent branch and that the SAME coverage check the
// real table satisfies fails while the synthetic entry is uncovered.
func TestOperatorApprovalInventoryCensusDetectsSyntheticBranch(t *testing.T) {
	source := `package store

import (
	"context"

	store2 "github.com/sharper-flow/concord/internal/store"
)

func syntheticConsentBranch(ctx context.Context) error {
	requiresApproval := action.Approval == store2.ActionApprovalRequired || retryApproval
	if requiresApproval {
		plan.requiresApproval = true
	}
	switch {
	case requiresApproval:
		plan2.requiresApproval = true
	}
	if syntheticClearance {
		plan3.requiresApproval = false
	}
	authority := store2.WorkflowOperatorRequired
	_ = authority
	return newFailure(KindApprovalRequired, "synthetic", "synthetic consent branch must be inventoried", false, "request_approval")
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("cannot write synthetic branch source: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse synthetic branch source: %v", err)
	}
	visitor := &censusVisitor{fset: fset, relPath: "synthetic.go", ordinal: map[string]int{}, resolutionKinds: map[string]string{"ResolutionDependsOn": "depends_on", "ResolutionBlocks": "blocks"}}
	visitor.storeAlias = "store2"
	ast.Walk(visitor, file)
	var qualified bool
	var demand []string
	var guard bool
	for _, point := range visitor.points {
		if point.Kind == "consent-metadata" && point.Owner.Path == "synthetic.go" {
			qualified = true
		}
		if point.Owner.Symbol != "syntheticConsentBranch" {
			continue
		}
		if point.Kind == "demand-assignment" {
			demand = append(demand, point.Owner.Branch)
		}
		if point.Kind == "guard-return" && strings.Contains(point.Owner.Branch, "synthetic consent branch must be inventoried") {
			guard = true
		}
	}
	// The package-qualified return-level reference is a point through the
	// file's imports, the computed and case-guarded demands carry their own
	// branch identities, and the guard return keeps its message identity.
	if !qualified || len(demand) != 4 || demand[0] != "action-approval-required" || demand[1] != "retry-approval" || !guard {
		t.Fatalf("census misclassified the synthetic consent branch: qualified=%v demand=%v guard=%v points=%v", qualified, demand, guard, visitor.points)
	}
	// A literal false assignment clears the consent flag: it contributes no
	// consent point and is recorded as a read.
	for _, branch := range demand {
		if branch == "synthetic-clearance" {
			t.Fatalf("the literal false assignment was counted as a consent demand: %v", demand)
		}
	}
	cleared := false
	for _, read := range visitor.reads {
		if strings.Contains(read, "requiresApproval cleared") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("the literal false assignment recorded no cleared read: %v", visitor.reads)
	}
	// The same coverage check the real table satisfies must fail while the
	// synthetic entries are uncovered.
	table := approvalInventoryLoadTable(t)
	problems := approvalInventoryCoverageProblems(visitor.points, table.ApprovalPoints, nil, nil)
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "synthetic.go/syntheticConsentBranch") {
		t.Fatalf("coverage check did not fail for the synthetic branch: %v", problems)
	}
}

// TestOperatorApprovalInventoryCensusDetectsClassifierConsent proves the
// census classifies the classifier's own consent decisions as demand points
// keyed by their governing branch, records the constant's validation and
// comparison occurrences as reads, and that the SAME coverage check the real
// table satisfies fails while a new classified consent branch is uncovered.
func TestOperatorApprovalInventoryCensusDetectsClassifierConsent(t *testing.T) {
	source := `package store

func syntheticClassifierConsent(facts WorkflowApprovalFacts) WorkflowApprovalPolicyResult {
	consent := WorkflowConsentNone
	switch consent {
	case WorkflowConsentNone, WorkflowConsentConditional, WorkflowConsentRequired:
	}
	if facts.OperatorRequired {
		consent = WorkflowConsentRequired
	}
	if facts.Destructive != "" {
		return WorkflowApprovalPolicyResult{Consent: WorkflowConsentRequired}
	}
	if consent == WorkflowConsentRequired {
		return WorkflowApprovalPolicyResult{Consent: WorkflowConsentRequired}
	}
	return WorkflowApprovalPolicyResult{Consent: WorkflowConsentRequired}
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic_classifier.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("cannot write synthetic classifier source: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("cannot parse synthetic classifier source: %v", err)
	}
	visitor := &censusVisitor{fset: fset, relPath: "synthetic_classifier.go", ordinal: map[string]int{}}
	ast.Walk(visitor, file)
	var demand []string
	var consentReads int
	for _, point := range visitor.points {
		if point.Owner.Symbol != "syntheticClassifierConsent" || point.Kind != "demand-assignment" {
			continue
		}
		demand = append(demand, point.Owner.Branch)
	}
	for _, read := range visitor.reads {
		if strings.Contains(read, "WorkflowConsentRequired read") {
			consentReads++
		}
	}
	// Each consent decision keys on its governing branch; the case-list
	// validation read and the comparison read stay reads.
	want := []string{"operator-required", "destructive", "workflow-consent-required", "syntheticClassifierConsent"}
	if !slices.Equal(demand, want) || consentReads != 2 {
		t.Fatalf("census misclassified the synthetic classifier consent: demand=%v consentReads=%d points=%v reads=%v", demand, consentReads, visitor.points, visitor.reads)
	}
	// The same coverage check the real table satisfies must fail while the
	// new classified consent branches are uncovered.
	table := approvalInventoryLoadTable(t)
	problems := approvalInventoryCoverageProblems(visitor.points, table.ApprovalPoints, nil, nil)
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "synthetic_classifier.go/syntheticClassifierConsent") {
		t.Fatalf("coverage check did not fail for the synthetic classifier consent: %v", problems)
	}
}

// approvalInventoryTriple is one current definition/action identity whose
// resolved action demands operator approval.
type approvalInventoryTriple struct {
	Ref    string
	Action string
}

func (t approvalInventoryTriple) key() string {
	return fmt.Sprintf("%s:%s", t.Ref, t.Action)
}

// approvalInventoryExpansion walks resolved definitions and the off-pin
// recovery resolution, returning every approval-required triple.
func approvalInventoryExpansion(definitions []WorkflowDefinition) []approvalInventoryTriple {
	triples := make([]approvalInventoryTriple, 0)
	for _, definition := range definitions {
		for _, action := range definition.ActionDefinitions {
			if action.Approval == ActionApprovalRequired {
				triples = append(triples, approvalInventoryTriple{Ref: definition.Ref, Action: action.ID})
			}
		}
		// Recovery actions resolve off-pin: every builtin recovery action
		// definition answers for any registered definition, so its
		// approval-required actions reach every version.
		for _, recovery := range BuiltinWorkflowRecoveryActionDefinitions() {
			if recovery.Approval == ActionApprovalRequired {
				triples = append(triples, approvalInventoryTriple{Ref: definition.Ref, Action: recovery.ID})
			}
		}
	}
	sort.Slice(triples, func(i, j int) bool {
		if triples[i].Ref != triples[j].Ref {
			return triples[i].Ref < triples[j].Ref
		}
		return triples[i].Action < triples[j].Action
	})
	return triples
}

// TestOperatorApprovalInventoryRegistryExpansionCatchesNewDefinition proves
// the registered-definition expansion catches a caller-registered definition
// that carries an approval-required action, and that the SAME triple
// coverage check the real table satisfies fails while it is uncovered. The
// lookup after registration is the behavioral witness that caller
// registries resolve approval metadata, which is the caller_registered
// context.
func TestOperatorApprovalInventoryRegistryExpansionCatchesNewDefinition(t *testing.T) {
	definitions := BuiltinWorkflowDefinitions()
	if len(definitions) == 0 {
		t.Fatal("no live workflow definitions are registered")
	}
	// A live head registers under today's validation rules. A released v1
	// clone predates current definition duties and refuses fresh registration
	// for reasons a caller registry never introduces.
	synthetic := cloneWorkflowDefinition(definitions[0])
	synthetic.Ref = "workflow.synthetic_consent"
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(synthetic)
	if err != nil {
		t.Fatalf("synthetic caller-registered definition is not registrable: %v", err)
	}
	resolved, found := registry.Current(synthetic.Ref)
	if !found {
		t.Fatal("caller registry did not resolve the synthetic definition")
	}
	for _, action := range resolved.Definition.ActionDefinitions {
		if action.ID == "approve_contract" && action.Approval != ActionApprovalRequired {
			t.Fatalf("caller-registered resolution lost the approval metadata: %+v", action)
		}
	}
	_ = registered
	triples := approvalInventoryExpansion([]WorkflowDefinition{synthetic})
	found = false
	for _, triple := range triples {
		if triple.Ref == synthetic.Ref && triple.Action == "approve_contract" {
			found = true
		}
	}
	if !found {
		t.Fatal("definition expansion missed the synthetic caller-registered approval action")
	}
	// An unknown custom action must fail coverage: it inherits no blanket
	// gap row. The caller-registered class is exercised by the deletion
	// witness test, which removes caller_registered from the known-source
	// row and proves the same coverage check fails and then passes.
	unknown := cloneWorkflowDefinition(synthetic)
	unknown.Ref = "workflow.synthetic_unknown_action"
	unknown.ActionDefinitions = append(unknown.ActionDefinitions, WorkflowActionDefinition{
		ID: "synthetic_unknown_action", Consequence: ActionInternalSQLite, Approval: ActionApprovalRequired, ExecutionMode: ActionHold,
	})
	problems := approvalInventoryTripleCoverageProblems(realBuiltinTriples(t), approvalInventoryExpansion([]WorkflowDefinition{unknown}), approvalInventoryLoadTable(t).ApprovalPoints)
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "workflow.synthetic_unknown_action") || !strings.Contains(joined, "synthetic_unknown_action") {
		t.Fatalf("triple coverage check did not fail for the unknown custom action: %v", joined)
	}
}

func realBuiltinTriples(t *testing.T) []approvalInventoryTriple {
	t.Helper()
	return approvalInventoryExpansion(BuiltinWorkflowDefinitions())
}

// callerWitnessTriples registers a real caller-registered definition through
// the production registry API and returns its approval triples: the
// behavioral witness behind every caller_registered claim.
func callerWitnessTriples(t *testing.T) []approvalInventoryTriple {
	t.Helper()
	definitions := BuiltinWorkflowDefinitions()
	synthetic := cloneWorkflowDefinition(definitions[0])
	synthetic.Ref = "workflow.caller_witness"
	registry := NewWorkflowDefinitionRegistry()
	if _, err := registry.Register(synthetic); err != nil {
		t.Fatalf("caller-registered witness definition is not registrable: %v", err)
	}
	if resolved, found := registry.Current(synthetic.Ref); !found || resolved.Definition.Ref != synthetic.Ref {
		t.Fatal("caller registry did not resolve the witness definition")
	}
	return approvalInventoryExpansion([]WorkflowDefinition{synthetic})
}

// TestOperatorApprovalInventoryCallerRegisteredWitness proves the
// caller_registered claims are load-bearing: appending a real registered
// caller definition's triples stays covered, removing caller_registered from
// the known-source row fails the SAME coverage check, and restoring it
// passes.
func TestOperatorApprovalInventoryCallerRegisteredWitness(t *testing.T) {
	definitions := BuiltinWorkflowDefinitions()
	_ = definitions
	builtin := realBuiltinTriples(t)
	caller := callerWitnessTriples(t)
	rows := approvalInventoryLoadTable(t).ApprovalPoints
	if problems := approvalInventoryTripleCoverageProblems(builtin, caller, rows); len(problems) != 0 {
		t.Fatalf("caller-registered witness triple is uncovered with the claim present: %v", problems)
	}
	stripped := make([]approvalInventoryPointRow, 0, len(rows))
	for _, row := range rows {
		if row.Owner.Branch == "confirm_premise" && slices.Contains(row.Reachability, "caller_registered") {
			reach := make([]string, 0, len(row.Reachability))
			for _, class := range row.Reachability {
				if class != "caller_registered" {
					reach = append(reach, class)
				}
			}
			row.Reachability = reach
		}
		stripped = append(stripped, row)
	}
	problems := approvalInventoryTripleCoverageProblems(builtin, caller, stripped)
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "workflow.caller_witness") || !strings.Contains(joined, "caller_registered") {
		t.Fatalf("removing caller_registered did not fail coverage: %v", joined)
	}
}

// approvalInventoryCoverageProblems is the shared set-equality check between
// census entries and table rows, through the declared aliases and witnesses.
// It returns the problems instead of failing, so the synthetic omission
// controls execute the identical check the real table must satisfy.
func approvalInventoryCoverageProblems(points []approvalInventoryPoint, rows []approvalInventoryPointRow, manifestGated []approvalInventoryManifestOperation, planners map[string][]string) []string {
	var problems []string
	rowMap := map[string]approvalInventoryPointRow{}
	for _, row := range rows {
		rowMap[row.ID] = row
	}
	for _, row := range rows {
		if row.ID == "" {
			problems = append(problems, "approval_points row with empty id")
			continue
		}
		if strings.Contains(row.Owner.Path, "*") || strings.Contains(row.Owner.Symbol, "*") || strings.Contains(row.Owner.Branch, "*") {
			problems = append(problems, fmt.Sprintf("approval_points row %s claims a wildcard owner; coverage is exact, not a reachability claim", row.ID))
		}
		if row.Owner.Path == "" || row.Owner.Symbol == "" || row.Owner.Branch == "" {
			problems = append(problems, fmt.Sprintf("approval_points row %s omits an owner path, symbol, or branch", row.ID))
		}
		// A null holding record is honest only on a reasoned gap with source
		// evidence; a class always carries its resolved actual holder.
		if row.HoldingLaw == nil {
			if !approvalInventoryRowIsGap(row) || len(row.Evidence) == 0 {
				problems = append(problems, fmt.Sprintf("approval_points row %s carries a null holding law without a reasoned gap and source evidence", row.ID))
			} else if !approvalInventoryNamesUnresolvedHolder(row) {
				problems = append(problems, fmt.Sprintf("approval_points row %s carries a null holding law whose reason does not name the unresolved holder", row.ID))
			}
		} else if strings.TrimSpace(*row.HoldingLaw) == "" {
			problems = append(problems, fmt.Sprintf("approval_points row %s carries no holding law", row.ID))
		}
		if len(row.Reachability) == 0 {
			problems = append(problems, fmt.Sprintf("approval_points row %s carries no reachability", row.ID))
		}
		for _, class := range row.Reachability {
			if !approvalInventoryReachabilities[class] {
				problems = append(problems, fmt.Sprintf("approval_points row %s carries unknown reachability %q", row.ID, class))
			}
		}
		problems = append(problems, approvalInventoryClassificationProblems(row)...)
	}
	rowOwners := map[string]string{}
	for id, row := range rowMap {
		if prior, dup := rowOwners[row.Owner.key()]; dup {
			problems = append(problems, fmt.Sprintf("approval_points rows %s and %s share owner %+v; each owner triple maps to exactly one row", prior, id, row.Owner))
			continue
		}
		rowOwners[row.Owner.key()] = id
	}
	census := map[string]approvalInventoryPoint{}
	for _, point := range points {
		census[point.Owner.key()] = point
	}
	for key, point := range census {
		if _, ok := rowOwners[key]; ok {
			continue
		}
		if _, aliased := approvalInventoryAliases[key]; aliased {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s %s at %s", point.Kind, strings.ReplaceAll(key, "\x00", "/"), point.Detail))
	}
	for id, row := range rowMap {
		if _, ok := census[row.Owner.key()]; !ok {
			problems = append(problems, fmt.Sprintf("row %s claims owner %+v which the census does not find in the current sources", id, row.Owner))
		}
	}
	for key, reason := range approvalInventoryAliases {
		if _, ok := census[key]; !ok {
			problems = append(problems, fmt.Sprintf("alias %q states %q but matches no census point in the current sources", strings.ReplaceAll(key, "\x00", "/"), reason))
		}
	}
	for key := range approvalInventoryRetainedDeclarations {
		if _, ok := census[key]; !ok {
			problems = append(problems, fmt.Sprintf("retained witness %q matches no census point", strings.ReplaceAll(key, "\x00", "/")))
		}
	}
	if len(manifestGated) > 0 {
		problems = append(problems, approvalInventoryManifestCoverageProblems(rowMap, manifestGated, planners, points)...)
	}
	return problems
}

// TestOperatorApprovalInventoryManifestConditionalPlannerControl proves the
// coverage loop still audits conditional operations after the required skip:
// a conditional operation whose manifest row exists but whose planner is
// unnamed must be flagged by the SAME function the real table satisfies.
func TestOperatorApprovalInventoryManifestConditionalPlannerControl(t *testing.T) {
	gated := []approvalInventoryManifestOperation{{ID: "concord_work_synthetic.conditional_op", Approval: "conditional"}}
	holding := "CD-0037"
	rows := map[string]approvalInventoryPointRow{
		"manifest-synthetic": {
			ID:             "manifest-synthetic",
			Owner:          approvalInventoryOwner{Path: "internal/agent/mutations.go", Symbol: "newMutationPlan", Branch: "manifest:concord_work_synthetic.conditional_op"},
			HoldingLaw:     &holding,
			Reachability:   []string{"live"},
			Classification: json.RawMessage(`{"gap_issue":"CON-1100","reason":"unresolved holder: synthetic conditional demand"}`),
			Evidence:       []string{"synthetic"},
		},
	}
	problems := approvalInventoryManifestCoverageProblems(rows, gated, map[string][]string{}, nil)
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "names no planner for it") {
		t.Fatalf("conditional operation without a planner was not flagged: %v", joined)
	}
	// Positive cases: the real conditional operations whose planners the
	// dispatch names and whose rows exist must raise no planner problems.
	realGated := approvalInventoryGatedManifestOperations(t)
	realPlanners := approvalInventoryManifestConditionalPlanners(t, realGated)
	realTable := approvalInventoryLoadTable(t)
	realRows := map[string]approvalInventoryPointRow{}
	for _, row := range realTable.ApprovalPoints {
		realRows[row.ID] = row
	}
	var realConditional []approvalInventoryManifestOperation
	for _, operation := range realGated {
		if operation.Approval == "conditional" {
			realConditional = append(realConditional, operation)
		}
	}
	if len(realConditional) == 0 {
		t.Fatal("the manifest names no conditional operations; the positive control is vacuous")
	}
	for operation, owner := range map[string]string{
		"concord_work_transition.workflow_action": "workflowActionMutation.execute",
		"concord_work_compact.reconcile":          "compactionMutation.reconcile",
	} {
		if !slices.Contains(realPlanners[operation], owner) {
			t.Fatalf("dispatch for %s does not reach its extracted owner %s: %v", operation, owner, realPlanners[operation])
		}
	}
	realPoints, _ := approvalInventoryCensus(t, realGated)
	positiveProblems := approvalInventoryManifestCoverageProblems(realRows, realConditional, realPlanners, realPoints)
	if len(positiveProblems) != 0 {
		t.Fatalf("a real conditional operation with a source-linked owner was flagged: %v", positiveProblems)
	}
}

// approvalInventoryConditionalPlannerProblems binds one conditional manifest
// operation to the planner the dispatch names and requires both an
// inventoried runtime demand and its row in the planner or a source-linked
// delegate. Both must belong to the same reachable owner.
func approvalInventoryConditionalPlannerProblems(operation approvalInventoryManifestOperation, planners map[string][]string, plannerPointSymbols, plannerRowOwners map[string]bool) []string {
	var problems []string
	owners := planners[operation.ID]
	if len(owners) == 0 {
		problems = append(problems, fmt.Sprintf("manifest operation %s is conditional but the dispatch switch names no planner for it", operation.ID))
		return problems
	}
	qualified := owners[0]
	hasPoint, hasRow, matched := false, false, false
	for _, owner := range owners {
		hasPoint = hasPoint || plannerPointSymbols[owner]
		hasRow = hasRow || plannerRowOwners[owner]
		matched = matched || (plannerPointSymbols[owner] && plannerRowOwners[owner])
	}
	if !hasPoint {
		problems = append(problems, fmt.Sprintf("manifest operation %s is conditional but its planner %s carries no inventoried runtime demand", operation.ID, qualified))
	}
	if !hasRow {
		problems = append(problems, fmt.Sprintf("manifest operation %s is conditional but no approval_points row is owned by its planner %s", operation.ID, qualified))
	}
	if hasPoint && hasRow && !matched {
		problems = append(problems, fmt.Sprintf("manifest operation %s is conditional but its planner %s has no source-linked owner carrying both a runtime demand and its approval_points row", operation.ID, qualified))
	}
	return problems
}

func TestOperatorApprovalInventoryDelegatedPlannerControl(t *testing.T) {
	const source = `package agent
type runtime struct{}
type syntheticMutation struct{}
type otherMutation struct{}
func (r runtime) dispatch(tool string) error {
	if tool == "concord_work_synthetic" {
		return r.mutateSynthetic()
	}
	return nil
}
func (r runtime) mutateSynthetic() error {
	mutation := syntheticMutation{}
	return mutation.check()
}
func (m syntheticMutation) check() error {
	return m.demand()
}
func (m syntheticMutation) demand() error {
	requiresApproval := true
	return nil
}
func (m otherMutation) check() error { return nil }
`
	gated := []approvalInventoryManifestOperation{{ID: "concord_work_synthetic.conditional_op", Approval: "conditional"}}
	row := approvalInventoryPointRow{ID: "synthetic-delegated-demand", Owner: approvalInventoryOwner{
		Path: "internal/agent/mutations.go", Symbol: "syntheticMutation.demand", Branch: "syntheticMutation.demand",
	}}
	for _, test := range []struct {
		name, old, replacement, want string
		omitRow, splitRowOwner       bool
	}{
		{name: "source-linked demand and row"},
		{name: "missing planner", old: "r.mutateSynthetic()", replacement: "r.unknown()", want: "names no planner for it"},
		{name: "removed delegated demand", old: "requiresApproval := true", replacement: "_ = true", want: "carries no inventoried runtime demand"},
		{name: "missing delegated row", omitRow: true, want: "no approval_points row is owned by its planner"},
		{name: "point and row on different owners", splitRowOwner: true, want: "no source-linked owner carrying both a runtime demand and its approval_points row"},
		{name: "removed delegation edge", old: "return mutation.check()", replacement: "return nil", want: "carries no inventoried runtime demand"},
		{name: "removed receiver delegation edge", old: "return m.demand()", replacement: "return nil", want: "carries no inventoried runtime demand"},
		{name: "cycle does not reach demand", old: "return m.demand()", replacement: "return m.check()", want: "carries no inventoried runtime demand"},
		{name: "missing delegated method", old: "return mutation.check()", replacement: "return mutation.absent()", want: "carries no inventoried runtime demand"},
		{name: "same method on wrong receiver", old: "mutation := syntheticMutation{}", replacement: "mutation := otherMutation{}", want: "carries no inventoried runtime demand"},
		{name: "callback is not delegation", old: "return mutation.check()", replacement: "invoke(func() error { return mutation.check() })\n\treturn nil", want: "carries no inventoried runtime demand"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := source
			if test.old != "" {
				if strings.Count(input, test.old) != 1 {
					t.Fatalf("control must change exactly one source site: %q", test.old)
				}
				input = strings.Replace(input, test.old, test.replacement, 1)
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "mutations.go", input, 0)
			if err != nil {
				t.Fatal(err)
			}
			visitor := &censusVisitor{fset: fset, relPath: "internal/agent/mutations.go", ordinal: map[string]int{}}
			ast.Walk(visitor, file)
			rows := map[string]approvalInventoryPointRow{}
			if !test.omitRow {
				controlRow := row
				if test.splitRowOwner {
					controlRow.Owner.Symbol = "runtime.mutateSynthetic"
				}
				rows[row.ID] = controlRow
			}
			planners := approvalInventoryConditionalPlannersFromAST(file, gated)
			problems := approvalInventoryManifestCoverageProblems(rows, gated, planners, visitor.points)
			if test.want == "" {
				if len(problems) != 0 {
					t.Fatalf("source-linked control is not covered: %v", problems)
				}
			} else if !strings.Contains(strings.Join(problems, "; "), test.want) {
				t.Fatalf("source control did not produce %q: %v", test.want, problems)
			}
		})
	}
}

// approvalInventoryManifestCoverageProblems requires one inventory row per
// gated manifest operation and a runtime demand plus its row in the planner
// or a source-linked delegate for every conditional operation. No manifest-wide
// row can claim wildcard coverage; a new gated operation needs its own row.
func approvalInventoryManifestCoverageProblems(rows map[string]approvalInventoryPointRow, gated []approvalInventoryManifestOperation, planners map[string][]string, points []approvalInventoryPoint) []string {
	var problems []string
	branchOwners := map[string]string{}
	for id, row := range rows {
		branchOwners[row.Owner.Branch] = id
	}
	plannerRowOwners := map[string]bool{}
	for _, row := range rows {
		if strings.HasSuffix(row.Owner.Path, "mutations.go") {
			plannerRowOwners[row.Owner.Symbol] = true
		}
	}
	plannerPointSymbols := map[string]bool{}
	for _, point := range points {
		if strings.HasSuffix(point.Owner.Path, "mutations.go") {
			plannerPointSymbols[point.Owner.Symbol] = true
		}
	}
	for _, operation := range gated {
		branch := "manifest:" + operation.ID
		if operation.Approval != "required" {
			// A conditional declaration is not an unconditional demand;
			// its planner-owned branch is checked here.
			if operation.Approval == "conditional" {
				problems = append(problems, approvalInventoryConditionalPlannerProblems(operation, planners, plannerPointSymbols, plannerRowOwners)...)
			}
			continue
		}
		if _, ok := branchOwners[branch]; !ok {
			problems = append(problems, fmt.Sprintf("manifest operation %s carries %s approval but has no approval_points row with branch %s", operation.ID, operation.Approval, branch))
		}
	}
	return problems
}

func approvalInventoryClassificationProblems(row approvalInventoryPointRow) []string {
	var class struct {
		Class string `json:"class"`
	}
	var gap struct {
		GapIssue string `json:"gap_issue"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(row.Classification, &class); err == nil && class.Class != "" {
		var extra map[string]json.RawMessage
		_ = json.Unmarshal(row.Classification, &extra)
		if _, hasGap := extra["gap_issue"]; hasGap {
			return []string{fmt.Sprintf("approval_points row %s carries both a class and a gap; classification is exactly one shape", row.ID)}
		}
		if len(extra) != 1 {
			return []string{fmt.Sprintf("approval_points row %s classification carries unknown fields: %s", row.ID, string(row.Classification))}
		}
		if !approvalInventoryClasses[class.Class] {
			return []string{fmt.Sprintf("approval_points row %s names unknown class %q; the four D5 classes are closed", row.ID, class.Class)}
		}
		return nil
	}
	if err := json.Unmarshal(row.Classification, &gap); err == nil && gap.GapIssue != "" {
		var problems []string
		if !strings.HasPrefix(gap.GapIssue, "CON-") || strings.TrimPrefix(gap.GapIssue, "CON-") == "" {
			problems = append(problems, fmt.Sprintf("approval_points row %s gap_issue %q must name a CON issue", row.ID, gap.GapIssue))
		}
		if strings.TrimSpace(gap.Reason) == "" {
			problems = append(problems, fmt.Sprintf("approval_points row %s names a gap with no reason", row.ID))
		}
		return problems
	}
	return []string{fmt.Sprintf("approval_points row %s classification is neither a D5 class nor a named gap: %s", row.ID, string(row.Classification))}
}

// approvalInventoryTripleCoverageProblems compares the declared coverage
// against the exact definition/version/action triples the registered history
// and off-pin recovery resolve to.
func approvalInventoryTripleCoverageProblems(builtinTriples []approvalInventoryTriple, callerTriples []approvalInventoryTriple, rows []approvalInventoryPointRow) []string {
	triples := append(append([]approvalInventoryTriple{}, builtinTriples...), callerTriples...)
	registeredRefs := map[string]bool{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		registeredRefs[definition.Ref] = true
	}
	tripleClass := func(triple approvalInventoryTriple) string {
		// The builtin registry holds current live families only.
		if !registeredRefs[triple.Ref] {
			return "caller_registered"
		}
		return "live"
	}
	tripleActions := map[string]bool{}
	classesByAction := map[string]map[string]bool{}
	for _, triple := range triples {
		tripleActions[triple.Action] = true
		if classesByAction[triple.Action] == nil {
			classesByAction[triple.Action] = map[string]bool{}
		}
		classesByAction[triple.Action][tripleClass(triple)] = true
	}
	covered := map[string]bool{}
	rowServes := map[string]bool{}
	var problems []string
	for _, row := range rows {
		if _, retained := approvalInventoryRetainedDeclarations[row.Owner.key()]; retained {
			for _, class := range row.Reachability {
				if class == "live" || class == "caller_registered" {
					problems = append(problems, fmt.Sprintf("witnessed retained declaration row %s claims %s reachability; an unreachable declaration is not live", row.ID, class))
				}
			}
			continue
		}
		if !tripleActions[row.Owner.Branch] {
			continue
		}
		for _, triple := range triples {
			if triple.Action != row.Owner.Branch || !slices.Contains(row.Reachability, tripleClass(triple)) {
				continue
			}
			covered[triple.key()] = true
			rowServes[row.ID] = true
		}
	}
	for key, evidence := range approvalInventoryRetainedDeclarations {
		witnessed := false
		for _, row := range rows {
			if row.Owner.key() == key {
				witnessed = true
				break
			}
		}
		if !witnessed {
			problems = append(problems, fmt.Sprintf("retained-declaration witness %q states %q but no approval_points row carries that owner", strings.ReplaceAll(key, "\x00", "/"), evidence))
		}
	}
	for _, triple := range triples {
		if !covered[triple.key()] {
			problems = append(problems, fmt.Sprintf("triple %s has no approval_points row whose branch names its action and whose reachability lists %s", triple.key(), tripleClass(triple)))
		}
	}
	for _, row := range rows {
		if _, retained := approvalInventoryRetainedDeclarations[row.Owner.key()]; retained {
			continue
		}
		if !tripleActions[row.Owner.Branch] {
			continue
		}
		if !rowServes[row.ID] {
			problems = append(problems, fmt.Sprintf("row %s branches for action %s but serves no registered definition/version/action triple", row.ID, row.Owner.Branch))
			continue
		}
		for _, class := range row.Reachability {
			if !classesByAction[row.Owner.Branch][class] {
				problems = append(problems, fmt.Sprintf("row %s claims %s reachability for action %s but no registered definition resolves that action at %s", row.ID, class, row.Owner.Branch, class))
			}
		}
	}
	return problems
}

func TestOperatorApprovalInventoryCensusRejectsStaleOwnerRow(t *testing.T) {
	gated := approvalInventoryGatedManifestOperations(t)
	points, _ := approvalInventoryCensus(t, gated)
	table := approvalInventoryLoadTable(t)
	holding := "CD-0037"
	rows := table.ApprovalPoints
	rows = append(rows, approvalInventoryPointRow{
		ID:             "synthetic-stale-owner",
		Owner:          approvalInventoryOwner{Path: "internal/store/workflow_registry.go", Symbol: "absentApprovalOwner", Branch: "absentApprovalOwner"},
		HoldingLaw:     &holding,
		Reachability:   []string{"live"},
		Classification: json.RawMessage(`{"class":"product_preference"}`),
	})
	planners := approvalInventoryManifestConditionalPlanners(t, gated)
	problems := approvalInventoryCoverageProblems(points, rows, gated, planners)
	if len(problems) != 1 || !strings.Contains(problems[0], "row synthetic-stale-owner claims owner") || !strings.Contains(problems[0], "which the census does not find in the current sources") {
		t.Fatalf("stale owner did not produce its exact census refusal: %v", problems)
	}
}

// TestOperatorApprovalInventoryTableCoversCensus asserts set equality between
// census entries and approval_points rows, through the declared aliases and
// witnesses, plus the manifest axis. It fails while the table carries no
// approval_points.
func TestOperatorApprovalInventoryTableCoversCensus(t *testing.T) {
	gated := approvalInventoryGatedManifestOperations(t)
	points, reads := approvalInventoryCensus(t, gated)
	if len(points) == 0 {
		t.Fatal("the approval census found no points; the census method is broken")
	}
	table := approvalInventoryLoadTable(t)
	if len(table.ApprovalPoints) == 0 {
		t.Fatalf("contracts/conflict-rules.v1.json carries no approval_points: the operator approval inventory is absent. "+
			"The census found %d consent point(s) across internal/store and internal/agent that no table row covers: %s. "+
			"%d non-point identifier read(s) were classified and excluded: %s",
			len(approvalInventoryPointKeys(points)), approvalInventoryFormatPoints(points), len(reads), strings.Join(reads, "; "))
	}
	if table.CatalogGeneration < 1 {
		t.Errorf("catalog_generation must be a positive per-revision identity")
	}
	if table.CatalogGeneration != conflictRuleCatalogGeneration {
		t.Errorf("catalog generation %d differs from generated catalog %d; rerun scripts/generate-conflict-rules.py", table.CatalogGeneration, conflictRuleCatalogGeneration)
	}
	planners := approvalInventoryManifestConditionalPlanners(t, gated)
	problems := approvalInventoryCoverageProblems(points, table.ApprovalPoints, gated, planners)
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("approval_points does not equal the consent census: %d problem(s): %s", len(problems), strings.Join(problems, "; "))
	}
}

// TestOperatorApprovalInventoryRegistryExpansionIsCovered compares the
// declared coverage against the exact definition/version/action triples the
// registered history and off-pin recovery resolve to.
func TestOperatorApprovalInventoryRegistryExpansionIsCovered(t *testing.T) {
	definitions := BuiltinWorkflowDefinitions()
	triples := approvalInventoryExpansion(definitions)
	if len(triples) == 0 {
		t.Fatal("no registered workflow definition carries an approval-required action; the expansion is broken")
	}
	table := approvalInventoryLoadTable(t)
	if len(table.ApprovalPoints) == 0 {
		t.Fatalf("contracts/conflict-rules.v1.json carries no approval_points: %d registered definition/version/action approval triple(s) have no inventory row: %v",
			len(triples), triples)
	}
	caller := callerWitnessTriples(t)
	problems := approvalInventoryTripleCoverageProblems(triples, caller, table.ApprovalPoints)
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("registered approval triples are not covered: %d problem(s): %s", len(problems), strings.Join(problems, "; "))
	}
}
