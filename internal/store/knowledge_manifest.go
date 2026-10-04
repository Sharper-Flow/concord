package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	knowledgeManifestPath = "docs/concord-knowledge-index.v1.json"
	// CD-0194 D5: a revision composes and validates under the record path
	// prefix its layout tier carries. Authoring is always the current tier.
	manifestRecordPathPrefix             = ".concord/docs/"
	preMigrationManifestRecordPathPrefix = "docs/"
	// CD-0194 D2: an operator override is the only route that admits a
	// Product knowledge location outside the layout tier's prefix. The
	// bounded array and closed path shape mirror $defs.operatorOverrides in
	// contracts/concord-knowledge-index.v1.schema.json.
	maxOperatorOverrides = 32
	maxOverrideReason    = 512
	minOverrideReason    = 12
	// maxKnowledgeRecord bounds one record shard. The caps test holds it
	// against the per-field record caps, and the shard reader refuses an
	// oversized shard while naming its path.
	maxKnowledgeRecord = 16 * 1024
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
	// knowledgeManifestHeadAllowance bounds the head shard, the domain
	// registry shard, and the composed record-list encoding overhead, none of
	// which grow with the record corpus.
	knowledgeManifestHeadAllowance = 256 * 1024
	// maxKnowledgeManifest is derived from the record bound, never hand-set:
	// the composed manifest carries at most maxManifestRecords record shards
	// of maxKnowledgeRecord each plus one head allowance. The derivation test
	// refuses any other value.
	maxKnowledgeManifest = maxManifestRecords*maxKnowledgeRecord + knowledgeManifestHeadAllowance

	// knowledgeManifestSchemaLegacy is the version published before CD-0159
	// gave a law record an authority tier. Its records declare no authority
	// object, so the parser supplies the derived tier and the conflict gate
	// treats that corpus exactly as it did before the tier existed.
	knowledgeManifestSchemaLegacy = "1.2"

	// knowledgeManifestSchemaCurrent is the version that carries the authority
	// tier as a required record field. A corpus adopts it by classifying every
	// record, which is the moment the tier starts to protect that Product.
	knowledgeManifestSchemaCurrent = "1.3"
)

// knowledgeManifestSchemaAccepted reports whether the parser reads a manifest
// at this declared version. The set is bounded and closed: an unknown version
// is refused rather than guessed at, because the version is the only signal
// that distinguishes a corpus carrying authority tiers from one that predates
// them.
func knowledgeManifestSchemaAccepted(version string) bool {
	return version == knowledgeManifestSchemaLegacy || version == knowledgeManifestSchemaCurrent
}

var knowledgeKindsClosed = map[string]bool{
	"work_note":    true,
	"constitution": true,
	"decision":     true,
	"spec":         true,
	"lesson":       true,
	"reference":    true,
	"research":     true,
}

