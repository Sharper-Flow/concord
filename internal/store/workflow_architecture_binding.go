package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	workflowArchitectureBindingMaxDomains      = 64
	workflowArchitectureBindingMaxRelations    = 64
	workflowArchitectureBindingMaxLawAdditions = 32
	workflowArchitectureBindingMaxObligations  = 64
)

// WorkflowArchitectureBinding is the one contract-owned description of a
// Product-changing work item's architectural and legislative footprint. The
// outcome, mandate, law modifications, and law revision pins remain fields of
// the composed workflow contract; this type owns only the additional binding.
type WorkflowArchitectureBinding struct {
	DomainRegistryContentHash string                               `json:"domain_registry_content_hash"`
	HomeDomainID              string                               `json:"home_domain_id"`
	AffectedDomainIDs         []string                             `json:"affected_domain_ids"`
	DomainModifies            []string                             `json:"domain_modifies"`
	DomainRelationModifies    []WorkflowDomainRelationModification `json:"domain_relation_modifies"`
	LawAdditions              []WorkflowLawAddition                `json:"law_additions"`
	VerificationObligations   []WorkflowVerificationObligation     `json:"verification_obligations"`
}

type WorkflowDomainRelationModification struct {
	SourceDomainID string `json:"source_domain_id"`
	Kind           string `json:"kind"`
	TargetDomainID string `json:"target_domain_id"`
}

type WorkflowLawAddition struct {
	LawID        string `json:"law_id"`
	HomeDomainID string `json:"home_domain_id"`
}

type WorkflowVerificationObligation struct {
	LawID        string `json:"law_id"`
	ObligationID string `json:"obligation_id"`
}

// UnmarshalJSON deliberately owns strict decoding for the nested binding. A
// plain struct decoder would otherwise make a missing array indistinguishable
// from an explicitly empty array and would silently accept future fields.
func (b *WorkflowArchitectureBinding) UnmarshalJSON(data []byte) error {
	type bindingAlias WorkflowArchitectureBinding
	var decoded bindingAlias
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("architecture_binding contains trailing data")
	}
	if decoded.AffectedDomainIDs == nil || decoded.DomainModifies == nil || decoded.DomainRelationModifies == nil || decoded.LawAdditions == nil || decoded.VerificationObligations == nil {
		return fmt.Errorf("architecture_binding arrays must be explicitly present and non-null")
	}
	if err := validateWorkflowArchitectureBindingShape(WorkflowArchitectureBinding(decoded)); err != nil {
		return err
	}
	*b = WorkflowArchitectureBinding(decoded)
	return nil
}

func parseWorkflowArchitectureBinding(raw json.RawMessage) (*WorkflowArchitectureBinding, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var binding WorkflowArchitectureBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_architecture_binding", "architecture_binding is not a strict typed object: "+err.Error(), false, "supply every binding field exactly once")
	}
	if err := validateWorkflowArchitectureBindingShape(binding); err != nil {
		return nil, err
	}
	return &binding, nil
}

