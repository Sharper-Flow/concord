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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	knowledgeManifestPath = "docs/concord-knowledge-index.v1.json"
	maxKnowledgeManifest  = 256 * 1024
	maxManifestRecords    = 1000
	maxManifestArray      = 64
	maxManifestID         = 256
	maxManifestTitle      = 256
	maxManifestSummary    = 4096
	maxManifestPath       = 512
	maxManifestDomains    = 64
	// maxManifestRootHomeRationale bounds the claim to a stated reason rather
	// than an essay. A rationale that needs more room is describing law that
	// belongs in a child Domain.
	maxManifestRootHomeRationale = 512
	maxManifestRelations         = 64
	maxCriterionBindings         = 1000
	minCriterionExemption        = 12
	maxCriterionExemption        = 512
)

var knowledgeKindsClosed = map[string]bool{
	"work_note":    true,
	"constitution": true,
	"decision":     true,
	"spec":         true,
	"lesson":       true,
	"reference":    true,
	"research":     true,
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
// store does not interpret — the prose contract — which must survive a parse
// and re-marshal verbatim, because
// rewriting the manifest to append a record must never silently repeal it.
// TestKnowledgeManifestVocabularyMatchesSchema binds this set to
// contracts/concord-knowledge-index.v1.schema.json.
var manifestRootKeys = map[string]bool{
	"schema_version":  true,
	"supported_kinds": true,
	"indexed_kinds":   true,
	"domain_registry": true,
	"records":         true,
	"dispositions":    true,
	"knowledge_roots": true,
	"exclusions":      true,
	"doc_contract":    false,
}

// canonicalManifestRootOrder is the root key order of the knowledge manifest:
// the property order declared by contracts/concord-knowledge-index.v1.schema.json,
// which is also the order scripts/generate-knowledge-index.py carries forward
// from its aggregate template. Go's map order is lexical and would regroup the
// root keys, so the emitter places them from this list.
// TestKnowledgeManifestKeyOrderMatchesSchema binds it to the schema. Record
// keys need no such list: the generator emits them sorted, which is what
// encoding/json already does for a map.
var canonicalManifestRootOrder = []string{
	"schema_version",
	"supported_kinds",
	"indexed_kinds",
	"domain_registry",
	"knowledge_roots",
	"exclusions",
	"dispositions",
	"doc_contract",
	"records",
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
	SchemaVersion         string                  `json:"schema_version"`
	SupportedKinds        []string                `json:"supported_kinds"`
	IndexedKinds          []string                `json:"indexed_kinds"`
	DomainRegistry        KnowledgeDomainRegistry `json:"domain_registry"`
	KnowledgeRoots        []string                `json:"knowledge_roots,omitempty"`
	Exclusions            []string                `json:"exclusions,omitempty"`
	DocContract           *KnowledgeDocContract   `json:"doc_contract,omitempty"`
	Records               []KnowledgeRecord       `json:"records"`
	Dispositions          []KnowledgeDisposition  `json:"dispositions"`
	domainRegistryPresent bool
	dispositionsPresent   bool
	// uninterpreted holds every declared top-level key this package does not
	// model, verbatim, so marshalKnowledgeManifest can put it back.
	uninterpreted map[string]json.RawMessage
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

type KnowledgeCriterionBinding struct {
	Criterion int    `json:"criterion"`
	Scenario  string `json:"scenario,omitempty"`
	Exemption string `json:"exemption,omitempty"`
}

// UndecidedRootHomeRationale marks a root home the CD-0041 D9.2 upcast
// assigned rather than an author claimed. Legacy law carrying zero or several
// component IDs has no decided home, and the migration cannot invent one. The
// marker keeps that state visible and searchable instead of letting it wear
// the same shape as a reviewed Product-wide claim.
const UndecidedRootHomeRationale = "undecided: home assigned by the CD-0041 D9.2 upcast and not yet reviewed"

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
type KnowledgeRelation struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
}

type KnowledgeRecordScopes struct {
	Mode                string   `json:"mode"`
	ProductIDs          []string `json:"product_ids"`
	ProjectIDs          []string `json:"project_ids"`
	ComponentIDs        []string `json:"component_ids"`
	DomainIDs           []string `json:"domain_ids,omitempty"`
	TagIDs              []string `json:"tag_ids"`
	componentIDsPresent bool
	domainIDsPresent    bool
}

func (scopes *KnowledgeRecordScopes) UnmarshalJSON(data []byte) error {
	type scopesAlias KnowledgeRecordScopes
	var parsed scopesAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*scopes = KnowledgeRecordScopes(parsed)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, scopes.componentIDsPresent = fields["component_ids"]
	_, scopes.domainIDsPresent = fields["domain_ids"]
	return nil
}

func (scopes KnowledgeRecordScopes) MarshalJSON() ([]byte, error) {
	type scopeJSON struct {
		Mode         string    `json:"mode"`
		ProductIDs   []string  `json:"product_ids"`
		ProjectIDs   []string  `json:"project_ids"`
		ComponentIDs *[]string `json:"component_ids,omitempty"`
		DomainIDs    *[]string `json:"domain_ids,omitempty"`
		TagIDs       []string  `json:"tag_ids"`
	}
	encoded := scopeJSON{
		Mode: scopes.Mode, ProductIDs: scopes.ProductIDs, ProjectIDs: scopes.ProjectIDs, TagIDs: scopes.TagIDs,
	}
	if scopes.componentIDsPresent || scopes.ComponentIDs != nil {
		componentIDs := scopes.ComponentIDs
		encoded.ComponentIDs = &componentIDs
	}
	if scopes.domainIDsPresent || scopes.DomainIDs != nil {
		domainIDs := scopes.DomainIDs
		encoded.DomainIDs = &domainIDs
	}
	return json.Marshal(encoded)
}

func (manifest *KnowledgeManifest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	modeled := make(map[string]json.RawMessage, len(fields))
	uninterpreted := map[string]json.RawMessage{}
	for key, value := range fields {
		projected, declared := manifestRootKeys[key]
		if !declared {
			return fmt.Errorf("json: unknown field %q", key)
		}
		if projected {
			modeled[key] = value
			continue
		}
		uninterpreted[key] = value
	}
	body, err := json.Marshal(modeled)
	if err != nil {
		return err
	}
	type manifestAlias KnowledgeManifest
	var parsed manifestAlias
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*manifest = KnowledgeManifest(parsed)
	manifest.uninterpreted = uninterpreted
	_, manifest.domainRegistryPresent = fields["domain_registry"]
	_, manifest.dispositionsPresent = fields["dispositions"]
	return nil
}

func (disposition *KnowledgeDisposition) UnmarshalJSON(data []byte) error {
	type dispositionAlias KnowledgeDisposition
	var parsed dispositionAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return err
	}
	*disposition = KnowledgeDisposition(parsed)
	return nil
}

