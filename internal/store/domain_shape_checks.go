package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	DomainShapeProtocolVersion = "1.0"
	DomainShapeMaxBytes        = 32768
	DomainShapeMaxItems        = 512
)

// DomainShapeSubject binds observations to a Product's exact input revision.
// ContentHash identifies the caller's pinned roots/graph/fact input, not a run.
type DomainShapeSubject struct {
	ProductID   string `json:"product_id"`
	ProjectID   string `json:"project_id"`
	CommitOID   string `json:"commit_oid"`
	ContentHash string `json:"content_hash"`
}

type DomainShapeRoot struct {
	Path          string   `json:"path"`
	Role          string   `json:"role"`
	OwnerDomainID string   `json:"owner_domain_id,omitempty"`
	DomainIDs     []string `json:"domain_ids,omitempty"`
}

type DomainShapeEdge struct {
	SourceDomainID string `json:"source_domain_id"`
	TargetDomainID string `json:"target_domain_id"`
}

// DomainShapeImport names declared roots, never individual source files. The Product's
// checker owns source-language resolution and must report its complete inventory.
type DomainShapeImport struct {
	SourceRoot string `json:"source_root"`
	TargetRoot string `json:"target_root"`
}

type DomainShapeRatchet struct {
	SchemaVersion string              `json:"schema_version"`
	MixedRoots    []string            `json:"mixed_roots"`
	Imports       []DomainShapeImport `json:"imports"`
}

// DomainShapeCheckInput is supplied from already validated Product-scoped facts
// and package-map 1.1 roots. It is not another fact/law/reference resolver.
type DomainShapeCheckInput struct {
	SchemaVersion      string                        `json:"schema_version"`
	Operation          string                        `json:"operation"`
	Subject            DomainShapeSubject            `json:"subject"`
	Roots              []DomainShapeRoot             `json:"roots"`
	DependsOn          []DomainShapeEdge             `json:"depends_on"`
	Facts              []DomainFact                  `json:"facts"`
	RepositoryAnchors  []DomainFactRepositoryAnchor  `json:"repository_anchors"`
	EnforcementAnchors []DomainFactEnforcementAnchor `json:"enforcement_anchors"`
	RequiredChecks     []string                      `json:"required_checks"`
	Strict             bool                          `json:"strict"`
	Ratchet            DomainShapeRatchet            `json:"ratchet"`
}

type DomainShapeDataObject struct {
	OwningRoot string `json:"owning_root"`
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
}

type DomainShapeConfigRead struct {
	OwningRoot string                     `json:"owning_root"`
	Setting    string                     `json:"setting"`
	Source     string                     `json:"source"`
	ReadBy     DomainFactRepositoryAnchor `json:"read_by"`
}

type DomainShapeRepositoryResolution struct {
	Anchor   DomainFactRepositoryAnchor `json:"anchor"`
	Resolved bool                       `json:"resolved"`
}

// DomainShapeEnforcementResolution records observed check membership and anchor resolution.
// RequiredCheckIDs comes from the Product's check configuration,
// not an echo of the requested check_id. Resolved independently reports whether
// the anchor exists. Neither field claims the check executed successfully.
type DomainShapeEnforcementResolution struct {
	Anchor           DomainFactEnforcementAnchor `json:"anchor"`
	Resolved         bool                        `json:"resolved"`
	RequiredCheckIDs []string                    `json:"required_check_ids"`
}

type DomainShapeCheckOutput struct {
	SchemaVersion      string                             `json:"schema_version"`
	Operation          string                             `json:"operation"`
	Subject            DomainShapeSubject                 `json:"subject"`
	Complete           bool                               `json:"complete"`
	Imports            []DomainShapeImport                `json:"imports,omitempty"`
	DataObjects        []DomainShapeDataObject            `json:"data_objects,omitempty"`
	ConfigReads        []DomainShapeConfigRead            `json:"config_reads,omitempty"`
	RepositoryAnchors  []DomainShapeRepositoryResolution  `json:"repository_anchors,omitempty"`
	EnforcementAnchors []DomainShapeEnforcementResolution `json:"enforcement_anchors,omitempty"`
}