// sortedKnowledgeKinds returns the closed knowledge vocabulary in a stable
// order for filters, coverage rows, and refusal text.
func sortedKnowledgeKinds() []string {
	kinds := make([]string, 0, len(knowledgeKindsClosed))
	for kind := range knowledgeKindsClosed {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// manifestRecordKinds is every kind a manifest record may declare. work_note is
// a supported knowledge kind that no record carries, so it is absent here.
var manifestRecordKinds = map[string]bool{
	"constitution": true,
	"decision":     true,
	"spec":         true,
	"lesson":       true,
	"reference":    true,
	"research":     true,
}

// manifestLawBearingKinds splits the record kinds into the two status tiers the
// schema declares. A law-bearing record is accepted or superseded; every other
// record kind is published or superseded. The tier decides the status, the
// law-home requirement, and the status a successor must carry.
var manifestLawBearingKinds = map[string]bool{
	"constitution": true,
	"decision":     true,
	"spec":         true,
}

// manifestLawRelationSubjects is narrower than manifestLawBearingKinds: the
// law-relation graph, the domain registry's governing_law_ids, and the
// supersedes symmetry rule are all defined over decisions and specs. A
// constitution is law-bearing for status purposes without yet participating in
// that graph.
var manifestLawRelationSubjects = map[string]bool{"decision": true, "spec": true}

// manifestRootKeys is the declared top-level vocabulary of the knowledge
// manifest contract. The value records whether this package projects the key
// onto a KnowledgeManifest field. A false value marks repository policy the
// store does not interpret, such as the prose doc contract; the head shard
// carries it and the store never rewrites that shard (CD-0114).
// TestKnowledgeManifestVocabularyMatchesSchema binds this set to
// contracts/concord-knowledge-index.v1.schema.json.
//
// The reader is additive (CD-0177): a key outside this vocabulary is dropped
// at parse instead of refusing the document, so a release-pinned core reads a
// manifest authored after its release. Authoring stays closed behind the JSON
// Schema and scripts/check-knowledge-index.py, and
// TestCommittedManifestCarriesNoFieldTheModelDrops keeps the committed shard
// tree inside the modeled vocabulary. A field that must restrict older cores
// needs a schema_version bump, which stays the only closed-version signal.
var manifestRootKeys = map[string]bool{
	"schema_version":     true,
	"supported_kinds":    true,
	"indexed_kinds":      true,
	"domain_registry":    true,
	"records":            true,
	"dispositions":       true,
	"knowledge_roots":    true,
	"exclusions":         true,
	"operator_overrides": true,
	"doc_contract":       false,
}

var lawRelationKinds = map[string]bool{
	"supersedes":     true,
	"refines":        true,
	"subordinate_to": true,
	"conflicts_with": true,
}

// KnowledgeManifest is the one tracked registry for non-work-note durable
// knowledge. It contains metadata and proofs, never document bodies.
type KnowledgeManifest struct {
	SchemaVersion         string                      `json:"schema_version"`
	SupportedKinds        []string                    `json:"supported_kinds"`
	IndexedKinds          []string                    `json:"indexed_kinds"`
	DomainRegistry        KnowledgeDomainRegistry     `json:"domain_registry"`
	KnowledgeRoots        []string                    `json:"knowledge_roots,omitempty"`
	Exclusions            []string                    `json:"exclusions,omitempty"`
	OperatorOverrides     []KnowledgeOperatorOverride `json:"operator_overrides,omitempty"`
	DocContract           *KnowledgeDocContract       `json:"doc_contract,omitempty"`
	Records               []KnowledgeRecord           `json:"records"`
	Dispositions          []KnowledgeDisposition      `json:"dispositions"`
	domainRegistryPresent bool
}

// KnowledgeOperatorOverride is one recorded operator instruction that admits a
// Product knowledge location outside the layout tier's record prefix
// (CD-0194 D2). RecordedIn names the accepted decision whose document carries
// the closed instruction block; the parse proves shape and Product ownership,
// and the anchor gate on every committed or working-tree manifest read, beside
// the placement check, proves that anchor against its document.
type KnowledgeOperatorOverride struct {
	Path       string `json:"path"`
	ProductID  string `json:"product_id"`
	RecordedIn string `json:"recorded_in"`
	Reason     string `json:"reason"`
}

type KnowledgeDocContract struct {
	Enforced      bool                      `json:"enforced"`
	Spec          *KnowledgeDocContractSpec `json:"spec,omitempty"`
	Decision      *KnowledgeDocContractSpec `json:"decision,omitempty"`
	BannedPhrases []string                  `json:"banned_phrases,omitempty"`
}

type KnowledgeDocContractSpec struct {
	RequiredSections []string `json:"required_sections"`
	ACRequired       bool     `json:"ac_required"`
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
	Successor          string                `json:"successor,omitempty"`
	SHA256             string                `json:"sha256"`
	LawRelations       []KnowledgeRelation   `json:"law_relations,omitempty"`
	HomeDomainID       string                `json:"home_domain_id,omitempty"`
	AppliesToDomainIDs []string              `json:"applies_to_domain_ids,omitempty"`
	// DocContractProfile carries the authored CD-0175 decision outline
	// generation ("legacy" or "current"). The store never interprets it; it is
	// carried so a re-marshaled manifest keeps the authored value.
	DocContractProfile string `json:"doc_contract_profile,omitempty"`
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

// KnowledgeDisposition records source material the operator has decided not to
// formalize. It is the opposite of a record: a record makes a document
// knowledge, a disposition states that the document will never become
// knowledge and why. The two are mutually exclusive over a path, so a document
// cannot be answered with both a law state and a refusal to give it one.
type KnowledgeDisposition struct {
	Path        string `json:"path"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

// KnowledgeRelation is authored in the Git knowledge manifest. It is never a
// source of precedence by itself; conflicts_with records an unresolved pair.
// A relation whose target lives in another manifest names that manifest's
// source Project in SourceProjectID (CD-0200); a target inside the same
// manifest leaves the field empty.
type KnowledgeRelation struct {
	Kind            string `json:"kind"`
	TargetID        string `json:"target_id"`
	SourceProjectID string `json:"source_project_id,omitempty"`
}

type KnowledgeRecordScopes struct {
	Mode       string   `json:"mode"`
	ProductIDs []string `json:"product_ids"`
	ProjectIDs []string `json:"project_ids"`
	DomainIDs  []string `json:"domain_ids"`
	TagIDs     []string `json:"tag_ids"`
}

func (manifest *KnowledgeManifest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	// Additive read (CD-0177): only declared, projected keys reach the model;
	// an undeclared key, and a declared key the store does not interpret, are
	// dropped rather than refused.
	modeled := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		if projected := manifestRootKeys[key]; projected {
			modeled[key] = value
		}
	}
	body, err := json.Marshal(modeled)
	if err != nil {
		return err
	}
	type manifestAlias KnowledgeManifest
	var parsed manifestAlias
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*manifest = KnowledgeManifest(parsed)
	_, manifest.domainRegistryPresent = fields["domain_registry"]
	return nil
}

func (disposition *KnowledgeDisposition) UnmarshalJSON(data []byte) error {
	type dispositionAlias KnowledgeDisposition
	var parsed dispositionAlias
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*disposition = KnowledgeDisposition(parsed)
	return nil
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

func (manifest KnowledgeManifest) MarshalJSON() ([]byte, error) {
	type manifestJSON struct {
		SchemaVersion     string                      `json:"schema_version"`
		SupportedKinds    []string                    `json:"supported_kinds"`
		IndexedKinds      []string                    `json:"indexed_kinds"`
		DomainRegistry    *KnowledgeDomainRegistry    `json:"domain_registry,omitempty"`
		KnowledgeRoots    []string                    `json:"knowledge_roots,omitempty"`
		Exclusions        []string                    `json:"exclusions,omitempty"`
		OperatorOverrides []KnowledgeOperatorOverride `json:"operator_overrides,omitempty"`
		DocContract       *KnowledgeDocContract       `json:"doc_contract,omitempty"`
		Records           []map[string]any            `json:"records"`
	}
	registry := manifest.DomainRegistry
	records := make([]map[string]any, 0, len(manifest.Records))
	for _, record := range manifest.Records {
		records = append(records, manifestRecordEntry(record))
	}
	return json.Marshal(manifestJSON{
		SchemaVersion: manifest.SchemaVersion, SupportedKinds: manifest.SupportedKinds,
		IndexedKinds: manifest.IndexedKinds, DomainRegistry: &registry,
		KnowledgeRoots: manifest.KnowledgeRoots, Exclusions: manifest.Exclusions,
		OperatorOverrides: manifest.OperatorOverrides,
		DocContract:       manifest.DocContract, Records: records,
	})
}

func manifestRecordEntry(record KnowledgeRecord) map[string]any {
	entry := map[string]any{
		"id": record.ID, "kind": record.Kind, "path": record.Path, "status": record.Status,
		"authority": record.Authority,
		"date":      record.Date, "title": record.Title, "summary": record.Summary,
		"tags": record.Tags, "scopes": manifestScopeEntry(record.Scopes), "sha256": record.SHA256,
	}
	if record.Successor != "" {
		entry["successor"] = record.Successor
	}
	if len(record.LawRelations) > 0 {
		relations := make([]map[string]string, 0, len(record.LawRelations))
		for _, relation := range record.LawRelations {
			entry := map[string]string{"kind": relation.Kind, "target_id": relation.TargetID}
			if relation.SourceProjectID != "" {
				// CD-0200: a cross-source target names its source Project
				// through the structured field, so the committed shard
				// carries the relation's full endpoint identity.
				entry["source_project_id"] = relation.SourceProjectID
			}
			relations = append(relations, entry)
		}
		entry["law_relations"] = relations
	}
	if len(record.Evidence) > 0 {
		entry["evidence"] = record.Evidence
	}
	if record.HomeDomainID != "" {
		entry["home_domain_id"] = record.HomeDomainID
	}
	if record.DocContractProfile != "" {
		entry["doc_contract_profile"] = record.DocContractProfile
	}
	if len(record.AppliesToDomainIDs) > 0 {
		entry["applies_to_domain_ids"] = record.AppliesToDomainIDs
	}
	if record.ProductWideRationale != "" {
		entry["product_wide_rationale"] = record.ProductWideRationale
	}
	if len(record.CriterionBindings) > 0 {
		entry["criterion_bindings"] = record.CriterionBindings
	}
	return entry
}

func manifestScopeEntry(scopes KnowledgeRecordScopes) map[string]any {
	return map[string]any{
		"mode": scopes.Mode, "product_ids": scopes.ProductIDs, "project_ids": scopes.ProjectIDs,
		"domain_ids": scopes.DomainIDs, "tag_ids": scopes.TagIDs,
	}
}

func knowledgeDomainRegistryZero(registry KnowledgeDomainRegistry) bool {
	return registry.SchemaVersion == "" && registry.ProductKey == "" && registry.RootDomainID == "" && registry.Domains == nil
}

func parseKnowledgeManifest(data []byte) (KnowledgeManifest, error) {
	return parseKnowledgeManifestForRole(data, manifestRecordPathPrefix, manifestSharedHomeRole)
}

func parseKnowledgeManifestForRole(data []byte, pathPrefix string, role knowledgeManifestRole) (KnowledgeManifest, error) {
	if len(data) == 0 || len(data) > maxKnowledgeManifest {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest is empty or exceeds the bounded size", false, "publish a bounded v1 manifest")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest contains duplicate JSON keys", false, "remove duplicate keys from the manifest")
	}
	var manifest KnowledgeManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&manifest); err != nil {
		return KnowledgeManifest{}, wrapFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest is not valid v1 JSON", false, "publish well-formed v1 manifest JSON", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest contains trailing JSON values", false, "publish exactly one JSON object")
	}
	applyLegacyAuthorityTier(&manifest)
	if err := validateKnowledgeManifestForRole(manifest, pathPrefix, role); err != nil {
		return KnowledgeManifest{}, err
	}
	return manifest, nil
}

// applyLegacyAuthorityTier gives a schema 1.2 record the derived tier when it
// declares none. CD-0159 arrived after 1.2 was published, so a corpus authored
// against 1.2 carries no authority object and cannot be classified by reading
// it. Derived is the tier that reproduces the admission the conflict gate had
// before the tier existed, so a 1.2 corpus keeps the behavior it already had
// rather than gaining or losing a protection its records never declared.
//
// The default is applied here, on the manifest this function returns, because
// validateKnowledgeRecordForSchema takes its record by value and law_subjects
// constrains the projected tier to a closed two-value set.
func applyLegacyAuthorityTier(manifest *KnowledgeManifest) {
	if manifest.SchemaVersion != knowledgeManifestSchemaLegacy {
		return
	}
	for i := range manifest.Records {
		if manifest.Records[i].Authority.Tier == "" {
			manifest.Records[i].Authority = KnowledgeAuthority{Tier: "derived"}
		}
	}
}

func validateKnowledgeManifest(manifest KnowledgeManifest) error {
	return validateKnowledgeManifestForRole(manifest, manifestRecordPathPrefix, manifestSharedHomeRole)
}

// knowledgeManifestRole names where a manifest sits in a federated Product
// (CD-0200). The shared-law home role keeps the registry-required validation.
// A registered source role refuses a local Domain registry — only the
// shared-law home carries one — and defers its Domain-ID checks to the
// rebuild, which validates them against the home's registry.
type knowledgeManifestRole int

const (
	manifestSharedHomeRole knowledgeManifestRole = iota
	manifestRegisteredSourceRole
)

func validateKnowledgeManifestForRole(manifest KnowledgeManifest, pathPrefix string, role knowledgeManifestRole) error {
	if !knowledgeManifestSchemaAccepted(manifest.SchemaVersion) || manifest.SupportedKinds == nil || manifest.IndexedKinds == nil || manifest.Records == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest schema version or required root fields are invalid", false, "publish strict schema 1.2 or 1.3 root fields")
	}
	hasRegistry := manifest.domainRegistryPresent || !knowledgeDomainRegistryZero(manifest.DomainRegistry)
	covered := func(string) bool { return false }
	if role == manifestRegisteredSourceRole {
		if hasRegistry {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "only the shared-law home carries the Domain registry", false, "remove the registry from this source manifest and reference the shared home's Domain IDs")
		}
		if len(manifest.OperatorOverrides) > 0 {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator overrides belong to the shared-law home manifest", false, "record CD-0194 placement exceptions in the shared home's manifest")
		}
	} else {
		if !hasRegistry {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "schema 1.2 requires a domain registry", false, "publish the bounded domain registry")
		}
		if err := validateKnowledgeDomainRegistry(manifest.DomainRegistry); err != nil {
			return err
		}
		resolved, err := validatedOverrideCoverage(manifest.OperatorOverrides, manifest.DomainRegistry.ProductKey)
		if err != nil {
			return err
		}
		covered = resolved
	}
	supported, err := validateManifestKindList(manifest.SupportedKinds, "supported_kinds")
	if err != nil {
		return err
	}
	indexed, err := validateManifestKindList(manifest.IndexedKinds, "indexed_kinds")
	if err != nil {
		return err
	}
	for kind := range indexed {
		if kind != "work_note" && !manifestRecordKinds[kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "indexed_kinds contains a kind without manifest record support: "+kind, false, "index only kinds a record may declare")
		}
		if !supported[kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "indexed kind is not supported: "+kind, false, "include every indexed kind in supported_kinds")
		}
	}
	if len(manifest.Records) > maxManifestRecords {
		return newFailure(KindKnowledgeIndexIncomplete, "parse_knowledge_manifest", "manifest contains too many records", true, "split the knowledge authority into bounded homes")
	}
	ids := map[string]bool{}
	paths := map[string]bool{}
	for _, record := range manifest.Records {
		if err := validateKnowledgeRecordForSchema(record, supported, indexed, manifest.SchemaVersion, pathPrefix, covered); err != nil {
			return err
		}
		if strings.Contains(record.ID, "/") {
			// CD-0200: the qualified reference form "project_id/law_id"
			// reserves '/', so a law ID containing one could never be named
			// unambiguously. The rebuild refuses it for work notes too.
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "stable ID contains '/', which the source-qualified reference form reserves", false, "publish a stable ID without '/'")
		}
		if role == manifestSharedHomeRole {
			if err := validateManifestLawHome(record, manifest.DomainRegistry); err != nil {
				return err
			}
		}
		if ids[record.ID] {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "manifest contains duplicate stable IDs", false, "assign one stable ID to one canonical record")
		}
		if paths[record.Path] {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "manifest contains duplicate canonical paths", false, "assign one canonical path to one record")
		}
		ids[record.ID], paths[record.Path] = true, true
	}
	if err := validateManifestDispositions(manifest.Dispositions, paths); err != nil {
		return err
	}
	if err := validateManifestSuccessors(manifest.Records); err != nil {
		return err
	}
	if err := validateManifestRelations(manifest); err != nil {
		return err
	}
	if role == manifestSharedHomeRole {
		if err := validateKnowledgeDomainLawReferences(manifest.DomainRegistry, manifest.Records); err != nil {
			return err
		}
	}
	return nil
}

// knowledgeDomainGraphs collects the parent, dependency, and replacement
// edges a registry validation walk builds.
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
	if relationGraphHasCycle(graphs.parent) || relationGraphHasCycle(graphs.depends) || relationGraphHasCycle(graphs.replaces) {
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
// further statement. The root has not: CD-0041 D2 makes it correct for
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

func validateManifestRelations(manifest KnowledgeManifest) error {
	byID := make(map[string]KnowledgeRecord, len(manifest.Records))
	for _, record := range manifest.Records {
		byID[record.ID] = record
	}
	seen := map[string]bool{}
	graph := map[string][]string{}
	for _, record := range manifest.Records {
		if len(record.LawRelations) == 0 {
			continue
		}
		if !manifestLawRelationSubjects[record.Kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law_relations are only allowed on decision/spec records", false, "publish authored relations on a decision or spec")
		}
		for _, relation := range record.LawRelations {
			if !lawRelationKinds[relation.Kind] || relation.TargetID == "" {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation kind or target is invalid", false, "use one closed relation kind and a distinct law ID")
			}
			// The self-edge refusal compares same-manifest endpoints. A
			// relation carrying an explicit source_project_id names another
			// source's node, so a bare ID both sources hold is not a
			// self-edge; the rebuild refuses a qualified target that names
			// the declaring source's own Project (CD-0200 source-qualified
			// identity).
			if relation.SourceProjectID == "" && relation.TargetID == record.ID {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation is a self-edge", false, "use one closed relation kind and a distinct law ID")
			}
			if strings.Contains(relation.TargetID, "/") {
				// A cross-source target is named by the structured
				// source_project_id field, never by packing the qualified
				// string form into target_id (CD-0200).
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation target contains '/'; name the target source with source_project_id instead", false, "keep target_id a bare law ID and declare source_project_id")
			}
			target, ok := byID[relation.TargetID]
			if relation.SourceProjectID != "" {
				// CD-0200 source-qualified identity: an explicit source
				// project names the target's endpoint, so the relation is
				// cross-source by declaration. A local record holding the
				// same bare ID cannot capture the endpoint. Target
				// existence, source-set membership, and the precedence
				// rules validate over the verified source set at rebuild.
				key := relation.Kind + "\x00" + record.ID + "\x00" + relation.SourceProjectID + "/" + relation.TargetID
				if seen[key] {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation is duplicated", false, "declare each typed law relation once")
				}
				seen[key] = true
				continue
			}
			if !ok {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation target is not a declared decision/spec record", false, "reference a decision or spec in the same manifest, or name its source with source_project_id")
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
			case "supersedes":
				if record.Status != "accepted" || target.Status != "superseded" || target.Successor != record.ID {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "supersedes relation disagrees with the target successor declaration", false, "make an accepted source supersede its exact superseded successor target")
				}
				graph[record.ID] = append(graph[record.ID], relation.TargetID)
			case "refines", "subordinate_to":
				graph[record.ID] = append(graph[record.ID], relation.TargetID)
			}
		}
	}
	for _, record := range manifest.Records {
		if record.Successor == "" {
			continue
		}
		// Cross-source supersedes is refused fail closed, so a successor
		// must be declared in the superseded record's own manifest; the
		// qualified project_id/law_id form is the refusal (CD-0200).
		if err := refuseExternalKnowledgeSuccessor("parse_knowledge_manifest", record.ID, record.Successor); err != nil {
			return err
		}
		found := false
		for _, relation := range byID[record.Successor].LawRelations {
			if relation.Kind == "supersedes" && relation.TargetID == record.ID {
				found = true
				break
			}
		}
		if !found {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "successor declaration lacks its matching supersedes relation", false, "declare the corresponding supersedes edge on the accepted successor")
		}
	}
	if relationGraphHasCycle(graph) {
		return newFailure(KindCycleDetected, "parse_knowledge_manifest", "directed law relations contain a cycle", false, "remove the cycle from the authored law graph")
	}
	return nil
}

func relationGraphHasCycle(graph map[string][]string) bool {
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

// dispositionPathPattern mirrors $defs.disposition.path in
// contracts/concord-knowledge-index.v1.schema.json. Only markdown is walked by
// the closure validator, so a disposition naming anything else could never
// subtract a document and would sit in the manifest as a claim nobody checks.
var dispositionPathPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+(?:/[a-zA-Z0-9._-]+)*\.md$`)

var manifestDispositions = map[string]bool{"archived": true}

// validateManifestDispositions enforces the record/disposition exclusion. A
// path is either knowledge with a law state or source material the operator
// declined to formalize; a manifest that claims both has no answer to give.
func validateManifestDispositions(dispositions []KnowledgeDisposition, recordPaths map[string]bool) error {
	if len(dispositions) > maxManifestRecords {
		return newFailure(KindKnowledgeIndexIncomplete, "parse_knowledge_manifest", "manifest contains too many dispositions", true, "split the knowledge authority into bounded homes")
	}
	seen := make(map[string]bool, len(dispositions))
	for _, disposition := range dispositions {
		if len(disposition.Path) > maxManifestPath || strings.Contains(disposition.Path, "..") || !dispositionPathPattern.MatchString(disposition.Path) {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "disposition path is not a bounded repository-relative markdown path: "+disposition.Path, false, "name one markdown document under a declared knowledge root")
		}
		if !manifestDispositions[disposition.Disposition] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "disposition is not closed: "+disposition.Disposition, false, "use the archived disposition")
		}
		if disposition.Reason == "" || utf8.RuneCountInString(disposition.Reason) > maxManifestSummary || strings.TrimSpace(disposition.Reason) != disposition.Reason {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "disposition reason is empty, oversized, or not clean", false, "state why the document is not formalized")
		}
		if seen[disposition.Path] {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "manifest disposes of the same path twice: "+disposition.Path, false, "dispose of one path once")
		}
		if recordPaths[disposition.Path] {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "path is both a record and a disposition: "+disposition.Path, false, "either record the document or dispose of it, never both")
		}
		seen[disposition.Path] = true
	}
	return nil
}