func (domain *KnowledgeDomain) UnmarshalJSON(data []byte) error {
	type domainAlias KnowledgeDomain
	var parsed domainAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
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

// orderManifestFields emits the canonical order, then any key this package has
// not placed yet, sorted, so the output stays deterministic for a key the
// contract gains later.
func orderManifestFields(values map[string]any, canonical []string) orderedObject {
	ordered := make(orderedObject, 0, len(values))
	remaining := make(map[string]any, len(values))
	for key, value := range values {
		remaining[key] = value
	}
	for _, key := range canonical {
		if value, ok := remaining[key]; ok {
			ordered = append(ordered, orderedMember{key: key, value: value})
			delete(remaining, key)
		}
	}
	unplaced := make([]string, 0, len(remaining))
	for key := range remaining {
		unplaced = append(unplaced, key)
	}
	sort.Strings(unplaced)
	for _, key := range unplaced {
		ordered = append(ordered, orderedMember{key: key, value: remaining[key]})
	}
	return ordered
}

// orderedObject is a JSON object that emits its members in slice order.
// encoding/json sorts map keys, so an object whose authored order carries
// meaning cannot round-trip through map[string]any.
type orderedObject []orderedMember

type orderedMember struct {
	key   string
	value any
}

func (object orderedObject) MarshalJSON() ([]byte, error) {
	buffer := bytes.Buffer{}
	buffer.WriteByte('{')
	for index, member := range object {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := marshalManifestValue(member.key)
		if err != nil {
			return nil, err
		}
		value, err := marshalManifestValue(member.value)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// marshalManifestValue encodes without HTML escaping, matching the Python
// updater's json.dumps. Escaping "&" as "\u0026" in a lesson title would make
// the two writers disagree on a byte the reader never typed.
func marshalManifestValue(value any) ([]byte, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

func (manifest KnowledgeManifest) MarshalJSON() ([]byte, error) {
	type manifestJSON struct {
		SchemaVersion  string                   `json:"schema_version"`
		SupportedKinds []string                 `json:"supported_kinds"`
		IndexedKinds   []string                 `json:"indexed_kinds"`
		DomainRegistry *KnowledgeDomainRegistry `json:"domain_registry,omitempty"`
		KnowledgeRoots []string                 `json:"knowledge_roots,omitempty"`
		Exclusions     []string                 `json:"exclusions,omitempty"`
		DocContract    *KnowledgeDocContract    `json:"doc_contract,omitempty"`
		Records        []map[string]any         `json:"records"`
	}
	var registry *KnowledgeDomainRegistry
	if manifest.SchemaVersion == "1.2" || manifest.domainRegistryPresent {
		copy := manifest.DomainRegistry
		registry = &copy
	}
	records := make([]map[string]any, 0, len(manifest.Records))
	for _, record := range manifest.Records {
		records = append(records, manifestRecordEntry(record))
	}
	return json.Marshal(manifestJSON{
		SchemaVersion: manifest.SchemaVersion, SupportedKinds: manifest.SupportedKinds,
		IndexedKinds: manifest.IndexedKinds, DomainRegistry: registry,
		KnowledgeRoots: manifest.KnowledgeRoots, Exclusions: manifest.Exclusions,
		DocContract: manifest.DocContract, Records: records,
	})
}

func manifestRecordEntry(record KnowledgeRecord) map[string]any {
	entry := map[string]any{
		"id": record.ID, "kind": record.Kind, "path": record.Path, "status": record.Status,
		"date": record.Date, "title": record.Title, "summary": record.Summary,
		"tags": record.Tags, "scopes": manifestScopeEntry(record.Scopes), "sha256": record.SHA256,
	}
	if record.Successor != "" {
		entry["successor"] = record.Successor
	}
	if len(record.LawRelations) > 0 {
		relations := make([]map[string]string, 0, len(record.LawRelations))
		for _, relation := range record.LawRelations {
			relations = append(relations, map[string]string{"kind": relation.Kind, "target_id": relation.TargetID})
		}
		entry["law_relations"] = relations
	}
	if len(record.Evidence) > 0 {
		entry["evidence"] = record.Evidence
	}
	if record.HomeDomainID != "" {
		entry["home_domain_id"] = record.HomeDomainID
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
	entry := map[string]any{
		"mode": scopes.Mode, "product_ids": scopes.ProductIDs, "project_ids": scopes.ProjectIDs,
		"tag_ids": scopes.TagIDs,
	}
	if scopes.componentIDsPresent || scopes.ComponentIDs != nil {
		entry["component_ids"] = scopes.ComponentIDs
	}
	if scopes.domainIDsPresent || scopes.DomainIDs != nil {
		entry["domain_ids"] = scopes.DomainIDs
	}
	return entry
}

func knowledgeDomainRegistryZero(registry KnowledgeDomainRegistry) bool {
	return registry.SchemaVersion == "" && registry.ProductKey == "" && registry.RootDomainID == "" && registry.Domains == nil
}

func parseKnowledgeManifest(data []byte) (KnowledgeManifest, error) {
	if len(data) == 0 || len(data) > maxKnowledgeManifest {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest is empty or exceeds the bounded size", false, "publish a bounded v1 manifest")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest contains duplicate JSON keys", false, "remove duplicate keys from the manifest")
	}
	var manifest KnowledgeManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return KnowledgeManifest{}, wrapFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest is not strict v1 JSON", false, "repair the manifest schema and remove unknown fields", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest contains trailing JSON values", false, "publish exactly one JSON object")
	}
	if err := validateKnowledgeManifest(manifest); err != nil {
		return KnowledgeManifest{}, err
	}
	return manifest, nil
}

func validateKnowledgeManifest(manifest KnowledgeManifest) error {
	if (manifest.SchemaVersion != "1.0" && manifest.SchemaVersion != "1.1" && manifest.SchemaVersion != "1.2") || manifest.SupportedKinds == nil || manifest.IndexedKinds == nil || manifest.Records == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "manifest schema version or required root fields are invalid", false, "publish strict v1 root fields")
	}
	hasRegistry := manifest.domainRegistryPresent || !knowledgeDomainRegistryZero(manifest.DomainRegistry)
	if manifest.SchemaVersion == "1.2" {
		if !hasRegistry {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "schema 1.2 requires a domain registry", false, "publish the bounded domain registry")
		}
		if err := validateKnowledgeDomainRegistry(manifest.DomainRegistry); err != nil {
			return err
		}
	} else if hasRegistry {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry requires schema version 1.2", false, "remove domain_registry from a 1.0 or 1.1 manifest")
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
		if err := validateKnowledgeRecordForSchema(record, supported, indexed, manifest.SchemaVersion); err != nil {
			return err
		}
		if err := validateManifestLawHome(record, manifest.SchemaVersion, manifest.DomainRegistry); err != nil {
			return err
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
	if manifest.SchemaVersion == "1.2" {
		if err := validateKnowledgeDomainLawReferences(manifest.DomainRegistry, manifest.Records); err != nil {
			return err
		}
	}
	return nil
}

func validateKnowledgeDomainRegistry(registry KnowledgeDomainRegistry) error {
	if registry.SchemaVersion != "1.0" || !validProductKey(registry.ProductKey) || registry.RootDomainID != "product-root:"+registry.ProductKey || registry.Domains == nil || len(registry.Domains) > maxManifestDomains {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry root is invalid", false, "publish schema 1.0 registry metadata and a bounded domain array")
	}
	byID := make(map[string]KnowledgeDomain, len(registry.Domains))
	parentGraph := map[string][]string{}
	dependsGraph := map[string][]string{}
	replacesGraph := map[string][]string{}
	relationKeys := map[string]bool{}
	rootFound := false
	for _, domain := range registry.Domains {
		if !validManifestID(domain.DomainID) || domain.Name == "" || utf8.RuneCountInString(domain.Name) > maxManifestTitle || strings.TrimSpace(domain.Name) != domain.Name || domain.Purpose == "" || utf8.RuneCountInString(domain.Purpose) > maxManifestSummary || strings.TrimSpace(domain.Purpose) != domain.Purpose || (domain.Status != "current" && domain.Status != "deprecated") || domain.ArchitectureRelations == nil || len(domain.ArchitectureRelations) > maxManifestRelations {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain record is invalid", false, "publish bounded domain metadata and architecture relations")
		}
		if _, exists := byID[domain.DomainID]; exists {
			return newFailure(KindKnowledgeAmbiguous, "parse_knowledge_manifest", "domain registry contains duplicate domain IDs", false, "declare each domain once")
		}
		byID[domain.DomainID] = domain
		if domain.DomainID == registry.RootDomainID {
			rootFound = true
			if domain.Status != "current" || domain.parentDomainPresent || domain.ParentDomainID != "" {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry root must be current and parentless", false, "make the product root a current parentless domain")
			}
		}
		if domain.ParentDomainID != "" || domain.parentDomainPresent {
			if !validManifestID(domain.ParentDomainID) || domain.ParentDomainID == domain.DomainID {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain parent is invalid or self-referential", false, "reference a distinct domain in the same registry")
			}
			parentGraph[domain.DomainID] = append(parentGraph[domain.DomainID], domain.ParentDomainID)
		}
		for _, relation := range domain.ArchitectureRelations {
			if !validManifestID(relation.TargetDomainID) || relation.TargetDomainID == domain.DomainID {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation target is invalid or self-referential", false, "reference a distinct domain in the same registry")
			}
			if relation.Kind != "depends_on" && relation.Kind != "shares_contract_with" && relation.Kind != "replaces" {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation kind is not closed", false, "use depends_on, shares_contract_with, or replaces")
			}
			if relation.Kind != "replaces" && (relation.State != "" || relation.statePresent) {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation state is only valid for replaces", false, "omit state except on replacement relations")
			}
			key := relation.Kind + "\x00" + domain.DomainID + "\x00" + relation.TargetDomainID
			switch relation.Kind {
			case "depends_on":
				if len(relation.GoverningLawIDs) == 0 {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "depends_on requires non-empty governing law IDs", false, "name current accepted laws governing the dependency")
				}
				dependsGraph[domain.DomainID] = append(dependsGraph[domain.DomainID], relation.TargetDomainID)
			case "shares_contract_with":
				if relation.TargetDomainID < domain.DomainID {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "shares_contract_with must use its canonical ordered pair", false, "author the lower domain ID as the relation source")
				}
				if len(relation.GoverningLawIDs) == 0 {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "shares_contract_with requires non-empty governing law IDs", false, "name current accepted laws governing the shared contract")
				}
				key = relation.Kind + "\x00" + domain.DomainID + "\x00" + relation.TargetDomainID
			case "replaces":
				if relation.governingLawsPresent || relation.GoverningLawIDs != nil {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "replaces cannot carry governing law IDs", false, "omit governing_law_ids from replacement relations")
				}
				if !relation.statePresent && relation.State == "" || relation.State != "declared" && relation.State != "building" && relation.State != "coexisting" && relation.State != "cutover" && relation.State != "retired" {
					return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "replaces requires a closed state", false, "declare the replacement lifecycle state")
				}
				replacesGraph[domain.DomainID] = append(replacesGraph[domain.DomainID], relation.TargetDomainID)
			}
			if relationKeys[key] {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "architecture relation is duplicated, including reverse shares_contract_with", false, "declare each architecture relation once")
			}
			relationKeys[key] = true
			if err := validateOptionalManifestIDs(relation.GoverningLawIDs, "governing_law_ids"); err != nil {
				return err
			}
		}
	}
	if !rootFound {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "domain registry root domain is not declared", false, "declare the current parentless root domain")
	}
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
	if relationGraphHasCycle(parentGraph) || relationGraphHasCycle(dependsGraph) || relationGraphHasCycle(replacesGraph) {
		return newFailure(KindCycleDetected, "parse_knowledge_manifest", "domain architecture graph contains a cycle", false, "remove cycles from hierarchy, dependency, or replacement relations")
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

// MigrateLegacyKnowledgeManifest converts one validated component-scoped
// manifest into the Domain-only 1.2 form. Additional IDs let the operator include
// components found in historical notes or other bounded compatibility sources.
// The caller retains ownership of the input manifest and all of its slices.
func MigrateLegacyKnowledgeManifest(manifest KnowledgeManifest, productKey string, additionalComponentIDs ...string) (KnowledgeManifest, error) {
	if !validProductKey(productKey) {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "migrate_legacy_knowledge_manifest", "product key is invalid", false, "supply a lowercase product key with letters, digits, or hyphens")
	}
	if manifest.SchemaVersion != "1.0" && manifest.SchemaVersion != "1.1" {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "migrate_legacy_knowledge_manifest", "only valid schema 1.0 or 1.1 manifests can be migrated", false, "supply a legacy component-scoped manifest")
	}
	if err := validateKnowledgeManifest(manifest); err != nil {
		return KnowledgeManifest{}, err
	}

	rootID := "product-root:" + productKey
	componentIDs := map[string]bool{}
	for _, componentID := range additionalComponentIDs {
		if !validManifestID(componentID) {
			return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "migrate_legacy_knowledge_manifest", "additional legacy component ID is invalid", false, "supply bounded clean component IDs from compatibility sources")
		}
		if componentID == rootID {
			return KnowledgeManifest{}, newFailure(KindKnowledgeAmbiguous, "migrate_legacy_knowledge_manifest", "legacy component ID collides with the derived product root", false, "rename the legacy component before migration or choose its actual product key")
		}
		componentIDs[componentID] = true
	}
	for _, record := range manifest.Records {
		for _, componentID := range record.Scopes.ComponentIDs {
			if componentID == rootID {
				return KnowledgeManifest{}, newFailure(KindKnowledgeAmbiguous, "migrate_legacy_knowledge_manifest", "legacy component ID collides with the derived product root", false, "rename the legacy component before migration or choose its actual product key")
			}
			componentIDs[componentID] = true
		}
	}
	components := make([]string, 0, len(componentIDs))
	for componentID := range componentIDs {
		components = append(components, componentID)
	}
	sort.Strings(components)

	migrated := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: append([]string{}, manifest.SupportedKinds...),
		IndexedKinds:   append([]string{}, manifest.IndexedKinds...),
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0",
			ProductKey:    productKey,
			RootDomainID:  rootID,
			Domains: []KnowledgeDomain{{
				DomainID: rootID, Name: "Product root", Purpose: "Product-wide law and architecture", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{},
			}},
		},
		Records: make([]KnowledgeRecord, len(manifest.Records)),
	}
	for _, componentID := range components {
		migrated.DomainRegistry.Domains = append(migrated.DomainRegistry.Domains, KnowledgeDomain{
			DomainID: componentID, Name: componentID, Purpose: "Migrated legacy component domain", ParentDomainID: rootID, Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{},
		})
	}
	for index, source := range manifest.Records {
		record := source
		record.Tags = append([]string{}, source.Tags...)
		record.Evidence = append([]string{}, source.Evidence...)
		record.LawRelations = append([]KnowledgeRelation{}, source.LawRelations...)
		record.Scopes.ProductIDs = append([]string{}, source.Scopes.ProductIDs...)
		record.Scopes.ProjectIDs = append([]string{}, source.Scopes.ProjectIDs...)
		record.Scopes.TagIDs = append([]string{}, source.Scopes.TagIDs...)
		record.Scopes.ComponentIDs = nil
		record.Scopes.componentIDsPresent = false
		record.Scopes.DomainIDs = append([]string{}, source.Scopes.ComponentIDs...)
		sort.Strings(record.Scopes.DomainIDs)
		record.Scopes.domainIDsPresent = true
		if record.Kind == "decision" || record.Kind == "spec" {
			switch len(record.Scopes.DomainIDs) {
			case 0:
				record.HomeDomainID = rootID
				record.AppliesToDomainIDs = []string{}
				record.ProductWideRationale = UndecidedRootHomeRationale
			case 1:
				record.HomeDomainID = record.Scopes.DomainIDs[0]
				record.AppliesToDomainIDs = []string{}
			default:
				record.HomeDomainID = rootID
				record.AppliesToDomainIDs = append([]string{}, record.Scopes.DomainIDs...)
				record.ProductWideRationale = UndecidedRootHomeRationale
			}
			record.homeDomainPresent = true
			record.appliesToDomainsPresent = true
			record.productWideRationalePresent = record.ProductWideRationale != ""
		} else {
			record.HomeDomainID = ""
			record.AppliesToDomainIDs = nil
			record.ProductWideRationale = ""
			record.homeDomainPresent = false
			record.appliesToDomainsPresent = false
			record.productWideRationalePresent = false
		}
		migrated.Records[index] = record
	}
	if err := validateKnowledgeManifest(migrated); err != nil {
		return KnowledgeManifest{}, err
	}
	return migrated, nil
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
	if !record.productWideRationalePresent || rationale == "" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law homed to the root Domain must state why no child Domain owns it", false, "author product_wide_rationale, or home the record to the child Domain whose behavior it governs")
	}
	if len(rationale) > maxManifestRootHomeRationale {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "product-wide rationale is too long", false, "state the reason in one or two sentences")
	}
	return nil
}

