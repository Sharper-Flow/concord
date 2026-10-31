package store

// The committed law record model: the authored metadata a law file's
// frontmatter decodes into, the independent Domain registry it resolves
// against, and the retained validators every committed law read applies.
// There is no manifest document behind these types: folder membership is the
// acceptance authority, and these validators own the record-level rules the
// law folder read enforces (law-folder-authority.md, Committed reads).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// maxManifestRecords bounds the committed law folder's file count, and the
	// other caps bound each authored record field the validators read.
	maxManifestRecords = 1000
	maxManifestArray   = 64
	maxManifestID      = 256
	maxManifestTitle   = 256
	maxManifestSummary = 4096
	maxManifestPath    = 512
	maxManifestDomains = 64
	// maxManifestRootHomeRationale bounds the claim to a stated reason rather
	// than an essay. A rationale that needs more room is describing law that
	// belongs in a child Domain.
	maxManifestRootHomeRationale = 512
	maxManifestRelations         = 64
	maxCriterionBindings         = 1000
	minCriterionExemption        = 12
	maxCriterionExemption        = 512
)

var manifestLawBearingKinds = map[string]bool{
	"constitution": true,
	"decision":     true,
	"spec":         true,
}

// manifestLawRelationSubjects is narrower than manifestLawBearingKinds: the
// law-relation graph and the domain registry's governing_law_ids are defined
// over decisions and specs. A constitution is law-bearing for status purposes
// without yet participating in that graph.
var manifestLawRelationSubjects = map[string]bool{"decision": true, "spec": true}

var lawRelationKinds = map[string]bool{
	"refines":        true,
	"subordinate_to": true,
	"conflicts_with": true,
}

type KnowledgeDomainRegistry struct {
	SchemaVersion string            `json:"schema_version"`
	ProductKey    string            `json:"product_key"`
	RootDomainID  string            `json:"root_domain_id"`
	Domains       []KnowledgeDomain `json:"domains"`
}

type KnowledgeDomain struct {
	DomainID              string                          `json:"domain_id"`
	Name                  string                          `json:"name"`
	Purpose               string                          `json:"purpose"`
	ParentDomainID        string                          `json:"parent_domain_id,omitempty"`
	Status                string                          `json:"status"`
	ArchitectureRelations []KnowledgeArchitectureRelation `json:"architecture_relations"`
	parentDomainPresent   bool
}

type KnowledgeArchitectureRelation struct {
	Kind                 string   `json:"kind"`
	TargetDomainID       string   `json:"target_domain_id"`
	GoverningLawIDs      []string `json:"governing_law_ids,omitempty"`
	State                string   `json:"state,omitempty"`
	governingLawsPresent bool
	statePresent         bool
}

// KnowledgeRecord is a bounded declaration whose path and hash identify the
// authoritative markdown blob at one commit.
type KnowledgeRecord struct {
	ID                 string                `json:"id"`
	Kind               string                `json:"kind"`
	Path               string                `json:"path"`
	Status             string                `json:"status"`
	Authority          KnowledgeAuthority    `json:"authority"`
	Date               string                `json:"date"`
	Title              string                `json:"title"`
	Summary            string                `json:"summary"`
	Tags               []string              `json:"tags"`
	Scopes             KnowledgeRecordScopes `json:"scopes"`
	SHA256             string                `json:"sha256"`
	LawRelations       []KnowledgeRelation   `json:"law_relations,omitempty"`
	HomeDomainID       string                `json:"home_domain_id,omitempty"`
	AppliesToDomainIDs []string              `json:"applies_to_domain_ids,omitempty"`
	// Evidence names implementation paths (scenarios, tests, code) that
	// carry this record's guidance. The offline validator fails when an
	// evidence path no longer exists — the structural law/implementation
	// drift audit (CD-0026).
	Evidence          []string                    `json:"evidence,omitempty"`
	CriterionBindings []KnowledgeCriterionBinding `json:"criterion_bindings,omitempty"`
	// ProductWideRationale states why this record's behavior fits no child
	// Domain. It is required when the home is the Product root and forbidden
	// otherwise. The root is the only home reachable without deciding
	// anything, so absent a stated claim a defaulted root home and a correct
	// one are indistinguishable once written.
	ProductWideRationale        string `json:"product_wide_rationale,omitempty"`
	homeDomainPresent           bool
	appliesToDomainsPresent     bool
	productWideRationalePresent bool
}