// refuseExternalKnowledgeSuccessor refuses the qualified project_id/law_id
// successor form (CD-0200). A cross-source supersedes edge has no admitted
// declaration — the rebuild and every consequential boundary refuse it with
// "supersede within the declaring source or amend through the shared home" —
// so a superseded record's successor must live in its own manifest.
func refuseExternalKnowledgeSuccessor(op, recordID, successor string) error {
	_, _, qualified, err := parseQualifiedKnowledgeID(op, successor)
	if err != nil {
		return err
	}
	if !qualified {
		return nil
	}
	return newFailure(KindInvalidNoteProof, op, "supersede within the declaring source or amend through the shared home: "+recordID+" declares the external successor "+successor, false, "declare the successor record in the same manifest")
}

func validateManifestSuccessors(records []KnowledgeRecord) error {
	byID := make(map[string]KnowledgeRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, record := range records {
		if record.Successor == "" {
			continue
		}
		if record.Successor == record.ID {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record cannot succeed itself", false, "reference a distinct canonical successor")
		}
		if err := refuseExternalKnowledgeSuccessor("parse_knowledge_manifest", record.ID, record.Successor); err != nil {
			return err
		}
		successor, ok := byID[record.Successor]
		if !ok {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record references an undeclared successor: "+record.Successor, false, "declare the successor in the same manifest")
		}
		if successor.Kind != record.Kind {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record successor kind does not match", false, "reference a successor of the same knowledge kind")
		}
		wantStatus := "published"
		if manifestLawBearingKinds[record.Kind] {
			wantStatus = "accepted"
		}
		if successor.Status != wantStatus {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record successor status is incompatible", false, "reference the active accepted or published successor")
		}
	}
	return nil
}

func validateManifestKindList(values []string, field string) (map[string]bool, error) {
	// Both lists draw from the same closed vocabulary, so the two bounds are
	// the same number.
	if len(values) > len(knowledgeKindsClosed) {
		return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" exceeds the closed kind bound", false, "use the closed knowledge kind vocabulary")
	}
	result := make(map[string]bool, len(values))
	for _, kind := range values {
		if !knowledgeKindsClosed[kind] {
			return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" contains an unsupported kind: "+kind, false, "use the closed knowledge kind vocabulary")
		}
		if result[kind] {
			return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", field+" contains duplicate kinds", false, "list each kind once")
		}
		result[kind] = true
	}
	return result, nil
}