func validateManifestLawHome(record KnowledgeRecord, schemaVersion string, registry KnowledgeDomainRegistry) error {
	if schemaVersion != "1.2" {
		if record.HomeDomainID != "" || record.AppliesToDomainIDs != nil || record.homeDomainPresent || record.appliesToDomainsPresent || record.ProductWideRationale != "" || record.productWideRationalePresent {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law-home fields require schema version 1.2", false, "remove domain law-home fields from a 1.0 or 1.1 manifest")
		}
		return nil
	}
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
		if (manifest.SchemaVersion != "1.1" && manifest.SchemaVersion != "1.2") || !manifestLawRelationSubjects[record.Kind] {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law_relations are only allowed on 1.1 or 1.2 decision/spec records", false, "publish authored relations on a schema 1.1 or 1.2 decision or spec")
		}
		for _, relation := range record.LawRelations {
			if !lawRelationKinds[relation.Kind] || relation.TargetID == "" || relation.TargetID == record.ID {
				return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "law relation kind, target, or self-edge is invalid", false, "use one closed relation kind and a distinct law ID")
			}
			target, ok := byID[relation.TargetID]
			if !ok || !manifestLawRelationSubjects[target.Kind] {
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
		if (manifest.SchemaVersion != "1.1" && manifest.SchemaVersion != "1.2") || record.Successor == "" {
			continue
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
	// Both lists draw from the same closed vocabulary. research used to be
	// supported without being indexable because no record could declare it;
	// it is a record kind now, so the two bounds are the same number.
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

func validateKnowledgeRecord(record KnowledgeRecord, supported, indexed map[string]bool) error {
	return validateKnowledgeRecordForSchema(record, supported, indexed, "1.0")
}

func validateKnowledgeRecordForSchema(record KnowledgeRecord, supported, indexed map[string]bool, schemaVersion string) error {
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
		if (binding.Scenario == "") == (binding.Exemption == "") {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion binding must carry exactly one scenario or exemption", false, "bind the criterion to a scenario or record an exemption")
		}
		if binding.Scenario != "" && !validManifestID(binding.Scenario) {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion scenario is empty, oversized, or not clean", false, "use a bounded scenario ID")
		}
		if binding.Exemption != "" && (utf8.RuneCountInString(binding.Exemption) < minCriterionExemption || utf8.RuneCountInString(binding.Exemption) > maxCriterionExemption || strings.TrimSpace(binding.Exemption) != binding.Exemption) {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "criterion exemption is not a bounded reason", false, "use a trimmed exemption reason of twelve to five hundred twelve characters")
		}
	}
	if len(record.Evidence) > 32 {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record carries too many evidence paths", false, "supply at most thirty-two evidence paths")
	}
	for _, evidence := range record.Evidence {
		if len(evidence) < 1 || len(evidence) > 512 || strings.HasPrefix(evidence, "/") || strings.Contains(evidence, "..") {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "evidence must be bounded repository-relative paths", false, "supply relative evidence paths")
		}
	}
	if record.ID == "" || utf8.RuneCountInString(record.ID) > maxManifestID || strings.TrimSpace(record.ID) != record.ID {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record ID is empty, oversized, or not clean", false, "use a bounded stable ID")
	}
	if !manifestRecordKinds[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record kind is not manifest-backed: "+record.Kind, false, "use constitution, decision, spec, lesson, reference, or research")
	}
	if !supported[record.Kind] || !indexed[record.Kind] {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record kind is not indexed: "+record.Kind, false, "include the record kind in supported_kinds and indexed_kinds")
	}
	if err := validateManifestPath(record.Path); err != nil {
		return err
	}
	if record.Kind == "decision" && !canonicalDecisionPath(record.Path) {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "decision record is outside the canonical CD decision path", false, "use docs/decisions/CD-NNNN markdown")
	}
	if record.Status != "accepted" && record.Status != "published" && record.Status != "superseded" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record status is not closed", false, "use accepted, published, or superseded")
	}
	if lawBearing := manifestLawBearingKinds[record.Kind]; lawBearing && record.Status == "published" || !lawBearing && record.Status == "accepted" {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "status is invalid for record kind", false, "law-bearing records are accepted; every other kind is published")
	}
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
	if value == knowledgeManifestPath || value == "" || utf8.RuneCountInString(value) > maxManifestPath || path.Clean(value) != value || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "-") || !strings.HasPrefix(value, "docs/") || !strings.HasSuffix(value, ".md") {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path is not a clean docs markdown path", false, "use one regular markdown blob below docs/")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == ".." || strings.ContainsRune(part, '\x00') {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path contains traversal or empty components", false, "use a clean relative path")
		}
	}
	if reason, ineligible := manifestPathIneligible(value); ineligible {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "record path is not an eligible authored knowledge blob", false, reason+"; "+manifestIneligibleHint())
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
var manifestIneligiblePrefixes = []string{"docs/work/", "docs/research/"}