// MarshalJSON keeps the selected empty inventory explicit instead of omitting
// it. An omitted inventory in a command response is not a complete observation.
func (output DomainShapeCheckOutput) MarshalJSON() ([]byte, error) {
	if (output.Operation != "boundary" && output.Imports != nil) || (output.Operation != "data_objects" && output.DataObjects != nil) || (output.Operation != "config_reads" && output.ConfigReads != nil) || (output.Operation != "references" && (output.RepositoryAnchors != nil || output.EnforcementAnchors != nil)) {
		return nil, fmt.Errorf("output carries unrelated inventories")
	}
	fields := map[string]any{"schema_version": output.SchemaVersion, "operation": output.Operation, "subject": output.Subject, "complete": output.Complete}
	empty := func(value any) any {
		if reflect.ValueOf(value).IsNil() {
			return []any{}
		}
		return value
	}
	switch output.Operation {
	case "boundary":
		fields["imports"] = empty(output.Imports)
	case "data_objects":
		fields["data_objects"] = empty(output.DataObjects)
	case "config_reads":
		fields["config_reads"] = empty(output.ConfigReads)
	case "references":
		fields["repository_anchors"] = empty(output.RepositoryAnchors)
		fields["enforcement_anchors"] = empty(output.EnforcementAnchors)
	default:
		return nil, fmt.Errorf("unknown domain shape output operation")
	}
	return json.Marshal(fields)
}

// DomainShapeFinding uses the existing coverage vocabulary. Verification separately makes
// absence of an extractor/check visibly unverified, never a satisfied result.
type DomainShapeFinding struct {
	State   string `json:"state"`
	Code    string `json:"code"`
	Subject string `json:"subject"`
}

type DomainShapeEvaluation struct {
	State                string                               `json:"state"`
	Verification         string                               `json:"verification"`
	Findings             []DomainShapeFinding                 `json:"findings"`
	RepositoryAnchors    map[DomainFactRepositoryAnchor]bool  `json:"-"`
	RequiredCheckAnchors map[DomainFactEnforcementAnchor]bool `json:"-"`
}

var domainShapeCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var domainShapeHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func domainShapeJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > DomainShapeMaxBytes || !utf8.Valid(raw) {
		return fmt.Errorf("domain shape JSON must be UTF-8 within %d bytes", DomainShapeMaxBytes)
	}
	if err := decodeStrictKnowledgeJSON(raw, target); err != nil {
		return err
	}
	return domainFactJSONShape(raw, reflect.TypeOf(target).Elem())
}

func domainShapeRootOwners(root DomainShapeRoot) []string {
	if root.Role == "domain" {
		return []string{root.OwnerDomainID}
	}
	return root.DomainIDs
}