func validateKnowledgeRecordForSchema(record KnowledgeRecord, supported, indexed map[string]bool, schemaVersion string, pathPrefix string, covered func(string) bool) error {
	if err := validateKnowledgeCriterionBindings(record); err != nil {
		return err
	}
	if err := validateKnowledgeEvidencePaths(record); err != nil {
		return err
	}
	if err := validateKnowledgeRecordIdentity(record, supported, indexed, pathPrefix, covered); err != nil {
		return err
	}
	if err := validateKnowledgeRecordAuthority(record); err != nil {
		return err
	}
	if err := validateKnowledgeRecordSuccession(record); err != nil {
		return err
	}
	if record.Title == "" || utf8.RuneCountInString(record.Title) > maxManifestTitle || strings.TrimSpace(record.Title) != record.Title || record.Summary == "" || utf8.RuneCountInString(record.Summary) > maxManifestSummary || strings.TrimSpace(record.Summary) != record.Summary {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record title or summary is empty, oversized, or not clean", false, "supply bounded authored metadata")
	}
	if record.Tags == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record tags field is missing or null", false, "supply an explicit tags array")
	}
	if err := validateManifestStringArray(record.Tags, "tags"); err != nil {
		return err
	}
	if err := validateManifestScopesForSchema(record.Scopes, schemaVersion); err != nil {
		return err
	}
	if err := validateContentHash(record.SHA256); err != nil {
		return err
	}
	return nil
}

