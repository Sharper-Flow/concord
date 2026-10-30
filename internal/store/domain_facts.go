package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	DomainFactSchemaVersion = "1.0"
	DomainFactMaxBytes      = 16384
	DomainFactMaxItems      = 32
	DomainFactMaxTextBytes  = 1024
)

type DomainFactKind string

const (
	DomainFactInvariant      DomainFactKind = "invariant"
	DomainFactDataObject     DomainFactKind = "data_object"
	DomainFactStateAuthority DomainFactKind = "state_authority"
	DomainFactConfigCoupling DomainFactKind = "config_coupling"
)

// DomainFact is one shard, not a law record. Exactly the payload named by Kind
// is present. Observed standing never grants law authority.
type DomainFact struct {
	SchemaVersion   string                    `json:"schema_version"`
	ID              string                    `json:"id"`
	Kind            DomainFactKind            `json:"kind"`
	OwnerDomainID   string                    `json:"owner_domain_id"`
	OwningRoot      string                    `json:"owning_root"`
	Standing        string                    `json:"standing"`
	GoverningLawIDs []string                  `json:"governing_law_ids,omitempty"`
	Invariant       *DomainInvariantFact      `json:"invariant,omitempty"`
	DataObject      *DomainDataObjectFact     `json:"data_object,omitempty"`
	StateAuthority  *DomainStateAuthorityFact `json:"state_authority,omitempty"`
	ConfigCoupling  *DomainConfigCouplingFact `json:"config_coupling,omitempty"`
}

type DomainInvariantFact struct {
	Statement          string                        `json:"statement"`
	AppliesToDomainIDs []string                      `json:"applies_to_domain_ids"`
	EnforcedBy         []DomainFactEnforcementAnchor `json:"enforced_by"`
}

// DomainFactEnforcementAnchor uses CD-0249 D3's kind/value identity. CheckID
// associates the resolved anchor with a required check, not a passing run.
type DomainFactEnforcementAnchor struct {
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	CheckID string `json:"check_id"`
}

// Domain facts and shape checks share a closed anchor vocabulary and a byte bound.
const maxDomainFactAnchorValue = 512

var domainFactAnchorKinds = map[string]bool{"adapter_test": true, "generated": true, "go_test": true, "scenario": true, "validator": true}

// DomainFactRepositoryAnchor identifies an entry point in an existing Project.
// The caller resolves it against the same source revision as the fact.
type DomainFactRepositoryAnchor struct {
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	Symbol    string `json:"symbol"`
}

type DomainDataObjectFact struct {
	Name            string   `json:"name"`
	ResourceID      string   `json:"resource_id"`
	WriterDomainIDs []string `json:"writer_domain_ids"`
	ReaderDomainIDs []string `json:"reader_domain_ids"`
	InvariantIDs    []string `json:"invariant_ids"`
}

type DomainStateAuthorityFact struct {
	State                   string                  `json:"state"`
	AuthoritativeResourceID string                  `json:"authoritative_resource_id"`
	DerivedCopies           []DomainFactDerivedCopy `json:"derived_copies"`
	DisagreementBehavior    string                  `json:"disagreement_behavior"`
}

type DomainFactDerivedCopy struct {
	ResourceID   string                     `json:"resource_id"`
	RebuildRoute DomainFactRepositoryAnchor `json:"rebuild_route"`
}

type DomainConfigCouplingFact struct {
	Setting      string                     `json:"setting"`
	Source       string                     `json:"source"`
	ReadBy       DomainFactRepositoryAnchor `json:"read_by"`
	Effect       string                     `json:"effect"`
	Environments []string                   `json:"environments"`
}

// DomainFactRoot is the caller's resolved package-map entry. Domain roots have
// exactly one owner; mixed roots have at least two. Other roles cannot own facts.
type DomainFactRoot struct {
	Role           string
	OwnerDomainIDs []string
}

// DomainFactReferences contains authoritative, Product-scoped resolved inputs
// from one revision. AcceptedLawIDs contains only current accepted law record
// IDs; Facts includes the complete fact inventory, allowing forward references.
// True anchor entries mean resolved and, for enforcement, required by CheckID.
// Neither map asserts that a check ran or that an extractor verified a fact.
type DomainFactReferences struct {
	Registry             KnowledgeDomainRegistry
	Roots                map[string]DomainFactRoot
	AcceptedLawIDs       map[string]bool
	Facts                map[string]DomainFactKind
	RepositoryAnchors    map[DomainFactRepositoryAnchor]bool
	RequiredCheckAnchors map[DomainFactEnforcementAnchor]bool
	// ValidationMode defaults to resolved inputs. Structural mode admits known
	// typed anchors without claiming that their source or check membership was
	// resolved. ProjectIDs and RequiredCheckIDs remain authoritative inputs.
	ValidationMode   DomainFactReferenceMode
	ProjectIDs       map[string]bool
	RequiredCheckIDs map[string]bool
}