func validateDomainShapeInput(input DomainShapeCheckInput) error {
	if input.Roots == nil || input.DependsOn == nil || input.Facts == nil || input.RepositoryAnchors == nil || input.EnforcementAnchors == nil || input.RequiredChecks == nil || input.Ratchet.MixedRoots == nil || input.Ratchet.Imports == nil {
		return fmt.Errorf("domain shape inventories must be arrays, including empty inventories")
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > DomainShapeMaxBytes {
		return fmt.Errorf("domain shape input exceeds wire bound")
	}
	if input.SchemaVersion != DomainShapeProtocolVersion || !validManifestID(input.Subject.ProductID) || !validManifestID(input.Subject.ProjectID) || !domainShapeCommit.MatchString(input.Subject.CommitOID) || !domainShapeHash.MatchString(input.Subject.ContentHash) {
		return fmt.Errorf("domain shape requires protocol 1.0 and exact pinned subject")
	}
	switch input.Operation {
	case "boundary", "data_objects", "config_reads", "references":
	default:
		return fmt.Errorf("unknown domain shape operation %q", input.Operation)
	}
	if len(input.Roots) > DomainShapeMaxItems || len(input.DependsOn) > DomainShapeMaxItems || len(input.Facts) > DomainShapeMaxItems || len(input.RepositoryAnchors) > DomainShapeMaxItems || len(input.EnforcementAnchors) > DomainShapeMaxItems || len(input.RequiredChecks) > 64 {
		return fmt.Errorf("domain shape input inventory exceeds bound")
	}
	if err := validateDomainShapeRoots(input.Roots); err != nil {
		return err
	}
	if err := validateDomainShapeEdges(input.DependsOn); err != nil {
		return err
	}
	checks, err := validateDomainShapeRequiredChecks(input.RequiredChecks)
	if err != nil {
		return err
	}
	if err := validateDomainShapeRatchet(input.Ratchet); err != nil {
		return err
	}
	return validateDomainShapeRequestedAnchors(input, checks)
}

func validateDomainShapeRoots(entries []DomainShapeRoot) error {
	roots := map[string]bool{}
	for _, root := range entries {
		if !validPackageMapPath(root.Path) || strings.ContainsAny(root.Path, "\\*?[]") || roots[root.Path] {
			return fmt.Errorf("invalid or duplicate root %q", root.Path)
		}
		roots[root.Path] = true
		if err := validateDomainShapeRootOwnership(root); err != nil {
			return err
		}
	}
	return nil
}

func validateDomainShapeRootOwnership(root DomainShapeRoot) error {
	switch root.Role {
	case "domain":
		if !validManifestID(root.OwnerDomainID) || root.DomainIDs != nil {
			return fmt.Errorf("domain root requires exactly one owner")
		}
	case "mixed":
		if root.OwnerDomainID != "" || len(root.DomainIDs) < 2 || len(root.DomainIDs) > 16 {
			return fmt.Errorf("mixed root requires 2..16 owners")
		}
		seen := map[string]bool{}
		for _, id := range root.DomainIDs {
			if !validManifestID(id) || seen[id] {
				return fmt.Errorf("invalid mixed owner")
			}
			seen[id] = true
		}
	case "composition", "test_support":
		if root.OwnerDomainID != "" || root.DomainIDs != nil {
			return fmt.Errorf("non-owning root carries owners")
		}
	default:
		return fmt.Errorf("unknown root role")
	}
	return nil
}

func validateDomainShapeEdges(entries []DomainShapeEdge) error {
	edges := map[DomainShapeEdge]bool{}
	for _, edge := range entries {
		if !validManifestID(edge.SourceDomainID) || !validManifestID(edge.TargetDomainID) || edges[edge] {
			return fmt.Errorf("invalid or duplicate depends_on edge")
		}
		edges[edge] = true
	}
	return nil
}

func validateDomainShapeRequiredChecks(entries []string) (map[string]bool, error) {
	checks := map[string]bool{}
	for _, id := range entries {
		if !projectToolID.MatchString(id) || checks[id] {
			return nil, fmt.Errorf("invalid or duplicate required check")
		}
		checks[id] = true
	}
	return checks, nil
}

func validateDomainShapeRatchet(ratchet DomainShapeRatchet) error {
	if ratchet.SchemaVersion != DomainShapeProtocolVersion || len(ratchet.MixedRoots) > DomainShapeMaxItems || len(ratchet.Imports) > DomainShapeMaxItems {
		return fmt.Errorf("invalid ratchet version or size")
	}
	seenRoots := map[string]bool{}
	for _, root := range ratchet.MixedRoots {
		if !validPackageMapPath(root) || seenRoots[root] {
			return fmt.Errorf("invalid or duplicate ratchet root")
		}
		seenRoots[root] = true
	}
	seenImports := map[DomainShapeImport]bool{}
	for _, imp := range ratchet.Imports {
		if !validPackageMapPath(imp.SourceRoot) || !validPackageMapPath(imp.TargetRoot) || seenImports[imp] {
			return fmt.Errorf("invalid or duplicate ratchet import")
		}
		seenImports[imp] = true
	}
	return nil
}

func validateDomainShapeRequestedAnchors(input DomainShapeCheckInput, checks map[string]bool) error {
	seenRepo := map[DomainFactRepositoryAnchor]bool{}
	for _, a := range input.RepositoryAnchors {
		if !validManifestID(a.ProjectID) || !validPackageMapPath(a.Path) || !domainFactText(a.Symbol, 256) || seenRepo[a] {
			return fmt.Errorf("invalid or duplicate requested repository anchor")
		}
		seenRepo[a] = true
	}
	seenEnforcement := map[DomainFactEnforcementAnchor]bool{}
	for _, a := range input.EnforcementAnchors {
		if !coverageAnchorKinds[a.Kind] || !domainFactText(a.Value, maxCoverageAnchorValue) || !checks[a.CheckID] || seenEnforcement[a] {
			return fmt.Errorf("invalid or non-required requested enforcement anchor")
		}
		seenEnforcement[a] = true
	}
	return nil
}

// ParseDomainShapeCheckOutput refuses whole
// oversized, duplicate-key, unknown-field, null or incomplete wire documents.
func ParseDomainShapeCheckOutput(raw []byte) (DomainShapeCheckOutput, error) {
	var output DomainShapeCheckOutput
	if err := domainShapeJSON(raw, &output); err != nil {
		return output, err
	}
	if output.SchemaVersion != DomainShapeProtocolVersion || !output.Complete {
		return output, fmt.Errorf("domain shape output must declare complete protocol 1.0 observations")
	}
	if !validManifestID(output.Subject.ProductID) || !validManifestID(output.Subject.ProjectID) || !domainShapeCommit.MatchString(output.Subject.CommitOID) || !domainShapeHash.MatchString(output.Subject.ContentHash) {
		return output, fmt.Errorf("output requires an exact pinned subject")
	}
	if len(output.Imports) > DomainShapeMaxItems || len(output.DataObjects) > DomainShapeMaxItems || len(output.ConfigReads) > DomainShapeMaxItems || len(output.RepositoryAnchors) > DomainShapeMaxItems || len(output.EnforcementAnchors) > DomainShapeMaxItems {
		return output, fmt.Errorf("output inventory exceeds bound")
	}
	// Exactly the selected inventory must be present, even when it is empty.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	selected := map[string][]string{"boundary": {"imports"}, "data_objects": {"data_objects"}, "config_reads": {"config_reads"}, "references": {"repository_anchors", "enforcement_anchors"}}
	keys, ok := selected[output.Operation]
	if !ok {
		return output, fmt.Errorf("unknown domain shape output operation")
	}
	for _, key := range []string{"imports", "data_objects", "config_reads", "repository_anchors", "enforcement_anchors"} {
		_, exists := fields[key]
		if exists != slices.Contains(keys, key) {
			return output, fmt.Errorf("output inventory %s does not match operation", key)
		}
	}
	return output, nil
}

// EvaluateDomainShapeCheck is pure: no source reads, subprocesses, DB writes,
// ratchet updates or workflow evidence. A nil output is explicitly unverified.
func EvaluateDomainShapeCheck(input DomainShapeCheckInput, output *DomainShapeCheckOutput) (DomainShapeEvaluation, error) {
	result := DomainShapeEvaluation{State: "unmeasured", Verification: "unverified", Findings: []DomainShapeFinding{}, RepositoryAnchors: map[DomainFactRepositoryAnchor]bool{}, RequiredCheckAnchors: map[DomainFactEnforcementAnchor]bool{}}
	if err := validateDomainShapeInput(input); err != nil {
		return result, err
	}
	if output == nil {
		result.Findings = append(result.Findings, DomainShapeFinding{"unmeasured", "checker_absent", input.Operation})
		if input.Operation == "boundary" {
			evaluateDomainShapeMixedRatchet(input, &result)
		}
		return result, nil
	}
	if err := validateDomainShapeObservedOutput(input, output); err != nil {
		return result, err
	}
	result.State, result.Verification = "satisfied", "verified"
	roots := map[string]DomainShapeRoot{}
	for _, root := range input.Roots {
		roots[root.Path] = root
	}
	var err error
	switch input.Operation {
	case "boundary":
		err = evaluateDomainShapeBoundary(input, output.Imports, roots, &result)
	case "data_objects":
		err = evaluateDomainShapeDataObjects(input.Facts, output.DataObjects, roots, &result)
	case "config_reads":
		err = evaluateDomainShapeConfigReads(input.Facts, output.ConfigReads, roots, &result)
	case "references":
		err = evaluateDomainShapeReferences(input, output, &result)
	}
	return result, err
}

func validateDomainShapeObservedOutput(input DomainShapeCheckInput, output *DomainShapeCheckOutput) error {
	raw, err := json.Marshal(output)
	if err != nil || len(raw) > DomainShapeMaxBytes {
		return fmt.Errorf("domain shape output exceeds wire bound or carries unrelated inventories")
	}
	// Revalidate typed callers as well as wire callers without omitempty dropping
	// empty inventories: evaluate the same exact shape the parser accepts below.
	if output.SchemaVersion != input.SchemaVersion || output.Operation != input.Operation || output.Subject != input.Subject || !output.Complete {
		return fmt.Errorf("output is incomplete or does not match the pinned request")
	}
	if len(output.Imports) > DomainShapeMaxItems || len(output.DataObjects) > DomainShapeMaxItems || len(output.ConfigReads) > DomainShapeMaxItems || len(output.RepositoryAnchors) > DomainShapeMaxItems || len(output.EnforcementAnchors) > DomainShapeMaxItems {
		return fmt.Errorf("output inventory exceeds bound")
	}
	if (input.Operation != "boundary" && output.Imports != nil) || (input.Operation != "data_objects" && output.DataObjects != nil) || (input.Operation != "config_reads" && output.ConfigReads != nil) || (input.Operation != "references" && (output.RepositoryAnchors != nil || output.EnforcementAnchors != nil)) {
		return fmt.Errorf("output carries unrelated inventories")
	}
	return nil
}

func (result *DomainShapeEvaluation) addFinding(code, subject string) {
	result.State = "outstanding"
	result.Findings = append(result.Findings, DomainShapeFinding{"outstanding", code, subject})
}

// The mixed-root inventory is declared, not a source-language measurement.
func evaluateDomainShapeMixedRatchet(input DomainShapeCheckInput, result *DomainShapeEvaluation) {
	for _, root := range input.Roots {
		if root.Role == "mixed" && input.Strict && !slices.Contains(input.Ratchet.MixedRoots, root.Path) {
			result.addFinding("new_mixed_root", root.Path)
		}
	}
}

func evaluateDomainShapeBoundary(input DomainShapeCheckInput, imports []DomainShapeImport, roots map[string]DomainShapeRoot, result *DomainShapeEvaluation) error {
	allowed := map[DomainShapeEdge]bool{}
	for _, edge := range input.DependsOn {
		allowed[edge] = true
	}
	evaluateDomainShapeMixedRatchet(input, result)
	seen := map[DomainShapeImport]bool{}
	for _, imp := range imports {
		source, sourceOK := roots[imp.SourceRoot]
		target, targetOK := roots[imp.TargetRoot]
		if !sourceOK || !targetOK || seen[imp] {
			return fmt.Errorf("import names undeclared roots or is duplicated")
		}
		seen[imp] = true
		violation := domainShapeImportViolatesGraph(source, target, allowed)
		if violation && (!input.Strict || !slices.Contains(input.Ratchet.Imports, imp)) {
			result.addFinding("disallowed_import", imp.SourceRoot+" -> "+imp.TargetRoot)
		}
	}
	return nil
}

func domainShapeImportViolatesGraph(source, target DomainShapeRoot, allowed map[DomainShapeEdge]bool) bool {
	violation := false
	for _, from := range domainShapeRootOwners(source) {
		for _, to := range domainShapeRootOwners(target) {
			if from != to && !allowed[DomainShapeEdge{from, to}] {
				violation = true
			}
		}
	}
	return violation
}

func evaluateDomainShapeDataObjects(facts []DomainFact, items []DomainShapeDataObject, roots map[string]DomainShapeRoot, result *DomainShapeEvaluation) error {
	observed := map[DomainShapeDataObject]bool{}
	for _, item := range items {
		root, ok := roots[item.OwningRoot]
		if !ok || len(domainShapeRootOwners(root)) == 0 || !domainFactText(item.Name, 256) || !domainFactText(item.ResourceID, 256) || observed[item] {
			return fmt.Errorf("invalid or duplicate extracted data object")
		}
		observed[item] = true
	}
	authored := map[DomainShapeDataObject]bool{}
	for _, fact := range facts {
		if fact.Kind == DomainFactDataObject && fact.DataObject != nil {
			item := DomainShapeDataObject{fact.OwningRoot, fact.DataObject.ResourceID, fact.DataObject.Name}
			authored[item] = true
			if !observed[item] {
				result.addFinding("orphan_fact", fact.ID)
			}
		}
	}
	for _, item := range items {
		if !authored[item] {
			result.addFinding("missing_fact", item.OwningRoot+":"+item.ResourceID+":"+item.Name)
		}
	}
	return nil
}

func evaluateDomainShapeConfigReads(facts []DomainFact, items []DomainShapeConfigRead, roots map[string]DomainShapeRoot, result *DomainShapeEvaluation) error {
	observed := map[DomainShapeConfigRead]bool{}
	for _, item := range items {
		root, ok := roots[item.OwningRoot]
		if !ok || len(domainShapeRootOwners(root)) == 0 || !domainFactText(item.Setting, 256) || !domainFactText(item.Source, 1024) || !validManifestID(item.ReadBy.ProjectID) || !validPackageMapPath(item.ReadBy.Path) || !domainFactText(item.ReadBy.Symbol, 256) || observed[item] {
			return fmt.Errorf("invalid or duplicate extracted config read")
		}
		observed[item] = true
	}
	authored := map[DomainShapeConfigRead]bool{}
	for _, fact := range facts {
		if fact.Kind == DomainFactConfigCoupling && fact.ConfigCoupling != nil {
			item := DomainShapeConfigRead{fact.OwningRoot, fact.ConfigCoupling.Setting, fact.ConfigCoupling.Source, fact.ConfigCoupling.ReadBy}
			authored[item] = true
			if !observed[item] {
				result.addFinding("orphan_fact", fact.ID)
			}
		}
	}
	for _, item := range items {
		if !authored[item] {
			result.addFinding("missing_fact", item.OwningRoot+":"+item.Setting)
		}
	}
	return nil
}

func evaluateDomainShapeReferences(input DomainShapeCheckInput, output *DomainShapeCheckOutput, result *DomainShapeEvaluation) error {
	if err := evaluateDomainShapeRepositoryAnchors(input.RepositoryAnchors, output.RepositoryAnchors, result); err != nil {
		return err
	}
	if err := evaluateDomainShapeEnforcementAnchors(input, output.EnforcementAnchors, result); err != nil {
		return err
	}
	if len(output.RepositoryAnchors) != len(input.RepositoryAnchors) || len(output.EnforcementAnchors) != len(input.EnforcementAnchors) {
		return fmt.Errorf("reference output omits requested observations")
	}
	return nil
}

func evaluateDomainShapeRepositoryAnchors(requested []DomainFactRepositoryAnchor, observations []DomainShapeRepositoryResolution, result *DomainShapeEvaluation) error {
	seen := map[DomainFactRepositoryAnchor]bool{}
	for _, item := range observations {
		if !slices.Contains(requested, item.Anchor) || seen[item.Anchor] {
			return fmt.Errorf("unsolicited or duplicate repository resolution")
		}
		seen[item.Anchor] = true
		result.RepositoryAnchors[item.Anchor] = item.Resolved
		if !item.Resolved {
			result.addFinding("unresolved_anchor", item.Anchor.Path+":"+item.Anchor.Symbol)
		}
	}
	return nil
}

func evaluateDomainShapeEnforcementAnchors(input DomainShapeCheckInput, observations []DomainShapeEnforcementResolution, result *DomainShapeEvaluation) error {
	seen := map[DomainFactEnforcementAnchor]bool{}
	for _, item := range observations {
		if !slices.Contains(input.EnforcementAnchors, item.Anchor) || seen[item.Anchor] || len(item.RequiredCheckIDs) > 64 {
			return fmt.Errorf("unsolicited or duplicate enforcement resolution")
		}
		seen[item.Anchor] = true
		members, err := validateDomainShapeObservedMembership(input.RequiredChecks, item.RequiredCheckIDs)
		if err != nil {
			return err
		}
		resolved := item.Resolved && members[item.Anchor.CheckID]
		result.RequiredCheckAnchors[item.Anchor] = resolved
		if !item.Resolved {
			result.addFinding("unresolved_anchor", item.Anchor.Value)
		} else if !resolved {
			result.addFinding("anchor_not_required", item.Anchor.Value)
		}
	}
	return nil
}

func validateDomainShapeObservedMembership(required, observed []string) (map[string]bool, error) {
	members := map[string]bool{}
	for _, id := range observed {
		if !slices.Contains(required, id) || members[id] {
			return nil, fmt.Errorf("observed membership names undeclared/non-required or duplicate check")
		}
		members[id] = true
	}
	return members, nil
}

// DomainShapeCommandRunner is an explicit host/operator seam. The caller owns
// authorization, selecting the declared manifest and materializing a clean
// checkout matching Subject. Reads and evaluation never call this interface.
type DomainShapeCommandRunner interface {
	Run(context.Context, string, []string, []byte) ([]byte, error)
}

// ExecDomainShapeCommandRunner executes argv directly, never through a shell.
// It is not a sandbox: running Product code needs the caller's existing host
// authority. It retains at most 32 KiB per stream and kills the process group
// on overflow, cancellation or deadline; inherited output pipes cannot hang Wait.
type ExecDomainShapeCommandRunner struct{ Timeout time.Duration }

type domainShapeBoundedWriter struct {
	// Do not embed Buffer: its promoted ReadFrom lets io.Copy bypass Write.
	buffer   bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func (writer *domainShapeBoundedWriter) Write(data []byte) (int, error) {
	if len(data) > DomainShapeMaxBytes-writer.buffer.Len() {
		writer.exceeded = true
		writer.cancel()
		return 0, fmt.Errorf("domain shape output exceeds byte bound")
	}
	return writer.buffer.Write(data)
}

func (runner ExecDomainShapeCommandRunner) Run(ctx context.Context, root string, argv []string, input []byte) ([]byte, error) {
	if !filepath.IsAbs(root) || len(argv) == 0 || len(argv) > 64 || len(input) > DomainShapeMaxBytes {
		return nil, fmt.Errorf("invalid explicit command, checkout or input bound")
	}
	for _, arg := range argv {
		if arg == "" || len(arg) > 512 || strings.ContainsRune(arg, 0) {
			return nil, fmt.Errorf("invalid argv element")
		}
	}
	budget := runner.Timeout
	if budget == 0 {
		budget = 30 * time.Second
	}
	if budget < 0 || budget > 5*time.Minute {
		return nil, fmt.Errorf("command timeout must be positive and at most five minutes")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // Explicit operator authority selects pinned Product argv; arguments stay separate and no shell is invoked.
	cmd.Dir, cmd.Stdin = root, bytes.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	stdout, stderr := &domainShapeBoundedWriter{cancel: cancel}, &domainShapeBoundedWriter{cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if cmd.Process != nil {
		// A successful parent may leave children holding inherited output pipes.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("domain shape command output exceeds %d bytes per stream", DomainShapeMaxBytes)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("domain shape command failed: %w", err)
	}
	return bytes.Clone(stdout.buffer.Bytes()), nil
}

// RunDomainShapeCheck resolves only an explicitly selected declared operation.
// It cannot turn a required-check declaration into workflow execution evidence.
func RunDomainShapeCheck(ctx context.Context, runner DomainShapeCommandRunner, root string, tooling *ProjectToolingManifest, input DomainShapeCheckInput) (DomainShapeEvaluation, error) {
	if err := validateDomainShapeInput(input); err != nil {
		return DomainShapeEvaluation{}, err
	}
	if tooling == nil || tooling.DomainShape == nil {
		return EvaluateDomainShapeCheck(input, nil)
	}
	if err := validateDomainShapeTooling(tooling.DomainShape, tooling.Declared); err != nil {
		return DomainShapeEvaluation{}, err
	}
	if !slices.Equal(input.RequiredChecks, tooling.DomainShape.RequiredChecks) {
		return DomainShapeEvaluation{}, fmt.Errorf("request required_checks differs from tooling declaration")
	}
	shape := tooling.DomainShape
	id := map[string]string{"boundary": shape.BoundaryCheck, "data_objects": shape.DataObjectExtractor, "config_reads": shape.ConfigExtractor, "references": shape.ReferenceCheck}[input.Operation]
	if id == "" {
		return EvaluateDomainShapeCheck(input, nil)
	}
	if runner == nil {
		return DomainShapeEvaluation{}, fmt.Errorf("explicit Product command runner is required")
	}
	var argv []string
	for _, tool := range tooling.Declared {
		if tool.ID == id {
			if !projectToolingPlainInvocation(tool.Invocation) {
				return DomainShapeEvaluation{}, fmt.Errorf("tool invocation is not plain argv")
			}
			argv = strings.Fields(tool.Invocation)
		}
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > DomainShapeMaxBytes {
		return DomainShapeEvaluation{}, fmt.Errorf("domain shape request exceeds wire bound")
	}
	response, err := runner.Run(ctx, root, argv, raw)
	if err != nil {
		return DomainShapeEvaluation{}, err
	}
	output, err := ParseDomainShapeCheckOutput(response)
	if err != nil {
		return DomainShapeEvaluation{}, err
	}
	return EvaluateDomainShapeCheck(input, &output)
}