// validateKnowledgeCriterionBindings validates one spec record's criterion
// bindings: each criterion index is positive and unique, and each binding
// carries exactly one form — a scenario, an exemption reason, or a work-item
// predicate.
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
	}
	return nil
}

// validateKnowledgeEvidencePaths validates the record's bounded set of
// repository-relative evidence paths.
func validateKnowledgeEvidencePaths(record KnowledgeRecord) error {
	if len(record.Evidence) > 32 {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record carries too many evidence paths", false, "supply at most thirty-two evidence paths")
	}
	for _, evidence := range record.Evidence {
		if len(evidence) < 1 || len(evidence) > 512 || strings.HasPrefix(evidence, "/") || strings.Contains(evidence, "..") {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "evidence must be bounded repository-relative paths", false, "supply relative evidence paths")
		}
	}
	return nil
}

// validateKnowledgeRecordIdentity validates the record's ID, kind, path,
// status, and the status each kind admits.
func validateKnowledgeRecordIdentity(record KnowledgeRecord, supported, indexed map[string]bool, pathPrefix string, covered func(string) bool) error {
	if record.ID == "" || utf8.RuneCountInString(record.ID) > maxManifestID || strings.TrimSpace(record.ID) != record.ID {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record ID is empty, oversized, or not clean", false, "use a bounded stable ID")
	}
	if !manifestRecordKinds[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record kind is not manifest-backed: "+record.Kind, false, "use constitution, decision, spec, lesson, reference, or research")
	}
	if !supported[record.Kind] || !indexed[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record kind is not indexed: "+record.Kind, false, "include the record kind in supported_kinds and indexed_kinds")
	}
	if err := validateRecordPathForTier(record.Path, pathPrefix, covered); err != nil {
		return err
	}
	if record.Kind == "decision" && !canonicalDecisionPath(record.Path) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "decision record is outside the canonical CD decision path", false, "use "+pathPrefix+"decisions/CD-NNNN markdown")
	}
	if record.Status != "accepted" && record.Status != "published" && record.Status != "superseded" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record status is not closed", false, "use accepted, published, or superseded")
	}
	if lawBearing := manifestLawBearingKinds[record.Kind]; lawBearing && record.Status == "published" || !lawBearing && record.Status == "accepted" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "status is invalid for record kind", false, "law-bearing records are accepted; every other kind is published")
	}
	return nil
}