func validateWorkflowArchitectureBindingShape(binding WorkflowArchitectureBinding) error {
	if err := validateContentHash(binding.DomainRegistryContentHash); err != nil {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "domain registry content hash is invalid", false, "pin one sha256 domain registry hash")
	}
	if !validDomainBindingID(binding.HomeDomainID) || len(binding.AffectedDomainIDs) == 0 || len(binding.AffectedDomainIDs) > workflowArchitectureBindingMaxDomains || len(binding.DomainModifies) > workflowArchitectureBindingMaxDomains || len(binding.DomainRelationModifies) > workflowArchitectureBindingMaxRelations || len(binding.LawAdditions) > workflowArchitectureBindingMaxLawAdditions || len(binding.VerificationObligations) > workflowArchitectureBindingMaxObligations {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "architecture binding identity or bounds are invalid", false, "supply bounded non-empty Domain and law binding fields")
	}
	if !uniqueDomainBindingIDs(binding.AffectedDomainIDs) || !uniqueDomainBindingIDs(binding.DomainModifies) {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "architecture binding Domain lists contain duplicates or invalid IDs", false, "supply unique bounded Domain IDs")
	}
	seenRelations := map[string]struct{}{}
	for _, relation := range binding.DomainRelationModifies {
		if !validDomainBindingID(relation.SourceDomainID) || !validDomainBindingID(relation.TargetDomainID) || relation.SourceDomainID == relation.TargetDomainID || (relation.Kind != "depends_on" && relation.Kind != "shares_contract_with" && relation.Kind != "replaces") {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "Domain relation modification is not a closed canonical tuple", false, "use a current source/kind/target Domain relation")
		}
		if relation.Kind == "shares_contract_with" && relation.SourceDomainID >= relation.TargetDomainID {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "symmetric Domain relation is not in canonical pair order", false, "order shares_contract_with endpoints lexically")
		}
		key := relation.SourceDomainID + "\x00" + relation.Kind + "\x00" + relation.TargetDomainID
		if _, exists := seenRelations[key]; exists {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "Domain relation modifications contain duplicates", false, "supply each exact relation tuple once")
		}
		seenRelations[key] = struct{}{}
	}
	seenLaws := map[string]struct{}{}
	for _, addition := range binding.LawAdditions {
		if !validLawBindingID(addition.LawID) || !validDomainBindingID(addition.HomeDomainID) {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "law addition identity is invalid", false, "supply a bounded law ID and home Domain")
		}
		if _, exists := seenLaws[addition.LawID]; exists {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "law additions contain duplicate IDs", false, "reserve each law ID once")
		}
		seenLaws[addition.LawID] = struct{}{}
	}
	seenObligations := map[string]struct{}{}
	for _, obligation := range binding.VerificationObligations {
		if !validLawBindingID(obligation.LawID) || !validWorkflowObligationID(obligation.ObligationID) {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "verification obligation identity is invalid", false, "name a bounded law and declared workflow obligation")
		}
		key := obligation.LawID + "\x00" + obligation.ObligationID
		if _, exists := seenObligations[key]; exists {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "verification obligations contain duplicates", false, "supply each law/obligation tuple once")
		}
		seenObligations[key] = struct{}{}
	}
	return nil
}

func validDomainBindingID(value string) bool {
	return len(value) >= 1 && len(value) <= 256 && strings.TrimSpace(value) == value
}
func validLawBindingID(value string) bool {
	return len(value) >= 2 && len(value) <= 256 && strings.TrimSpace(value) == value
}
func validWorkflowObligationID(value string) bool {
	return len(value) >= 1 && len(value) <= 256 && strings.TrimSpace(value) == value
}