type DomainFactReferenceMode uint8

const (
	DomainFactResolvedReferences DomainFactReferenceMode = iota
	DomainFactUnverifiedReferences
)

var domainFactIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ParseDomainFact parses and validates one basename named <id>.json. It reads
// no files, runs no commands and returns no partially validated fact on error.
func ParseDomainFact(name string, data []byte, refs DomainFactReferences) (DomainFact, error) {
	var fact DomainFact
	fail := func(detail string) (DomainFact, error) {
		return DomainFact{}, newFailure(KindInvalidNoteProof, "parse_domain_fact", "domain fact "+name+" "+detail, false, "repair the fact shard or its resolved references")
	}
	if refs.ValidationMode != DomainFactResolvedReferences && refs.ValidationMode != DomainFactUnverifiedReferences {
		return fail("has an unknown reference validation mode")
	}
	if len(data) == 0 || len(data) > DomainFactMaxBytes || !utf8.Valid(data) {
		return fail("must be UTF-8 JSON within 16384 bytes")
	}
	if err := decodeStrictKnowledgeJSON(data, &fact); err != nil {
		return fail("is not strict JSON: " + err.Error())
	}
	if err := domainFactJSONShape(data, reflect.TypeFor[DomainFact]()); err != nil {
		return fail(err.Error())
	}
	if fact.SchemaVersion != DomainFactSchemaVersion || !domainFactIDPattern.MatchString(fact.ID) || name != fact.ID+".json" {
		return fail("requires schema_version 1.0 and a basename matching its bounded id")
	}
	domains := map[string]bool{}
	for _, domain := range refs.Registry.Domains {
		if domain.Status == "current" {
			domains[domain.DomainID] = true
		}
	}
	if !validManifestID(fact.OwnerDomainID) || !domains[fact.OwnerDomainID] || !validPackageMapPath(fact.OwningRoot) || strings.Contains(fact.OwningRoot, "\\") {
		return fail("requires a current owner Domain and normalized owning_root")
	}
	root, exists := refs.Roots[fact.OwningRoot]
	if !exists || (root.Role != "domain" && root.Role != "mixed") ||
		(root.Role == "domain" && len(root.OwnerDomainIDs) != 1) ||
		(root.Role == "mixed" && len(root.OwnerDomainIDs) < 2) ||
		!domainFactIDs(root.OwnerDomainIDs, domains, false) || !slices.Contains(root.OwnerDomainIDs, fact.OwnerDomainID) {
		return fail("owning_root must resolve to its Domain's domain or mixed root")
	}
	switch fact.Standing {
	case "law":
		if !domainFactIDs(fact.GoverningLawIDs, refs.AcceptedLawIDs, false) {
			return fail("law standing requires current accepted governing_law_ids")
		}
	case "observed":
		if fact.GoverningLawIDs != nil {
			return fail("observed standing must omit governing_law_ids")
		}
	default:
		return fail("standing must be law or observed")
	}
	variants := map[DomainFactKind]bool{
		DomainFactInvariant: fact.Invariant != nil, DomainFactDataObject: fact.DataObject != nil,
		DomainFactStateAuthority: fact.StateAuthority != nil, DomainFactConfigCoupling: fact.ConfigCoupling != nil,
	}
	if !variants[fact.Kind] {
		return fail("requires the payload named by kind")
	}
	for kind, present := range variants {
		if present && kind != fact.Kind {
			return fail("carries an unrelated variant payload")
		}
	}
	if detail := validateDomainFactPayload(fact, refs, domains); detail != "" {
		return fail(detail)
	}
	return fact, nil
}