// validateKnowledgeRecordAuthority validates the record's authority tier and
// the fields each tier admits.
func validateKnowledgeRecordAuthority(record KnowledgeRecord) error {
	if record.Authority.Tier != "legislated" && record.Authority.Tier != "derived" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record authority tier is not closed", false, "use legislated or derived")
	}
	if record.Authority.Tier == "legislated" {
		if !validManifestID(record.Authority.LegislatedBy) || record.Authority.ContractVersion < 1 {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "legislated record authority is incomplete", false, "name the approving work and positive contract version")
		}
	} else if record.Authority.LegislatedBy != "" || record.Authority.ContractVersion != 0 {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "derived record authority carries legislative fields", false, "remove legislated_by and contract_version")
	}
	return nil
}

// validateKnowledgeRecordSuccession validates the record's successor chain
// and date.
func validateKnowledgeRecordSuccession(record KnowledgeRecord) error {
	if record.Status == "superseded" && record.Successor == "" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "superseded record lacks successor", false, "declare the stable successor ID")
	}
	if record.Successor != "" && (utf8.RuneCountInString(record.Successor) > maxManifestID || strings.TrimSpace(record.Successor) != record.Successor) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "successor is oversized or not clean", false, "use a bounded stable successor ID")
	}
	if record.Status != "superseded" && record.Successor != "" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "successor is only valid for superseded records", false, "remove successor or mark the record superseded")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.Date); err != nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record date is not RFC3339", false, "use an RFC3339 date")
	}
	return nil
}

func canonicalDecisionPath(value string) bool {
	base := path.Base(value)
	if !strings.HasPrefix(base, "CD-") || !strings.HasSuffix(base, ".md") {
		return false
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(base, "CD-"), ".md")
	if len(identifier) < 4 {
		return false
	}
	for _, digit := range identifier[:4] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return len(identifier) == 4 || identifier[4] == '-'
}

func validateManifestPath(value string) error {
	return validateManifestPathForPrefix(value, manifestRecordPathPrefix)
}