// KnowledgeAuthority records how a law-bearing statement entered Product law.
// A derived record has no legislative provenance. A legislated record names the
// approved work contract that fixed its standing.
type KnowledgeAuthority struct {
	Tier            string `json:"tier"`
	LegislatedBy    string `json:"legislated_by,omitempty"`
	ContractVersion int64  `json:"contract_version,omitempty"`
}

// KnowledgeCriterionBinding resolves one spec acceptance criterion through
// exactly one of three forms: a scenario id, a recorded exemption reason, or
// a work-item predicate reference (CD-0180) that names the outcome predicate
// of one Concord work item which discharges the criterion.
type KnowledgeCriterionBinding struct {
	Criterion   int    `json:"criterion"`
	Scenario    string `json:"scenario,omitempty"`
	Exemption   string `json:"exemption,omitempty"`
	WorkID      string `json:"work_id,omitempty"`
	PredicateID string `json:"predicate_id,omitempty"`
}

var (
	// criterionWorkIDPattern and criterionPredicateIDPattern bound the
	// predicate-reference form authoritatively, mirroring
	// contracts/concord-knowledge-index.v1.schema.json ($defs.criterionBinding).
	// The checker cannot reach the store and the store cannot reach the
	// workflow table from a Git manifest parse, so both sides validate shape
	// only; verdict-level discharge lives on the work-item side.
	criterionWorkIDPattern      = regexp.MustCompile(`^work-[0-9a-f]{8,64}$`)
	criterionPredicateIDPattern = regexp.MustCompile(`^predicate:[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type KnowledgeRelation struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
}

type KnowledgeRecordScopes struct {
	Mode       string   `json:"mode"`
	ProductIDs []string `json:"product_ids"`
	ProjectIDs []string `json:"project_ids"`
	DomainIDs  []string `json:"domain_ids"`
	TagIDs     []string `json:"tag_ids"`
}

func (domain *KnowledgeDomain) UnmarshalJSON(data []byte) error {
	type domainAlias KnowledgeDomain
	var parsed domainAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*domain = KnowledgeDomain(parsed)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["parent_domain_id"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("parent_domain_id cannot be null")
		}
		domain.parentDomainPresent = true
	}
	return nil
}

func (relation *KnowledgeArchitectureRelation) UnmarshalJSON(data []byte) error {
	type relationAlias KnowledgeArchitectureRelation
	var parsed relationAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*relation = KnowledgeArchitectureRelation(parsed)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["governing_law_ids"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("governing_law_ids cannot be null")
		}
		relation.governingLawsPresent = true
	}
	if raw, ok := fields["state"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("state cannot be null")
		}
		relation.statePresent = true
	}
	return nil
}

func (record *KnowledgeRecord) UnmarshalJSON(data []byte) error {
	type recordAlias KnowledgeRecord
	var parsed recordAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*record = KnowledgeRecord(parsed)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["home_domain_id"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("home_domain_id cannot be null")
		}
		record.homeDomainPresent = true
	}
	if raw, ok := fields["applies_to_domain_ids"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("applies_to_domain_ids cannot be null")
		}
		record.appliesToDomainsPresent = true
	}
	if raw, ok := fields["product_wide_rationale"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("product_wide_rationale cannot be null")
		}
		record.productWideRationalePresent = true
	}
	return nil
}

type knowledgeDomainGraphs struct {
	byID     map[string]KnowledgeDomain
	parent   map[string][]string
	depends  map[string][]string
	replaces map[string][]string
	relKeys  map[string]bool
	rootSeen bool
}

func validateKnowledgeDomainRegistry(registry KnowledgeDomainRegistry) error {
	if registry.SchemaVersion != "1.0" || !validProductKey(registry.ProductKey) || registry.RootDomainID != "product-root:"+registry.ProductKey || registry.Domains == nil || len(registry.Domains) > maxManifestDomains {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry root is invalid", false, "publish schema 1.0 registry metadata and a bounded domain array")
	}
	graphs := knowledgeDomainGraphs{
		byID:     make(map[string]KnowledgeDomain, len(registry.Domains)),
		parent:   map[string][]string{},
		depends:  map[string][]string{},
		replaces: map[string][]string{},
		relKeys:  map[string]bool{},
	}
	for _, domain := range registry.Domains {
		if err := validateKnowledgeDomainRecord(domain, registry.RootDomainID, &graphs); err != nil {
			return err
		}
	}
	if !graphs.rootSeen {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry root domain is not declared", false, "declare the current parentless root domain")
	}
	if err := validateKnowledgeDomainReferences(registry, graphs.byID); err != nil {
		return err
	}
	if graphHasCycle(graphs.parent) || graphHasCycle(graphs.depends) || graphHasCycle(graphs.replaces) {
		return newFailure(KindCycleDetected, "parse_knowledge_manifest", "domain architecture graph contains a cycle", false, "remove cycles from hierarchy, dependency, or replacement relations")
	}
	return nil
}

// validateKnowledgeDomainRecord validates one domain record and records its
// parent edge and architecture relations into the walk's graphs.
func validateKnowledgeDomainRecord(domain KnowledgeDomain, rootDomainID string, graphs *knowledgeDomainGraphs) error {
	if !validManifestID(domain.DomainID) || domain.Name == "" || utf8.RuneCountInString(domain.Name) > maxManifestTitle || strings.TrimSpace(domain.Name) != domain.Name || domain.Purpose == "" || utf8.RuneCountInString(domain.Purpose) > maxManifestSummary || strings.TrimSpace(domain.Purpose) != domain.Purpose || (domain.Status != "current" && domain.Status != "deprecated") || domain.ArchitectureRelations == nil || len(domain.ArchitectureRelations) > maxManifestRelations {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain record is invalid", false, "publish bounded domain metadata and architecture relations")
	}
	if _, exists := graphs.byID[domain.DomainID]; exists {
		return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "domain registry contains duplicate domain IDs", false, "declare each domain once")
	}
	graphs.byID[domain.DomainID] = domain
	if domain.DomainID == rootDomainID {
		graphs.rootSeen = true
		if domain.Status != "current" || domain.parentDomainPresent || domain.ParentDomainID != "" {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry root must be current and parentless", false, "make the product root a current parentless domain")
		}
	}
	if domain.ParentDomainID != "" || domain.parentDomainPresent {
		if !validManifestID(domain.ParentDomainID) || domain.ParentDomainID == domain.DomainID {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain parent is invalid or self-referential", false, "reference a distinct domain in the same registry")
		}
		graphs.parent[domain.DomainID] = append(graphs.parent[domain.DomainID], domain.ParentDomainID)
	}
	for _, relation := range domain.ArchitectureRelations {
		if err := validateKnowledgeDomainRelation(domain.DomainID, relation, graphs); err != nil {
			return err
		}
	}
	return nil
}

// validateKnowledgeDomainRelation validates one architecture relation and
// records its edge into the walk's graphs.
func validateKnowledgeDomainRelation(domainID string, relation KnowledgeArchitectureRelation, graphs *knowledgeDomainGraphs) error {
	if !validManifestID(relation.TargetDomainID) || relation.TargetDomainID == domainID {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation target is invalid or self-referential", false, "reference a distinct domain in the same registry")
	}
	if relation.Kind != "depends_on" && relation.Kind != "shares_contract_with" && relation.Kind != "replaces" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation kind is not closed", false, "use depends_on, shares_contract_with, or replaces")
	}
	if relation.Kind != "replaces" && (relation.State != "" || relation.statePresent) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation state is only valid for replaces", false, "omit state except on replacement relations")
	}
	key := relation.Kind + "\x00" + domainID + "\x00" + relation.TargetDomainID
	switch relation.Kind {
	case "depends_on":
		if len(relation.GoverningLawIDs) == 0 {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "depends_on requires non-empty governing law IDs", false, "name current accepted laws governing the dependency")
		}
		graphs.depends[domainID] = append(graphs.depends[domainID], relation.TargetDomainID)
	case "shares_contract_with":
		if relation.TargetDomainID < domainID {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "shares_contract_with must use its canonical ordered pair", false, "author the lower domain ID as the relation source")
		}
		if len(relation.GoverningLawIDs) == 0 {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "shares_contract_with requires non-empty governing law IDs", false, "name current accepted laws governing the shared contract")
		}
		key = relation.Kind + "\x00" + domainID + "\x00" + relation.TargetDomainID
	case "replaces":
		if relation.governingLawsPresent || relation.GoverningLawIDs != nil {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "replaces cannot carry governing law IDs", false, "omit governing_law_ids from replacement relations")
		}
		if !relation.statePresent && relation.State == "" || relation.State != "declared" && relation.State != "building" && relation.State != "coexisting" && relation.State != "cutover" && relation.State != "retired" {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "replaces requires a closed state", false, "declare the replacement lifecycle state")
		}
		graphs.replaces[domainID] = append(graphs.replaces[domainID], relation.TargetDomainID)
	}
	if graphs.relKeys[key] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation is duplicated, including reverse shares_contract_with", false, "declare each architecture relation once")
	}
	graphs.relKeys[key] = true
	return validateOptionalManifestIDs(relation.GoverningLawIDs, "governing_law_ids")
}

// validateKnowledgeDomainReferences refuses dangling parent and relation
// targets, and dependency relations that point at a deprecated domain.
func validateKnowledgeDomainReferences(registry KnowledgeDomainRegistry, byID map[string]KnowledgeDomain) error {
	for _, domain := range registry.Domains {
		if domain.ParentDomainID != "" || domain.parentDomainPresent {
			if _, ok := byID[domain.ParentDomainID]; !ok {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain parent is dangling", false, "reference a domain declared in this registry")
			}
		}
		for _, relation := range domain.ArchitectureRelations {
			target, ok := byID[relation.TargetDomainID]
			if !ok {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation target is dangling", false, "reference a domain declared in this registry")
			}
			if relation.Kind == "depends_on" && target.Status != "current" {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "depends_on target must be current", false, "reference a current domain in dependency relations")
			}
		}
	}
	return nil
}

func validProductKey(value string) bool {
	if len(value) < 2 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value[1:] {
		if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func validManifestID(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= maxManifestID && strings.TrimSpace(value) == value
}

func validateManifestStringArray(values []string, field string) error {
	if len(values) > maxManifestArray {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" exceeds the bounded array size", false, "use a bounded unique ID array")
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || utf8.RuneCountInString(value) > maxManifestID || strings.TrimSpace(value) != value || seen[value] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" contains an empty, oversized, or duplicate value", false, "use bounded unique IDs")
		}
		seen[value] = true
	}
	return nil
}

func validateOptionalManifestIDs(values []string, field string) error {
	if values == nil {
		return nil
	}
	return validateManifestStringArray(values, field)
}

func validateKnowledgeDomainLawReferences(registry KnowledgeDomainRegistry, records []KnowledgeRecord) error {
	domainIDs := make(map[string]bool, len(registry.Domains))
	for _, domain := range registry.Domains {
		domainIDs[domain.DomainID] = true
	}
	acceptedLaws := map[string]bool{}
	for _, record := range records {
		if manifestLawRelationSubjects[record.Kind] && record.Status == "accepted" {
			acceptedLaws[record.ID] = true
		}
	}
	for _, domain := range registry.Domains {
		for _, relation := range domain.ArchitectureRelations {
			if relation.Kind != "depends_on" && relation.Kind != "shares_contract_with" {
				continue
			}
			for _, lawID := range relation.GoverningLawIDs {
				if !acceptedLaws[lawID] {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation governing law is not a current accepted law", false, "reference an accepted decision or spec in the same manifest")
				}
			}
		}
	}
	for _, record := range records {
		for _, domainID := range record.Scopes.DomainIDs {
			if !domainIDs[domainID] {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "knowledge scope domain is dangling", false, "reference a domain declared in the registry")
			}
		}
		for _, domainID := range record.AppliesToDomainIDs {
			if !domainIDs[domainID] {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law applies_to domain is dangling", false, "reference a domain declared in the registry")
			}
		}
	}
	return nil
}

// validateManifestRootHomeClaim holds the asymmetry between the root Domain
// and every child. A child home has already decided, so the record needs no
// further statement. The root has not: CD-0222 D2 makes it correct for
// Product-wide law and simultaneously the only home an author reaches by
// deciding nothing. Requiring the claim in the record is what separates the
// two cases after the fact, which no later reader can otherwise do.
func validateManifestRootHomeClaim(record KnowledgeRecord, hasHome bool, registry KnowledgeDomainRegistry) error {
	rationale := strings.TrimSpace(record.ProductWideRationale)
	rootHome := hasHome && registry.RootDomainID != "" && record.HomeDomainID == registry.RootDomainID
	if !rootHome {
		if record.productWideRationalePresent || record.ProductWideRationale != "" {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "only law homed to the root Domain states a product-wide rationale", false, "remove product_wide_rationale from a child-homed record")
		}
		return nil
	}
	if rationale == "" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law homed to the root Domain must state why no child Domain owns it", false, "author product_wide_rationale, or home the record to the child Domain whose behavior it governs")
	}
	if len(rationale) > maxManifestRootHomeRationale {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "product-wide rationale is too long", false, "state the reason in one or two sentences")
	}
	return nil
}

func validateManifestLawHome(record KnowledgeRecord, registry KnowledgeDomainRegistry) error {
	if !manifestLawBearingKinds[record.Kind] {
		if record.HomeDomainID != "" || len(record.AppliesToDomainIDs) > 0 || record.homeDomainPresent || record.appliesToDomainsPresent || record.ProductWideRationale != "" || record.productWideRationalePresent {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "non-law records cannot author domain law-home fields", false, "keep domain law-home fields on law-bearing records only")
		}
		return nil
	}
	hasHome := record.homeDomainPresent || record.HomeDomainID != ""
	if err := validateManifestRootHomeClaim(record, hasHome, registry); err != nil {
		return err
	}
	if record.Status == "accepted" && !hasHome {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "an accepted law-bearing record requires exactly one home domain", false, "author one home_domain_id")
	}
	if hasHome && !validManifestID(record.HomeDomainID) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law home domain is invalid", false, "reference one clean domain ID")
	}
	if hasHome && !domainRegistryHas(registry, record.HomeDomainID) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law home domain is dangling", false, "reference a domain declared in the registry")
	}
	if record.AppliesToDomainIDs != nil || record.appliesToDomainsPresent {
		if !hasHome {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law applicability requires an authored home domain", false, "author home_domain_id before applies_to_domain_ids")
		}
		if err := validateManifestStringArray(record.AppliesToDomainIDs, "applies_to_domain_ids"); err != nil {
			return err
		}
		for _, domainID := range record.AppliesToDomainIDs {
			if domainID == record.HomeDomainID {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law applies_to domains repeat the home domain", false, "omit the home domain from applies_to_domain_ids")
			}
		}
	}
	return nil
}

func domainRegistryHas(registry KnowledgeDomainRegistry, id string) bool {
	for _, domain := range registry.Domains {
		if domain.DomainID == id {
			return true
		}
	}
	return false
}

func validateManifestRecordRelations(record KnowledgeRecord, byID map[string]KnowledgeRecord, seen map[string]bool, graph map[string][]string) error {
	if len(record.LawRelations) == 0 {
		return nil
	}
	if !manifestLawRelationSubjects[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law_relations are only allowed on decision/spec records", false, "publish authored relations on a decision or spec")
	}
	for _, relation := range record.LawRelations {
		if !lawRelationKinds[relation.Kind] || relation.TargetID == "" {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation kind or target is invalid", false, "use one closed relation kind and a distinct law ID")
		}
		if relation.TargetID == record.ID {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation is a self-edge", false, "use one closed relation kind and a distinct law ID")
		}
		target, ok := byID[relation.TargetID]
		if !ok {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation target is not a declared decision/spec record", false, "reference a decision or spec in the same manifest")
		}
		if !manifestLawRelationSubjects[target.Kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation target is not a declared decision/spec record", false, "reference a decision or spec in the same manifest")
		}
		key := relation.Kind + "\x00" + record.ID + "\x00" + relation.TargetID
		if relation.Kind == "conflicts_with" {
			left, right := record.ID, relation.TargetID
			if left > right {
				left, right = right, left
			}
			key = relation.Kind + "\x00" + left + "\x00" + right
		}
		if seen[key] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation is duplicated, including a reverse conflict declaration", false, "declare each typed law relation once")
		}
		seen[key] = true
		switch relation.Kind {
		case "refines", "subordinate_to":
			graph[record.ID] = append(graph[record.ID], relation.TargetID)
		}
	}
	return nil
}

func graphHasCycle(graph map[string][]string) bool {
	state := map[string]uint8{}
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == 1 {
			return true
		}
		if state[node] == 2 {
			return false
		}
		state[node] = 1
		for _, target := range graph[node] {
			if visit(target) {
				return true
			}
		}
		state[node] = 2
		return false
	}
	for node := range graph {
		if visit(node) {
			return true
		}
	}
	return false
}

func validateKnowledgeCriterionBindings(record KnowledgeRecord) error {
	if len(record.CriterionBindings) > maxCriterionBindings {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record carries too many criterion bindings", false, "supply at most one thousand criterion bindings")
	}
	seenCriteria := map[int]bool{}
	for _, binding := range record.CriterionBindings {
		if record.Kind != "spec" {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion bindings are only valid on spec records", false, "move criterion bindings to a spec record")
		}
		if binding.Criterion < 1 || seenCriteria[binding.Criterion] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion binding index is invalid or duplicated", false, "use one positive index for each criterion")
		}
		seenCriteria[binding.Criterion] = true
		if err := validateKnowledgeCriterionBindingForm(binding); err != nil {
			return err
		}
	}
	return nil
}

func validateKnowledgeCriterionBindingForm(binding KnowledgeCriterionBinding) error {
	forms := 0
	if binding.Scenario != "" {
		forms++
	}
	if binding.Exemption != "" {
		forms++
	}
	if binding.WorkID != "" || binding.PredicateID != "" {
		forms++
	}
	halfPredicate := (binding.WorkID == "") != (binding.PredicateID == "")
	if forms != 1 || halfPredicate {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion binding must carry exactly one scenario, exemption, or work predicate", false, "bind the criterion to a scenario, record an exemption, or reference one work-item predicate")
	}
	if binding.Scenario != "" && !validManifestID(binding.Scenario) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion scenario is empty, oversized, or not clean", false, "use a bounded scenario ID")
	}
	if binding.Exemption != "" && (utf8.RuneCountInString(binding.Exemption) < minCriterionExemption || utf8.RuneCountInString(binding.Exemption) > maxCriterionExemption || strings.TrimSpace(binding.Exemption) != binding.Exemption) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion exemption is not a bounded reason", false, "use a trimmed exemption reason of twelve to five hundred twelve characters")
	}
	if binding.WorkID != "" && (len(binding.WorkID) > maxManifestID || !criterionWorkIDPattern.MatchString(binding.WorkID)) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion binding work id is not a bounded work- id", false, "reference a Concord work item with the work- id form")
	}
	if binding.PredicateID != "" && (len(binding.PredicateID) > maxManifestID || !criterionPredicateIDPattern.MatchString(binding.PredicateID)) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion binding predicate id is not a predicate- prefixed id", false, "reference one outcome predicate with the predicate: prefix")
	}
	return nil
}

func validateKnowledgeRecordAuthority(record KnowledgeRecord) error {
	if record.Authority.Tier != "legislated" && record.Authority.Tier != "derived" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record authority tier is not closed", false, "use legislated or derived")
	}
	if record.Authority.Tier == "legislated" {
		if len(record.Authority.LegislatedBy) > maxManifestID || !criterionWorkIDPattern.MatchString(record.Authority.LegislatedBy) || record.Authority.ContractVersion < 1 {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "legislated record authority is incomplete", false, "name the approving work with the work- id form and a positive contract version")
		}
		if err := validateKnowledgeAuthorityDefault(record); err != nil {
			return err
		}
	} else if record.Authority.LegislatedBy != "" || record.Authority.ContractVersion != 0 {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "derived record authority carries legislative fields", false, "remove legislated_by and contract_version")
	}
	return nil
}

func domainRegistryContentHash(registry KnowledgeDomainRegistry) string {
	normalized := KnowledgeDomainRegistry{
		SchemaVersion: registry.SchemaVersion,
		ProductKey:    registry.ProductKey,
		RootDomainID:  registry.RootDomainID,
		Domains:       make([]KnowledgeDomain, len(registry.Domains)),
	}
	for index, domain := range registry.Domains {
		normalized.Domains[index] = domain
		normalized.Domains[index].ArchitectureRelations = make([]KnowledgeArchitectureRelation, len(domain.ArchitectureRelations))
		for relationIndex, relation := range domain.ArchitectureRelations {
			normalizedRelation := relation
			normalizedRelation.GoverningLawIDs = append([]string(nil), relation.GoverningLawIDs...)
			sort.Strings(normalizedRelation.GoverningLawIDs)
			normalized.Domains[index].ArchitectureRelations[relationIndex] = normalizedRelation
		}
		sort.Slice(normalized.Domains[index].ArchitectureRelations, func(left, right int) bool {
			a, b := normalized.Domains[index].ArchitectureRelations[left], normalized.Domains[index].ArchitectureRelations[right]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.TargetDomainID != b.TargetDomainID {
				return a.TargetDomainID < b.TargetDomainID
			}
			return a.State < b.State
		})
	}
	sort.Slice(normalized.Domains, func(left, right int) bool {
		return normalized.Domains[left].DomainID < normalized.Domains[right].DomainID
	})
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}