func validateDomainFactPayload(fact DomainFact, refs DomainFactReferences, domains map[string]bool) string {
	switch fact.Kind {
	case DomainFactInvariant:
		return validateDomainInvariantFact(fact.Invariant, refs, domains)
	case DomainFactDataObject:
		value := fact.DataObject
		invariants := map[string]bool{}
		for id, kind := range refs.Facts {
			invariants[id] = kind == DomainFactInvariant && domainFactIDPattern.MatchString(id)
		}
		if !domainFactText(value.Name, 256) || !domainFactResource(value.ResourceID) || !domainFactIDs(value.WriterDomainIDs, domains, true) || !domainFactIDs(value.ReaderDomainIDs, domains, true) || !domainFactIDs(value.InvariantIDs, invariants, true) {
			return "data_object requires a bounded name, bounded fact-local resource source, writer/reader Domains and invariant IDs"
		}
	case DomainFactStateAuthority:
		value := fact.StateAuthority
		if !domainFactText(value.State, DomainFactMaxTextBytes) || !domainFactResource(value.AuthoritativeResourceID) || !domainFactText(value.DisagreementBehavior, DomainFactMaxTextBytes) || len(value.DerivedCopies) > DomainFactMaxItems {
			return "state_authority requires bounded state/disagreement behavior and a bounded fact-local authoritative resource source"
		}
		seen := map[string]bool{value.AuthoritativeResourceID: true}
		for _, copy := range value.DerivedCopies {
			if !domainFactResource(copy.ResourceID) || seen[copy.ResourceID] || !domainFactRepositoryAnchor(copy.RebuildRoute, refs) {
				return "derived_copies require distinct bounded resource sources and repository rebuild routes"
			}
			seen[copy.ResourceID] = true
		}
	case DomainFactConfigCoupling:
		value := fact.ConfigCoupling
		if !domainFactText(value.Setting, 256) || !domainFactText(value.Source, DomainFactMaxTextBytes) || !domainFactRepositoryAnchor(value.ReadBy, refs) || !domainFactText(value.Effect, DomainFactMaxTextBytes) || len(value.Environments) == 0 || len(value.Environments) > DomainFactMaxItems {
			return "config_coupling requires bounded setting/source/effect, resolved read_by and 1..32 environments"
		}
		seen := map[string]bool{}
		for _, environment := range value.Environments {
			if !domainFactText(environment, 128) || seen[environment] {
				return "environments must be unique bounded nonempty strings"
			}
			seen[environment] = true
		}
	}
	return ""
}

func validateDomainInvariantFact(value *DomainInvariantFact, refs DomainFactReferences, domains map[string]bool) string {
	if !domainFactText(value.Statement, DomainFactMaxTextBytes) || !domainFactIDs(value.AppliesToDomainIDs, domains, false) || len(value.EnforcedBy) == 0 || len(value.EnforcedBy) > DomainFactMaxItems {
		return "invariant requires a bounded statement, current applies_to_domain_ids and 1..32 enforced_by anchors"
	}
	seen := map[DomainFactEnforcementAnchor]bool{}
	for _, anchor := range value.EnforcedBy {
		resolved := refs.RequiredCheckAnchors[anchor]
		if refs.ValidationMode == DomainFactUnverifiedReferences {
			resolved = refs.RequiredCheckIDs[anchor.CheckID]
		}
		if !domainFactAnchorKinds[anchor.Kind] || !domainFactText(anchor.Value, maxDomainFactAnchorValue) || !domainFactIDPattern.MatchString(anchor.CheckID) || !resolved || seen[anchor] {
			return "enforced_by must name unique resolved CD-0249 anchors in a required check"
		}
		seen[anchor] = true
	}
	return ""
}

func domainFactRepositoryAnchor(anchor DomainFactRepositoryAnchor, refs DomainFactReferences) bool {
	known := refs.RepositoryAnchors[anchor]
	if refs.ValidationMode == DomainFactUnverifiedReferences {
		known = refs.ProjectIDs[anchor.ProjectID]
	}
	return validManifestID(anchor.ProjectID) && validPackageMapPath(anchor.Path) &&
		!strings.Contains(anchor.Path, "\\") && domainFactText(anchor.Symbol, 256) && known
}

// domainFactResource admits a bounded fact-local source identifier string.
func domainFactResource(id string) bool {
	return id != "" && len(id) <= 256
}

func domainFactText(value string, maxBytes int) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && len(value) <= maxBytes && utf8.ValidString(value)
}

func domainFactIDs(ids []string, resolved map[string]bool, allowEmpty bool) bool {
	if (!allowEmpty && len(ids) == 0) || len(ids) > DomainFactMaxItems {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validManifestID(id) || !resolved[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// The standard decoder accepts case-folded field names and null scalars.
// Require exact JSON tags and every non-optional field, including empty arrays.
// The preceding strict decode already proves scalar and container types.
func domainFactJSONShape(data json.RawMessage, shape reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("must omit optional fields rather than use null")
	}
	if shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch shape.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		for index := 0; index < shape.NumField(); index++ {
			field := shape.Field(index)
			name, optional, _ := strings.Cut(field.Tag.Get("json"), ",")
			value, present := object[name]
			if !present && optional != "omitempty" {
				return fmt.Errorf("requires field %s", name)
			}
			if present {
				if err := domainFactJSONShape(value, field.Type); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				delete(object, name)
			}
		}
		if len(object) != 0 {
			return fmt.Errorf("contains noncanonical field names")
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := domainFactJSONShape(value, shape.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