// operatorOverridePathRE mirrors OPERATOR_OVERRIDE_PATH_RE in
// scripts/check-knowledge-index.py: a relative directory prefix with a
// trailing slash, or a relative markdown file path, over bounded clean
// segments.
var operatorOverridePathRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+(?:/[a-zA-Z0-9._-]+)*(?:/|\.md)$`)

// externalRecordSegmentRE mirrors the external path shape in
// $defs.record.allOf of contracts/concord-knowledge-index.v1.schema.json:
// every segment is a bounded clean identifier and the path ends in markdown.
var externalRecordSegmentRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// validatedOverrideCoverage validates the manifest head's operator overrides
// (CD-0194 D2) and returns the coverage predicate they admit. The store
// proves shape, completeness, and Product ownership; composition and the
// placement check prove the recorded_in anchor against its closed instruction
// block, so a record that parses here has already passed the anchor gate on
// the authoring side. An override belongs to exactly one Product: a head
// whose override names a Product other than the manifest's owning registry
// key refuses, so one Product's recorded instruction can never admit another
// Product's external placement.
func validatedOverrideCoverage(overrides []KnowledgeOperatorOverride, productKey string) (func(string) bool, error) {
	if len(overrides) == 0 {
		return func(string) bool { return false }, nil
	}
	if len(overrides) > maxOperatorOverrides {
		return nil, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator_overrides exceeds the bounded array", false, "record at most 32 operator overrides")
	}
	seen := make(map[string]bool, len(overrides))
	for _, override := range overrides {
		if err := validateOperatorOverride(override, productKey); err != nil {
			return nil, err
		}
		if seen[override.Path] {
			return nil, newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "operator_overrides carries a duplicate path", false, "record one override per placement")
		}
		seen[override.Path] = true
	}
	return func(path string) bool {
		for _, override := range overrides {
			if overrideCoversPath(override.Path, path) {
				return true
			}
		}
		return false
	}, nil
}

// overrideCoversPath mirrors the placement check: a directory override ends in
// a slash and covers every path beneath it, so `external/knowledge/` cannot be
// narrowed by a sibling such as `external/knowledge-notes/`. A file override
// covers exactly its own path.
func overrideCoversPath(overridePath, candidate string) bool {
	if strings.HasSuffix(overridePath, "/") {
		return strings.HasPrefix(candidate, overridePath)
	}
	return candidate == overridePath
}

func validateOperatorOverride(override KnowledgeOperatorOverride, productKey string) error {
	if override.Path == "" || utf8.RuneCountInString(override.Path) > 256 || !operatorOverridePathRE.MatchString(override.Path) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator override path is not a clean directory prefix or markdown path", false, "use a relative directory prefix with a trailing slash or a relative markdown path")
	}
	if strings.HasPrefix(override.Path, ".concord/") {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator override path is inside the default tree and cannot be an override", false, "place the record under .concord/ or override a path outside it")
	}
	if override.ProductID != productKey {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator override names Product "+override.ProductID+", not this manifest's owning Product "+productKey, false, "record the override in the manifest of the Product it belongs to")
	}
	if override.RecordedIn == "" || utf8.RuneCountInString(override.RecordedIn) > maxManifestID || strings.TrimSpace(override.RecordedIn) != override.RecordedIn {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator override recorded_in is empty, oversized, or not clean", false, "name the accepted decision carrying the operator's instruction")
	}
	if utf8.RuneCountInString(override.Reason) < minOverrideReason || utf8.RuneCountInString(override.Reason) > maxOverrideReason || strings.TrimSpace(override.Reason) != override.Reason {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "operator override reason is not a bounded trimmed justification", false, "supply a trimmed reason of twelve to five hundred twelve characters")
	}
	return nil
}

// validateRecordPathForTier validates one record path under the prefix its
// layout tier carries (CD-0194 D5). A path below the tier prefix follows the
// authored tier rules. A path outside the prefix is a Product knowledge
// location outside the default tree: CD-0194 D2 admits it only through an
// explicit operator override, and never for a path inside .concord/, which no
// override can name. covered is nil on self-authored validation routes, which
// never leave the tier prefix.
func validateRecordPathForTier(value, pathPrefix string, covered func(string) bool) error {
	if strings.HasPrefix(value, pathPrefix) {
		return validateManifestPathForPrefix(value, pathPrefix)
	}
	if covered != nil && !strings.HasPrefix(value, ".concord/") && covered(value) {
		return validateExternalRecordPath(value, pathPrefix)
	}
	if isExternalRecordPathShape(value) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "external record path carries no operator override", false, "record an explicit operator override in the manifest head before indexing knowledge outside "+pathPrefix)
	}
	return validateManifestPathForPrefix(value, pathPrefix)
}

// isExternalRecordPathShape reports whether the value looks like a clean
// override-admittable markdown path, so an uncovered external placement
// refuses with the override guidance instead of the tier-prefix guidance.
func isExternalRecordPathShape(value string) bool {
	if value == "" || !strings.HasSuffix(value, ".md") || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !externalRecordSegmentRE.MatchString(part) {
			return false
		}
	}
	return true
}

// validateExternalRecordPath applies the closed external shape to an
// override-covered record path. The tier-relative work and research
// exclusions stay tier-relative; the generated-substring exclusion is
// generic and refuses generated content in any tree.
func validateExternalRecordPath(value, pathPrefix string) error {
	if value == "" || utf8.RuneCountInString(value) > maxManifestPath || path.Clean(value) != value || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "-") || !strings.HasSuffix(value, ".md") {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "external record path is not a clean relative markdown path", false, "use a clean relative markdown path below the overridden location")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == ".." || strings.ContainsRune(part, '\x00') || !externalRecordSegmentRE.MatchString(part) {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "external record path carries a forbidden or malformed segment", false, "use bounded clean path segments below the overridden location")
		}
	}
	if reason, ineligible := manifestPathIneligibleFor(value, pathPrefix); ineligible {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "external record path is not an eligible authored knowledge blob", false, reason+"; "+manifestIneligibleHintFor(pathPrefix))
	}
	return nil
}

func validateManifestPathForPrefix(value, pathPrefix string) error {
	if value == knowledgeManifestPath || value == "" || utf8.RuneCountInString(value) > maxManifestPath || path.Clean(value) != value || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "-") || !strings.HasPrefix(value, pathPrefix) || !strings.HasSuffix(value, ".md") {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path is not a clean docs markdown path", false, "use one regular markdown blob below "+pathPrefix)
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == ".." || strings.ContainsRune(part, '\x00') {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path contains traversal or empty components", false, "use a clean relative path")
		}
	}
	if reason, ineligible := manifestPathIneligibleFor(value, pathPrefix); ineligible {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path is not an eligible authored knowledge blob", false, reason+"; "+manifestIneligibleHintFor(pathPrefix))
	}
	return nil
}

// manifestIneligiblePrefixes and manifestIneligibleSubstring decompose the
// negative lookahead of $defs.record.path in
// contracts/concord-knowledge-index.v1.schema.json, which is the sole
// declaration of which authored docs paths may carry a manifest record. RE2
// has no lookahead, so the schema pattern cannot be compiled here;
// TestKnowledgeManifestIneligiblePathsMatchSchema binds this decomposition
// back to the schema alternation instead of trusting the restatement.
var manifestIneligiblePrefixes = ineligiblePrefixesForPrefix(manifestRecordPathPrefix)

// The comparison is ASCII case-insensitive, which the schema alternation
// spells as a per-letter character class so both forms accept the same set.
const manifestIneligibleSubstring = "generated"

func ineligiblePrefixesForPrefix(pathPrefix string) []string {
	return []string{pathPrefix + "work/", pathPrefix + "research/"}
}

// manifestPathIneligible reports why a well-formed docs markdown path may not
// carry a manifest record, or false when the path is eligible.
func manifestPathIneligible(value string) (string, bool) {
	return manifestPathIneligibleFor(value, manifestRecordPathPrefix)
}

func manifestPathIneligibleFor(value, pathPrefix string) (string, bool) {
	for _, prefix := range ineligiblePrefixesForPrefix(pathPrefix) {
		if strings.HasPrefix(value, prefix) {
			return "path is under " + prefix, true
		}
	}
	if strings.Contains(strings.ToLower(value), manifestIneligibleSubstring) {
		return "path contains " + strconv.Quote(manifestIneligibleSubstring), true
	}
	return "", false
}

// manifestIneligibleHint states exactly what validateManifestPath enforces, so
// the operator guidance cannot drift from the rules that produced the failure.
func manifestIneligibleHint() string {
	return manifestIneligibleHintFor(manifestRecordPathPrefix)
}

func manifestIneligibleHintFor(pathPrefix string) string {
	return "a record path may not start with " + strings.Join(ineligiblePrefixesForPrefix(pathPrefix), " or ") +
		", or contain " + strconv.Quote(manifestIneligibleSubstring)
}

func validateManifestScopesForSchema(scopes KnowledgeRecordScopes, schemaVersion string) error {
	if !knowledgeManifestSchemaAccepted(schemaVersion) || scopes.Mode != "home" && scopes.Mode != "explicit" || scopes.ProductIDs == nil || scopes.ProjectIDs == nil || scopes.DomainIDs == nil || scopes.TagIDs == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record scopes are invalid", false, "use an accepted schema version with home or explicit scope mode and domain_ids")
	}
	valuesByName := map[string][]string{
		"product_ids": scopes.ProductIDs,
		"project_ids": scopes.ProjectIDs,
		"domain_ids":  scopes.DomainIDs,
		"tag_ids":     scopes.TagIDs,
	}
	for name, values := range valuesByName {
		if err := validateManifestStringArray(values, name); err != nil {
			return err
		}
	}
	if scopes.Mode == "home" && (len(scopes.ProductIDs) > 0 || len(scopes.ProjectIDs) > 0 || len(scopes.DomainIDs) > 0 || len(scopes.TagIDs) > 0) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "home scope cannot carry explicit scope IDs", false, "choose explicit mode for declared scope IDs")
	}
	return nil
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

// readKnowledgeManifest reads the manifest at a commit. A commit that carries
// the shard head composes the manifest from its shards (CD-0114). A commit
// that predates the shards carries the aggregate file, and that is the shape
// it is read in; a commit with neither is the legacy, explicitly-supported
// state with no manifest.
func readKnowledgeManifest(ctx context.Context, repo, commit string, role knowledgeManifestRole) (KnowledgeManifest, bool, error) {
	shards, sharded, err := readKnowledgeShardsAtCommit(ctx, repo, commit)
	if err != nil {
		return KnowledgeManifest{}, false, err
	}
	if sharded {
		manifest, err := composeKnowledgeManifest(shards, role)
		if err != nil {
			return KnowledgeManifest{}, false, err
		}
		// Every committed manifest read proves the operator overrides it
		// carries (CD-0194 D2): an external record is law only while its
		// anchor decision, read from this same commit, carries the closed
		// approve instruction for the override's exact Product and path.
		if err := validateOverrideAnchors(manifest, committedOverrideAnchorReader(ctx, repo, commit)); err != nil {
			return KnowledgeManifest{}, false, err
		}
		return manifest, false, nil
	}
	entry, err := gitTreeEntry(ctx, repo, commit, knowledgeManifestPath)
	if err != nil {
		// A missing manifest is the legacy, explicitly-supported state.
		out, gitErr := runGit(ctx, repo, "ls-tree", "-z", commit, "--", knowledgeManifestPath)
		if gitErr != nil {
			return KnowledgeManifest{}, false, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot inspect the knowledge manifest", true, "restore the git object and retry", gitErr)
		}
		entries, parseErr := parseTreeEntries(out)
		if parseErr != nil {
			return KnowledgeManifest{}, false, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest tree entry is malformed", false, "repair the canonical git tree", parseErr)
		}
		if len(entries) == 0 {
			return KnowledgeManifest{}, true, nil
		}
		return KnowledgeManifest{}, false, err
	}
	if entry.kind != "blob" || entry.mode != "100644" {
		return KnowledgeManifest{}, false, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest is not a regular blob", false, "commit a regular manifest file")
	}
	content, err := runGit(ctx, repo, "cat-file", "blob", commit+":"+knowledgeManifestPath)
	if err != nil {
		return KnowledgeManifest{}, false, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read the committed manifest blob", true, "restore the manifest blob and retry", err)
	}
	manifest, err := parseKnowledgeManifestForRole(content, aggregateRecordPathPrefix(content), role)
	if err != nil {
		return KnowledgeManifest{}, false, err
	}
	if err := validateOverrideAnchors(manifest, committedOverrideAnchorReader(ctx, repo, commit)); err != nil {
		return KnowledgeManifest{}, false, err
	}
	return manifest, false, nil
}

// aggregateRecordPathPrefix resolves the record path prefix an aggregate
// manifest validates under. The aggregate shape predates the shard homes and
// carries no layout marker, so the prefix follows the document: every record
// under .concord/docs/ is a current-placement corpus, anything else is the
// aggregate-era docs/ placement. A corpus mixing the two refuses under the
// docs/ prefix it falls back to, which is the outcome a mixed corpus has
// coming.
func aggregateRecordPathPrefix(data []byte) string {
	var probe struct {
		Records []struct {
			Path string `json:"path"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || len(probe.Records) == 0 {
		return preMigrationManifestRecordPathPrefix
	}
	for _, record := range probe.Records {
		if !strings.HasPrefix(record.Path, manifestRecordPathPrefix) {
			return preMigrationManifestRecordPathPrefix
		}
	}
	return manifestRecordPathPrefix
}