func uniqueDomainBindingIDs(values []string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		if !validDomainBindingID(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func architectureBindingRevisionMandate(specMandate []string, additions []WorkflowLawAddition) ([]string, error) {
	if err := validateLawModificationSubset(specMandate, []string{}); err != nil {
		return nil, err
	}
	added := map[string]struct{}{}
	for _, addition := range additions {
		if _, exists := added[addition.LawID]; exists {
			return nil, newFailure(KindInvalidPayload, "workflow_architecture_binding", "law addition is duplicated", false, "supply unique law additions")
		}
		added[addition.LawID] = struct{}{}
	}
	result := make([]string, 0, len(specMandate))
	for _, lawID := range specMandate {
		if _, isAddition := added[lawID]; !isAddition {
			result = append(result, lawID)
		}
	}
	return result, nil
}

func currentWorkflowLawMandate(specMandate []string, binding *WorkflowArchitectureBinding) ([]string, error) {
	if binding == nil {
		return append([]string{}, specMandate...), nil
	}
	return architectureBindingRevisionMandate(specMandate, binding.LawAdditions)
}

func currentWorkflowLawMandateFromProjection(ctx context.Context, q queryer, workID string, contractVersion int64, specMandate []string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT law_id FROM workflow_contract_law_additions WHERE work_id=? AND contract_version=? ORDER BY law_id`, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	additions := []WorkflowLawAddition{}
	for rows.Next() {
		var lawID string
		if err := rows.Scan(&lawID); err != nil {
			rows.Close()
			return nil, err
		}
		additions = append(additions, WorkflowLawAddition{LawID: lawID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return architectureBindingRevisionMandate(specMandate, additions)
}

func workflowDefinitionObligations(definition WorkflowDefinition) map[string]struct{} {
	result := map[string]struct{}{}
	for _, kind := range definition.RequiredEvidenceKinds {
		result[string(kind)] = struct{}{}
	}
	for _, step := range definition.StepGraph.Steps {
		for _, kind := range step.RequiredEvidenceKinds {
			result[string(kind)] = struct{}{}
		}
	}
	for _, rule := range definition.RigorRules {
		for _, kind := range rule.RequiredEvidenceKinds {
			result[string(kind)] = struct{}{}
		}
	}
	return result
}

func architectureBindingProjectionHash(binding WorkflowArchitectureBinding) string {
	copy := binding
	copy.AffectedDomainIDs = append([]string(nil), binding.AffectedDomainIDs...)
	copy.DomainModifies = append([]string(nil), binding.DomainModifies...)
	copy.DomainRelationModifies = append([]WorkflowDomainRelationModification(nil), binding.DomainRelationModifies...)
	copy.LawAdditions = append([]WorkflowLawAddition(nil), binding.LawAdditions...)
	copy.VerificationObligations = append([]WorkflowVerificationObligation(nil), binding.VerificationObligations...)
	sort.Strings(copy.AffectedDomainIDs)
	sort.Strings(copy.DomainModifies)
	sort.Slice(copy.DomainRelationModifies, func(i, j int) bool {
		return relationBindingKey(copy.DomainRelationModifies[i]) < relationBindingKey(copy.DomainRelationModifies[j])
	})
	sort.Slice(copy.LawAdditions, func(i, j int) bool { return copy.LawAdditions[i].LawID < copy.LawAdditions[j].LawID })
	sort.Slice(copy.VerificationObligations, func(i, j int) bool {
		return copy.VerificationObligations[i].LawID+"\x00"+copy.VerificationObligations[i].ObligationID < copy.VerificationObligations[j].LawID+"\x00"+copy.VerificationObligations[j].ObligationID
	})
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func relationBindingKey(relation WorkflowDomainRelationModification) string {
	return relation.SourceDomainID + "\x00" + relation.Kind + "\x00" + relation.TargetDomainID
}

func validateArchitectureBindingTx(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, binding *WorkflowArchitectureBinding, specMandate, lawModifies []string, lawRevisions []WorkflowLawRevision) error {
	if binding == nil {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "Product-changing contract is missing architecture_binding", false, "supply the complete architecture binding")
	}
	if err := validateWorkflowArchitectureBindingShape(*binding); err != nil {
		return err
	}
	if !*definition.ChangesProductTruth {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "non-Product-changing workflow cannot carry an architecture binding", false, "select a registered Product-changing workflow")
	}
	affected := make(map[string]struct{}, len(binding.AffectedDomainIDs))
	for _, domainID := range binding.AffectedDomainIDs {
		affected[domainID] = struct{}{}
	}
	if _, ok := affected[binding.HomeDomainID]; !ok {
		return newFailure(KindUnknownScope, "workflow_architecture_binding", "home Domain is not in affected_domain_ids", false, "include the home Domain in the affected set")
	}
	if err := validateLawModificationSubset(specMandate, lawModifies); err != nil {
		return err
	}
	for _, domainID := range binding.DomainModifies {
		if _, ok := affected[domainID]; !ok {
			return newFailure(KindUnknownScope, "workflow_architecture_binding", "domain_modifies contains a Domain outside affected_domain_ids", false, "modify only affected Domains")
		}
	}
	var productIDs []string
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 2`, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_architecture_binding", "cannot resolve workflow Product scope", true, "retry once the workflow scope is readable", err)
	}
	for rows.Next() {
		var productID string
		if err := rows.Scan(&productID); err != nil {
			rows.Close()
			return err
		}
		productIDs = append(productIDs, productID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(productIDs) != 1 {
		return newFailure(KindUnknownScope, "workflow_architecture_binding", "workflow must resolve to exactly one Product", false, "assign the work to Projects in one Product")
	}
	productID := productIDs[0]
	var registryHash string
	if err := tx.QueryRowContext(ctx, `SELECT content_hash FROM domain_registries WHERE product_id=?`, productID).Scan(&registryHash); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnknownScope, "workflow_architecture_binding", "Product has no current Domain registry", false, "publish and rebuild the Product Domain registry")
		}
		return wrapFailure(KindUnavailable, "workflow_architecture_binding", "cannot read Product Domain registry", true, "retry once the registry projection is readable", err)
	}
	if registryHash != binding.DomainRegistryContentHash {
		return newFailure(KindStaleRequiresReview, "workflow_architecture_binding", "architecture binding Domain registry hash is stale", false, "reread and pin the Product's current Domain registry")
	}
	for domainID := range affected {
		var status, hash string
		if err := tx.QueryRowContext(ctx, `SELECT status,registry_content_hash FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&status, &hash); err != nil {
			if err == sql.ErrNoRows {
				return newFailure(KindUnknownScope, "workflow_architecture_binding", "architecture binding names an unknown Domain: "+domainID, false, "name a current Domain in the Product registry")
			}
			return wrapFailure(KindUnavailable, "workflow_architecture_binding", "cannot read named Domain", true, "retry once the Domain projection is readable", err)
		}
		if status != "current" || hash != registryHash {
			return newFailure(KindStaleRequiresReview, "workflow_architecture_binding", "architecture binding names a non-current or stale Domain: "+domainID, false, "rebuild the current Product Domain registry")
		}
	}
	for _, relation := range binding.DomainRelationModifies {
		if _, ok := affected[relation.SourceDomainID]; !ok {
			return newFailure(KindUnknownScope, "workflow_architecture_binding", "relation source is outside affected Domains", false, "include relation endpoints in affected_domain_ids")
		}
		if _, ok := affected[relation.TargetDomainID]; !ok {
			return newFailure(KindUnknownScope, "workflow_architecture_binding", "relation target is outside affected Domains", false, "include relation endpoints in affected_domain_ids")
		}
	}
	homeProjectID, homeLocatorID, err := productKnowledgeHomeTx(ctx, tx, productID)
	if err != nil {
		return err
	}
	for _, lawID := range lawModifies {
		var homeDomain, status string
		if err := tx.QueryRowContext(ctx, `SELECT h.domain_id,s.status FROM law_domain_homes h JOIN law_subjects s ON s.home_project_id=h.home_project_id AND s.home_locator_id=h.home_locator_id AND s.law_id=h.law_id WHERE h.product_id=? AND h.home_project_id=? AND h.home_locator_id=? AND h.law_id=?`, productID, homeProjectID, homeLocatorID, lawID).Scan(&homeDomain, &status); err != nil {
			return newFailure(KindProjectionNotFound, "workflow_architecture_binding", "law_modifies names a law without a current canonical home", false, "name a current accepted law in the Product mandate")
		}
		if status != "accepted" {
			return newFailure(KindProjectionNotFound, "workflow_architecture_binding", "law_modifies names a non-current law", false, "modify only current accepted law")
		}
		if _, ok := affected[homeDomain]; !ok {
			return newFailure(KindUnknownScope, "workflow_architecture_binding", "modified law home Domain is outside affected Domains", false, "include each modified law home Domain")
		}
	}
	for _, addition := range binding.LawAdditions {
		if _, inMandate := stringSet(specMandate)[addition.LawID]; !inMandate {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "law addition is outside spec_mandate", false, "authorize every law addition in spec_mandate")
		}
		if containsString(lawModifies, addition.LawID) {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "law addition overlaps law_modifies", false, "choose addition or modification for a law ID")
		}
		for _, revision := range lawRevisions {
			if revision.LawID == addition.LawID {
				return newFailure(KindInvalidPayload, "workflow_architecture_binding", "law addition overlaps a governing law revision", false, "exclude additions from law_revisions")
			}
		}
		if _, ok := affected[addition.HomeDomainID]; !ok {
			return newFailure(KindUnknownScope, "workflow_architecture_binding", "law addition home Domain is outside affected Domains", false, "include each added law home Domain")
		}
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, homeProjectID, homeLocatorID, addition.LawID).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return newFailure(KindProjectionConflict, "workflow_architecture_binding", "law addition reuses an existing Product law ID", false, "use a new law ID or modify the existing law")
		}
	}
	remaining, err := architectureBindingRevisionMandate(specMandate, binding.LawAdditions)
	if err != nil {
		return err
	}
	if err := validateWorkflowLawRevisions(remaining, lawRevisions); err != nil {
		return err
	}
	for _, revision := range lawRevisions {
		var currentHash string
		if err := tx.QueryRowContext(ctx, `SELECT s.content_hash FROM law_subjects s JOIN law_domain_homes h ON h.home_project_id=s.home_project_id AND h.home_locator_id=s.home_locator_id AND h.law_id=s.law_id WHERE h.product_id=? AND h.home_project_id=? AND h.home_locator_id=? AND s.law_id=? AND s.status='accepted'`, productID, homeProjectID, homeLocatorID, revision.LawID).Scan(&currentHash); err != nil {
			return newFailure(KindProjectionNotFound, "workflow_architecture_binding", "governing law revision is not current", false, "pin the current accepted Git law hash")
		}
		if currentHash != revision.ContentHash && !isWorkflowReplay(ctx) {
			return newFailure(KindStaleRequiresReview, "workflow_architecture_binding", "governing law revision pin is stale", false, "reread and pin the current accepted Git law hash")
		}
	}
	obligations := workflowDefinitionObligations(definition)
	for _, obligation := range binding.VerificationObligations {
		if _, ok := obligations[obligation.ObligationID]; !ok {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "verification obligation is not declared by the pinned workflow definition", false, "reference a root, step, or rigor evidence obligation ID")
		}
		var currentHash string
		if err := tx.QueryRowContext(ctx, `SELECT s.content_hash FROM law_subjects s JOIN law_domain_homes h ON h.home_project_id=s.home_project_id AND h.home_locator_id=s.home_locator_id AND h.law_id=s.law_id WHERE h.product_id=? AND h.home_project_id=? AND h.home_locator_id=? AND s.law_id=? AND s.status='accepted'`, productID, homeProjectID, homeLocatorID, obligation.LawID).Scan(&currentHash); err != nil {
			return newFailure(KindProjectionNotFound, "workflow_architecture_binding", "verification obligation names a non-current law", false, "reference a current pinned law revision")
		}
		foundPin := false
		for _, revision := range lawRevisions {
			if revision.LawID == obligation.LawID {
				foundPin = true
				break
			}
		}
		if !foundPin {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "verification obligation law is not pinned", false, "pin every verified current law")
		}
	}
	return nil
}

func validateArchitectureBindingReplayShape(definition WorkflowDefinition, binding *WorkflowArchitectureBinding, specMandate, lawModifies []string, lawRevisions []WorkflowLawRevision) error {
	if binding == nil {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "Product-changing replay is missing architecture_binding", false, "repair the historical contract payload")
	}
	if err := validateWorkflowArchitectureBindingShape(*binding); err != nil {
		return err
	}
	affected := stringSet(binding.AffectedDomainIDs)
	if _, ok := affected[binding.HomeDomainID]; !ok {
		return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical binding home Domain is not affected", false, "repair the historical binding shape")
	}
	if err := validateLawModificationSubset(specMandate, lawModifies); err != nil {
		return err
	}
	for _, domainID := range binding.DomainModifies {
		if _, ok := affected[domainID]; !ok {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical modified Domain is not affected", false, "repair the historical binding shape")
		}
	}
	for _, relation := range binding.DomainRelationModifies {
		if _, ok := affected[relation.SourceDomainID]; !ok {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical relation source is not affected", false, "repair the historical binding shape")
		}
		if _, ok := affected[relation.TargetDomainID]; !ok {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical relation target is not affected", false, "repair the historical binding shape")
		}
	}
	remaining, err := architectureBindingRevisionMandate(specMandate, binding.LawAdditions)
	if err != nil {
		return err
	}
	if err := validateWorkflowLawRevisions(remaining, lawRevisions); err != nil {
		return err
	}
	for _, addition := range binding.LawAdditions {
		if !containsString(specMandate, addition.LawID) {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical law addition is outside the mandate", false, "repair the historical law mandate")
		}
		if containsString(lawModifies, addition.LawID) {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical law addition overlaps law_modifies", false, "repair the historical law fields")
		}
		for _, revision := range lawRevisions {
			if revision.LawID == addition.LawID {
				return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical law addition overlaps law revisions", false, "repair the historical law fields")
			}
		}
	}
	obligations := workflowDefinitionObligations(definition)
	pinned := map[string]struct{}{}
	for _, revision := range lawRevisions {
		pinned[revision.LawID] = struct{}{}
	}
	for _, obligation := range binding.VerificationObligations {
		if _, ok := obligations[obligation.ObligationID]; !ok {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical verification obligation is not declared by the definition", false, "repair the obligation ID")
		}
		if _, ok := pinned[obligation.LawID]; !ok {
			return newFailure(KindInvalidPayload, "workflow_architecture_binding", "historical verification obligation law is not pinned", false, "repair the law revision pins")
		}
	}
	return nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func productKnowledgeHomeTx(ctx context.Context, tx *sql.Tx, productID string) (string, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT project_id,locator_id FROM product_knowledge_homes WHERE product_id=? ORDER BY project_id,locator_id LIMIT 2`, productID)
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "workflow_architecture_binding", "cannot resolve Product law home", true, "retry once the Product projection is readable", err)
	}
	var homes [][2]string
	for rows.Next() {
		var project, locator string
		if err := rows.Scan(&project, &locator); err != nil {
			rows.Close()
			return "", "", err
		}
		homes = append(homes, [2]string{project, locator})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", "", err
	}
	if err := rows.Close(); err != nil {
		return "", "", err
	}
	if len(homes) != 1 {
		return "", "", newFailure(KindUnknownScope, "workflow_architecture_binding", "Product must have exactly one canonical law home", false, "resolve one Product knowledge home")
	}
	return homes[0][0], homes[0][1], nil
}

func persistWorkflowArchitectureBindingTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, binding WorkflowArchitectureBinding) error {
	productID, err := workflowBindingProductIDTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,?,?,?,?)`, workID, contractVersion, productID, binding.DomainRegistryContentHash, binding.HomeDomainID, architectureBindingProjectionHash(binding)); err != nil {
		return workflowProjectionError(err, "cannot record workflow architecture binding")
	}
	for _, domainID := range binding.AffectedDomainIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,?,?)`, workID, contractVersion, domainID); err != nil {
			return workflowProjectionError(err, "cannot record affected Domain")
		}
	}
	for _, domainID := range binding.DomainModifies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES(?,?,?)`, workID, contractVersion, domainID); err != nil {
			return workflowProjectionError(err, "cannot record modified Domain")
		}
	}
	for _, relation := range binding.DomainRelationModifies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_relation_modifications(work_id,contract_version,source_domain_id,kind,target_domain_id) VALUES(?,?,?,?,?)`, workID, contractVersion, relation.SourceDomainID, relation.Kind, relation.TargetDomainID); err != nil {
			return workflowProjectionError(err, "cannot record modified Domain relation")
		}
	}
	for _, addition := range binding.LawAdditions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES(?,?,?,?,?) ON CONFLICT(product_id,law_id) DO NOTHING`, productID, addition.LawID, workID, contractVersion, addition.HomeDomainID); err != nil {
			return workflowProjectionError(err, "cannot reserve law addition")
		}
		var owner, homeDomain string
		var ownerVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT owner_work_id,owner_contract_version,home_domain_id FROM workflow_law_addition_reservations WHERE product_id=? AND law_id=?`, productID, addition.LawID).Scan(&owner, &ownerVersion, &homeDomain); err != nil || owner != workID || homeDomain != addition.HomeDomainID {
			return newFailure(KindProjectionConflict, "workflow_architecture_binding", "law addition is reserved by another work", false, "choose a different law ID")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES(?,?,?,?,?,?,?)`, workID, contractVersion, productID, addition.LawID, addition.HomeDomainID, owner, ownerVersion); err != nil {
			return workflowProjectionError(err, "cannot record law addition")
		}
	}
	for _, obligation := range binding.VerificationObligations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES(?,?,?,?)`, workID, contractVersion, obligation.LawID, obligation.ObligationID); err != nil {
			return workflowProjectionError(err, "cannot record verification obligation")
		}
	}
	return nil
}