// The comparison is ASCII case-insensitive, which the schema alternation
// spells as a per-letter character class so both forms accept the same set.
const manifestIneligibleSubstring = "generated"

// manifestPathIneligible reports why a well-formed docs markdown path may not
// carry a manifest record, or false when the path is eligible.
func manifestPathIneligible(value string) (string, bool) {
	for _, prefix := range manifestIneligiblePrefixes {
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
	return "a record path may not start with " + strings.Join(manifestIneligiblePrefixes, " or ") +
		", or contain " + strconv.Quote(manifestIneligibleSubstring)
}

func validateManifestScopes(scopes KnowledgeRecordScopes) error {
	return validateManifestScopesForSchema(scopes, "1.0")
}

func validateManifestScopesForSchema(scopes KnowledgeRecordScopes, schemaVersion string) error {
	if scopes.Mode != "home" && scopes.Mode != "explicit" || scopes.ProductIDs == nil || scopes.ProjectIDs == nil || scopes.TagIDs == nil {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "scope mode is not closed", false, "use home or explicit scope mode")
	}
	if schemaVersion == "1.2" {
		if scopes.DomainIDs == nil || scopes.ComponentIDs != nil || scopes.componentIDsPresent {
			return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "schema 1.2 scopes require domain_ids and forbid component_ids", false, "use domain_ids in a schema 1.2 scope")
		}
	} else if scopes.ComponentIDs == nil || scopes.DomainIDs != nil || scopes.domainIDsPresent {
		return newFailure(KindInvalidNoteProof, "parse_knowledge_manifest", "schema 1.0 or 1.1 scopes require component_ids and forbid domain_ids", false, "use component_ids in a compatibility scope")
	}
	valuesByName := map[string][]string{"product_ids": scopes.ProductIDs, "project_ids": scopes.ProjectIDs, "tag_ids": scopes.TagIDs}
	if schemaVersion == "1.2" {
		valuesByName["domain_ids"] = scopes.DomainIDs
	} else {
		valuesByName["component_ids"] = scopes.ComponentIDs
	}
	for name, values := range valuesByName {
		if err := validateManifestStringArray(values, name); err != nil {
			return err
		}
	}
	if scopes.Mode == "home" && (len(scopes.ProductIDs) > 0 || len(scopes.ProjectIDs) > 0 || len(scopes.ComponentIDs) > 0 || len(scopes.DomainIDs) > 0 || len(scopes.TagIDs) > 0) {
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

func readKnowledgeManifest(ctx context.Context, repo, commit string) (KnowledgeManifest, bool, error) {
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
	manifest, err := parseKnowledgeManifest(content)
	return manifest, false, err
}

func verifyManifestRecord(ctx context.Context, repo, commit string, record KnowledgeRecord) error {
	manifest, missing, err := readKnowledgeManifest(ctx, repo, commit)
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
	if declared == nil || !sameKnowledgeRecord(*declared, record) {
		return newFailure(KindInvalidNoteProof, "verify_manifest_record", "recorded projection does not match the exact manifest declaration", false, "rebuild from the manifest commit and preserve its metadata")
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

func sameKnowledgeRecord(a, b KnowledgeRecord) bool {
	normalize := func(record KnowledgeRecord) KnowledgeRecord {
		record.Tags = append([]string{}, record.Tags...)
		record.Scopes.ProductIDs = append([]string{}, record.Scopes.ProductIDs...)
		record.Scopes.ProjectIDs = append([]string{}, record.Scopes.ProjectIDs...)
		record.Scopes.ComponentIDs = append([]string{}, record.Scopes.ComponentIDs...)
		record.Scopes.DomainIDs = append([]string{}, record.Scopes.DomainIDs...)
		record.Scopes.TagIDs = append([]string{}, record.Scopes.TagIDs...)
		record.AppliesToDomainIDs = append([]string{}, record.AppliesToDomainIDs...)
		record.LawRelations = append([]KnowledgeRelation{}, record.LawRelations...)
		sort.Strings(record.Tags)
		sort.Strings(record.Scopes.ProductIDs)
		sort.Strings(record.Scopes.ProjectIDs)
		sort.Strings(record.Scopes.ComponentIDs)
		sort.Strings(record.Scopes.DomainIDs)
		sort.Strings(record.Scopes.TagIDs)
		sort.Strings(record.AppliesToDomainIDs)
		sort.Slice(record.LawRelations, func(i, j int) bool {
			if record.LawRelations[i].Kind == record.LawRelations[j].Kind {
				return record.LawRelations[i].TargetID < record.LawRelations[j].TargetID
			}
			return record.LawRelations[i].Kind < record.LawRelations[j].Kind
		})
		return record
	}
	a, b = normalize(a), normalize(b)
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
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