func verifyManifestRecord(ctx context.Context, repo, commit string, record KnowledgeRecord, role knowledgeManifestRole) error {
	manifest, missing, err := readKnowledgeManifest(ctx, repo, commit, role)
	if err != nil {
		return err
	}
	if missing {
		return newFailure(KindKnowledgeMissing, "verify_manifest_record", "recorded manifest is missing", false, "restore the manifest at the recorded commit")
	}
	var declared *KnowledgeRecord
	for i := range manifest.Records {
		if manifest.Records[i].ID == record.ID {
			declared = &manifest.Records[i]
			break
		}
	}
	if declared == nil {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "recorded projection has no manifest declaration", false, "rebuild from the manifest commit and preserve its metadata")
	}
	if differences := knowledgeProjectionDifferences(*declared, record); len(differences) > 0 {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "recorded projection differs from manifest fields: "+strings.Join(differences, ", "), false, "rebuild from the manifest commit and preserve its metadata")
	}
	entry, err := gitTreeEntry(ctx, repo, commit, record.Path)
	if err != nil || entry.kind != "blob" || entry.mode != "100644" {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "manifest record blob is missing or not regular", false, "restore the referenced regular markdown blob")
	}
	content, err := runGit(ctx, repo, "cat-file", "blob", commit+":"+record.Path)
	if err != nil {
		return wrapFailure(KindInvalidNoteProof, "verify_manifest_record", "cannot read the referenced manifest blob", true, "restore the git object and retry", err)
	}
	sum := sha256.Sum256(content)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != record.SHA256 {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "manifest record hash does not match blob bytes", false, "recompute the authored sha256 proof")
	}
	return nil
}

// knowledgeProjectionDifferences compares the manifest fields that Q10 stores
// and reconstructs. Other metadata remains protected by manifest parsing,
// blob verification, and its owning projection readers.
func knowledgeProjectionDifferences(declared, projected KnowledgeRecord) []string {
	differences := []string{}
	for _, field := range []struct {
		name                string
		declared, projected string
	}{
		{"id", declared.ID, projected.ID},
		{"kind", declared.Kind, projected.Kind},
		{"path", declared.Path, projected.Path},
		{"status", declared.Status, projected.Status},
		{"date", declared.Date, projected.Date},
		{"title", declared.Title, projected.Title},
		{"summary", declared.Summary, projected.Summary},
		{"scopes.mode", declared.Scopes.Mode, projected.Scopes.Mode},
		{"successor", declared.Successor, projected.Successor},
		{"sha256", declared.SHA256, projected.SHA256},
		{"home_domain_id", declared.HomeDomainID, projected.HomeDomainID},
		{"product_wide_rationale", declared.ProductWideRationale, projected.ProductWideRationale},
	} {
		if field.declared != field.projected {
			differences = append(differences, field.name)
		}
	}
	for _, field := range []struct {
		name                string
		declared, projected []string
	}{
		{"tags", declared.Tags, projected.Tags},
		{"scopes.product_ids", declared.Scopes.ProductIDs, projected.Scopes.ProductIDs},
		{"scopes.project_ids", declared.Scopes.ProjectIDs, projected.Scopes.ProjectIDs},
		{"scopes.domain_ids", declared.Scopes.DomainIDs, projected.Scopes.DomainIDs},
		{"scopes.tag_ids", declared.Scopes.TagIDs, projected.Scopes.TagIDs},
		{"applies_to_domain_ids", declared.AppliesToDomainIDs, projected.AppliesToDomainIDs},
	} {
		if !slices.Equal(sortedKnowledgeProjectionValues(field.declared), sortedKnowledgeProjectionValues(field.projected)) {
			differences = append(differences, field.name)
		}
	}
	return differences
}

func sortedKnowledgeProjectionValues(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid object key")
				}
				seen[name] = true
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		case '[':
			for decoder.More() {
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		return err
	}
	return nil
}