func workflowBindingProductIDTx(ctx context.Context, tx *sql.Tx, workID string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 2`, workID)
	if err != nil {
		return "", wrapFailure(KindUnavailable, "workflow_architecture_binding", "cannot resolve workflow Product scope", true, "retry once the workflow scope is readable", err)
	}
	var productIDs []string
	for rows.Next() {
		var productID string
		if err := rows.Scan(&productID); err != nil {
			rows.Close()
			return "", err
		}
		productIDs = append(productIDs, productID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if len(productIDs) != 1 {
		return "", newFailure(KindUnknownScope, "workflow_architecture_binding", "workflow must resolve to exactly one Product", false, "assign the work to Projects in one Product")
	}
	return productIDs[0], nil
}

func readWorkflowArchitectureBinding(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, workID string, contractVersion int64) (*WorkflowArchitectureBinding, error) {
	var binding WorkflowArchitectureBinding
	if err := q.QueryRowContext(ctx, `SELECT domain_registry_content_hash,home_domain_id FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&binding.DomainRegistryContentHash, &binding.HomeDomainID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow architecture binding", true, "retry once the workflow projection is readable", err)
	}
	binding.AffectedDomainIDs = []string{}
	binding.DomainModifies = []string{}
	binding.DomainRelationModifies = []WorkflowDomainRelationModification{}
	binding.LawAdditions = []WorkflowLawAddition{}
	binding.VerificationObligations = []WorkflowVerificationObligation{}
	var err error
	if binding.AffectedDomainIDs, err = readWorkflowBindingStringList(ctx, q, `SELECT domain_id FROM workflow_contract_affected_domains WHERE work_id=? AND contract_version=? ORDER BY domain_id`, workID, contractVersion); err != nil {
		return nil, err
	}
	if binding.DomainModifies, err = readWorkflowBindingStringList(ctx, q, `SELECT domain_id FROM workflow_contract_domain_modifications WHERE work_id=? AND contract_version=? ORDER BY domain_id`, workID, contractVersion); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT source_domain_id,kind,target_domain_id FROM workflow_contract_domain_relation_modifications WHERE work_id=? AND contract_version=? ORDER BY source_domain_id,kind,target_domain_id`, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var value WorkflowDomainRelationModification
		if err := rows.Scan(&value.SourceDomainID, &value.Kind, &value.TargetDomainID); err != nil {
			rows.Close()
			return nil, err
		}
		binding.DomainRelationModifies = append(binding.DomainRelationModifies, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT law_id,home_domain_id FROM workflow_contract_law_additions WHERE work_id=? AND contract_version=? ORDER BY law_id`, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var value WorkflowLawAddition
		if err := rows.Scan(&value.LawID, &value.HomeDomainID); err != nil {
			rows.Close()
			return nil, err
		}
		binding.LawAdditions = append(binding.LawAdditions, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = q.QueryContext(ctx, `SELECT law_id,obligation_id FROM workflow_contract_verification_obligations WHERE work_id=? AND contract_version=? ORDER BY law_id,obligation_id`, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var value WorkflowVerificationObligation
		if err := rows.Scan(&value.LawID, &value.ObligationID); err != nil {
			rows.Close()
			return nil, err
		}
		binding.VerificationObligations = append(binding.VerificationObligations, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return &binding, nil
}

func readWorkflowBindingStringList(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return values, nil
}
