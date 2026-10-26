package store

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

const domainShapeRatchetPath = knowledgeShardRoot + "/domain-shape-ratchet.json"
const domainShapeFactsDir = knowledgeShardRoot + "/facts"

// DomainShapeSetupRequest names the selected home's working tree and stable
// Product/Project keys, not a template repository. Setup neither registers the
// home nor commits files. Git-derived reads see these files only after commit.
type DomainShapeSetupRequest struct {
	RepoPath   string
	ProductKey string
	ProjectKey string
}

// DomainShapeSetupResult reports paths relative to RepoPath, including on an
// apply error. Created includes directories. Updated contains only tooling when
// adding the optional empty declaration; existing identities are never rewritten.
type DomainShapeSetupResult struct {
	Created []string `json:"created"`
	Updated []string `json:"updated"`
}

type domainShapeSetupEntry struct {
	Mode fs.FileMode
	Data []byte
}

// DomainShapeSetupPlan is an immutable, validated filesystem proposal. Prepare
// performs no writes. Apply revalidates the complete observed tree before its
// first write and refuses drift. It is not a filesystem/SQLite transaction.
// Callers must serialize designation and setup for this home. A write failure
// can leave the reported prefix behind; a DB failure after Apply leaves valid,
// unregistered files. Re-prepare to resume, never blindly roll back user files.
type DomainShapeSetupPlan struct {
	request  DomainShapeSetupRequest
	observed map[string]domainShapeSetupEntry
	writes   map[string][]byte
	dirs     []string
}

func domainShapeSetupError(detail string) error {
	return newFailure(KindInvalidPayload, "domain_shape_setup", detail, false, "repair the selected home's inputs and prepare setup again")
}

// PrepareDomainShapeSetup builds a generic empty shape, preserving valid authored
// files. It validates manifest/registry/package joins with the normal readers.
// Existing facts are checked as strict typed JSON, not claimed extractor-verified;
// resource and executable-anchor resolution belongs to explicit Product checks.
func PrepareDomainShapeSetup(request DomainShapeSetupRequest) (*DomainShapeSetupPlan, error) {
	if !filepath.IsAbs(request.RepoPath) || filepath.Clean(request.RepoPath) != request.RepoPath || !validProductKey(request.ProductKey) || !validProductKey(request.ProjectKey) {
		return nil, domainShapeSetupError("an absolute normalized repository path and valid Product/Project keys are required")
	}
	root, err := openDomainShapeSetupRoot(request.RepoPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	plan := &DomainShapeSetupPlan{request: request, observed: map[string]domainShapeSetupEntry{}, writes: map[string][]byte{}}
	if err := plan.observeSetupHome(root); err != nil {
		return nil, err
	}
	registry := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: request.ProductKey, RootDomainID: "product-root:" + request.ProductKey,
		Domains: []KnowledgeDomain{{DomainID: "product-root:" + request.ProductKey, Name: request.ProductKey, Purpose: "Product-wide behavior.", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
	}
	registryBytes := plan.defaultJSON(knowledgeRegistryPath, registry)
	registry = KnowledgeDomainRegistry{}
	if err := decodeStrictKnowledgeJSON(registryBytes, &registry); err != nil {
		return nil, domainShapeSetupError("domain registry is not strict JSON: " + err.Error())
	}
	if registry.ProductKey != request.ProductKey {
		return nil, domainShapeSetupError("domain registry belongs to a different Product")
	}
	if err := validateKnowledgeDomainRegistry(registry); err != nil {
		return nil, err
	}
	if err := plan.prepareSetupManifest(registryBytes); err != nil {
		return nil, err
	}
	packages, err := plan.prepareSetupPackages(root, registry)
	if err != nil {
		return nil, err
	}
	// Seed declared mixed roots only. Imports need explicit source measurements;
	// setup neither executes a checker nor changes an existing ratchet.
	ratchetBytes := plan.defaultJSON(domainShapeRatchetPath, initialDomainShapeRatchet(packages))
	if err := validateSetupRatchet(ratchetBytes, packages); err != nil {
		return nil, err
	}
	tooling := plan.defaultJSON(projectToolingManifestPath, map[string]any{"schema_version": "1.0", "project": request.ProjectKey, "tools": []any{}, "domain_shape": map[string]any{}})
	if err := plan.prepareTooling(root, tooling); err != nil {
		return nil, err
	}
	return plan, nil
}

func initialDomainShapeRatchet(packages knowledgePackageMapDocument) DomainShapeRatchet {
	ratchet := DomainShapeRatchet{SchemaVersion: DomainShapeProtocolVersion, MixedRoots: []string{}, Imports: []DomainShapeImport{}}
	for _, entry := range packages.Packages {
		if entry.Role == "mixed" || packages.SchemaVersion == packageMapSchemaVersion {
			ratchet.MixedRoots = append(ratchet.MixedRoots, entry.Path)
		}
	}
	sort.Strings(ratchet.MixedRoots)
	return ratchet
}

func (p *DomainShapeSetupPlan) observeSetupHome(root *os.Root) error {
	for _, dir := range []string{".concord", ".concord/docs", knowledgeShardRoot, knowledgeRecordTree, domainShapeFactsDir} {
		info, err := root.Lstat(dir)
		if os.IsNotExist(err) {
			p.dirs = append(p.dirs, dir)
			continue
		}
		if err != nil || !info.IsDir() {
			return domainShapeSetupError("setup directory is unreadable, not a directory, or a symlink: " + dir)
		}
		p.observed[dir] = domainShapeSetupEntry{Mode: info.Mode()}
	}
	if err := p.readTree(root); err != nil {
		return err
	}
	if _, present := p.observed[knowledgeHeadPath]; !present {
		for _, legacy := range []string{"docs/knowledge/manifest.json", knowledgeManifestPath} {
			if _, err := root.Lstat(legacy); !os.IsNotExist(err) {
				return domainShapeSetupError("setup cannot shadow or migrate an existing legacy manifest: " + legacy)
			}
		}
	}
	return nil
}

func (p *DomainShapeSetupPlan) prepareSetupManifest(registryBytes []byte) error {
	head := p.defaultJSON(knowledgeHeadPath, map[string]any{
		"schema_version": knowledgeManifestSchemaCurrent, "supported_kinds": sortedKnowledgeKinds(), "indexed_kinds": sortedKnowledgeKinds(),
		"knowledge_roots": []string{".concord/docs/"}, "dispositions": []any{},
	})
	shards := knowledgeShards{head: head, registry: registryBytes, records: map[string][]byte{}}
	for name, entry := range p.observed {
		if directShardChild(name, knowledgeRecordTree+"/") {
			shards.records[strings.TrimPrefix(name, knowledgeRecordTree+"/")] = entry.Data
		}
		if directShardChild(name, domainShapeFactsDir+"/") {
			var fact DomainFact
			if err := decodeStrictKnowledgeJSON(entry.Data, &fact); err != nil {
				return domainShapeSetupError("fact is not strict JSON: " + name)
			}
			if err := domainFactJSONShape(entry.Data, reflect.TypeFor[DomainFact]()); err != nil {
				return domainShapeSetupError("fact has malformed fields: " + name)
			}
		}
	}
	_, err := composeKnowledgeManifest(shards, manifestSharedHomeRole)
	return err
}

func (p *DomainShapeSetupPlan) prepareSetupPackages(root *os.Root, registry KnowledgeDomainRegistry) (knowledgePackageMapDocument, error) {
	packageBytes := p.defaultJSON(knowledgePackageMapPath, knowledgePackageMapDocument{SchemaVersion: packageMapShapeVersion, Packages: []knowledgePackageEntry{}})
	packages, err := parseKnowledgePackageMap(packageBytes, registry)
	if err != nil {
		return packages, err
	}
	for _, entry := range packages.Packages {
		if err := p.observeDirectory(root, entry.Path); err != nil {
			return packages, err
		}
	}
	for name, entry := range p.observed {
		if directShardChild(name, domainShapeFactsDir+"/") {
			if err := validateSetupFactIdentity(name, entry.Data, registry, packages); err != nil {
				return packages, err
			}
		}
	}
	return packages, nil
}

func (p *DomainShapeSetupPlan) defaultJSON(name string, value any) []byte {
	if entry, ok := p.observed[name]; ok {
		return entry.Data
	}
	data, _ := json.MarshalIndent(value, "", "  ") // Only concrete JSON values enter here.
	data = append(data, '\n')
	p.writes[name] = data
	return data
}

func validateSetupFactIdentity(name string, data []byte, registry KnowledgeDomainRegistry, packages knowledgePackageMapDocument) error {
	var fact DomainFact
	// Prepare already proved strict field shape; this checks the joins owned by
	// these local shards without pretending to resolve federated law/resources.
	_ = json.Unmarshal(data, &fact)
	if fact.SchemaVersion != DomainFactSchemaVersion || !domainFactIDPattern.MatchString(fact.ID) || filepath.Base(name) != fact.ID+".json" {
		return domainShapeSetupError("fact schema or filename identity is invalid: " + name)
	}
	domains := map[string]bool{}
	for _, domain := range registry.Domains {
		domains[domain.DomainID] = domain.Status == "current"
	}
	root, exists := packages.domainFactRoots()[fact.OwningRoot]
	if !exists || !domains[fact.OwnerDomainID] || (root.Role != "domain" && root.Role != "mixed") || (root.Role == "domain" && len(root.OwnerDomainIDs) != 1) || (root.Role == "mixed" && len(root.OwnerDomainIDs) < 2) {
		return domainShapeSetupError("fact has no valid current owning root: " + name)
	}
	owner := false
	for _, domain := range root.OwnerDomainIDs {
		owner = owner || domain == fact.OwnerDomainID
	}
	if !owner || (fact.Standing != "law" && fact.Standing != "observed") || (fact.Standing == "observed" && fact.GoverningLawIDs != nil) || (fact.Standing == "law" && len(fact.GoverningLawIDs) == 0) {
		return domainShapeSetupError("fact owner or standing is invalid: " + name)
	}
	variants := map[DomainFactKind]bool{DomainFactInvariant: fact.Invariant != nil, DomainFactDataObject: fact.DataObject != nil, DomainFactStateAuthority: fact.StateAuthority != nil, DomainFactConfigCoupling: fact.ConfigCoupling != nil}
	if !variants[fact.Kind] {
		return domainShapeSetupError("fact requires the payload named by kind: " + name)
	}
	for kind, present := range variants {
		if present && kind != fact.Kind {
			return domainShapeSetupError("fact carries an unrelated payload: " + name)
		}
	}
	return nil
}

func openDomainShapeSetupRoot(name string) (*os.Root, error) {
	// Reject symlinks even above RepoPath. os.Root also confines all subsequent
	// operations if an external process races an ancestor into a symlink.
	for current := name; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() {
			return nil, domainShapeSetupError("repository ancestors must be real directories: " + current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return os.OpenRoot(name)
}

func (p *DomainShapeSetupPlan) observeDirectory(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil || !info.IsDir() {
			return domainShapeSetupError("package root must be an existing real directory: " + prefix)
		}
		p.observed[prefix] = domainShapeSetupEntry{Mode: info.Mode()}
	}
	return nil
}

func (p *DomainShapeSetupPlan) readTree(root *os.Root) error {
	paths := []string{projectToolingManifestPath}
	if _, err := root.Lstat(knowledgeShardRoot); err == nil {
		if err := fs.WalkDir(root.FS(), knowledgeShardRoot, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
				return domainShapeSetupError("knowledge tree contains a symlink or special file: " + name)
			}
			paths = append(paths, name)
			if len(paths) > 4096 {
				return domainShapeSetupError("knowledge tree exceeds 4096 entries")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return p.readSetupEntries(root, paths)
}

func (p *DomainShapeSetupPlan) readSetupEntries(root *os.Root, paths []string) error {
	for _, name := range paths {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		entry := domainShapeSetupEntry{Mode: info.Mode()}
		if !info.IsDir() {
			limit := maxKnowledgeRecord
			switch name {
			case projectToolingManifestPath:
				limit = 64 * 1024
			case domainShapeRatchetPath:
				limit = DomainShapeMaxBytes
			}
			if !info.Mode().IsRegular() || info.Size() > int64(limit) {
				return domainShapeSetupError(fmt.Sprintf("setup input must be a regular file within %d bytes: %s", limit, name))
			}
			file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			entry.Data, err = io.ReadAll(io.LimitReader(file, int64(limit+1)))
			closeErr := file.Close()
			if err != nil || closeErr != nil || len(entry.Data) > limit || !utf8.Valid(entry.Data) {
				return domainShapeSetupError("cannot read bounded setup input: " + name)
			}
		}
		p.observed[name] = entry
	}
	return nil
}

func (p *DomainShapeSetupPlan) prepareTooling(root *os.Root, data []byte) error {
	var document map[string]json.RawMessage
	if err := decodeStrictKnowledgeJSON(data, &document); err != nil || document == nil {
		return domainShapeSetupError("tooling must be one JSON object without duplicate keys")
	}
	for key := range document {
		if key != "schema_version" && key != "project" && key != "tools" && key != "domain_shape" {
			return domainShapeSetupError("tooling has an unknown field: " + key)
		}
	}
	var version, project string
	var tools []json.RawMessage
	if json.Unmarshal(document["schema_version"], &version) != nil || version != "1.0" || json.Unmarshal(document["project"], &project) != nil || project != p.request.ProjectKey || json.Unmarshal(document["tools"], &tools) != nil || tools == nil {
		return domainShapeSetupError("tooling requires schema 1.0, the selected Project key and a tools array")
	}
	for _, raw := range tools {
		if err := validateSetupToolMetadata(root, raw); err != nil {
			return err
		}
	}
	if _, exists := document["domain_shape"]; !exists {
		// Insert only the missing declaration. Keep every authored tool byte intact.
		trimmed := bytes.TrimRight(data, " \t\r\n")
		data = append(append(bytes.Clone(trimmed[:len(trimmed)-1]), []byte(",\n  \"domain_shape\": {}\n}")...), data[len(trimmed):]...)
		p.writes[projectToolingManifestPath] = data
	}
	_, err := ParseProjectToolingManifest(data)
	return err
}

// The normal tooling reader owns executable declarations. Setup also checks
// authored metadata so it cannot preserve a malformed schema 1.0 tool as valid.
func validateSetupToolMetadata(root *os.Root, raw []byte) error {
	var tool struct {
		ID             string `json:"id"`
		Purpose        string `json:"purpose"`
		Invocation     string `json:"invocation"`
		Tier           string `json:"tier"`
		CostHint       string `json:"cost_hint,omitempty"`
		ConfigPath     string `json:"config_path,omitempty"`
		AutomationPath string `json:"automation_path,omitempty"`
		Notes          string `json:"notes,omitempty"`
	}
	if err := domainShapeJSON(raw, &tool); err != nil {
		return domainShapeSetupError("tool metadata is malformed: " + err.Error())
	}
	text := func(value string, min, max int) bool {
		return utf8.RuneCountInString(value) >= min && utf8.RuneCountInString(value) <= max && strings.TrimSpace(value) != ""
	}
	if !text(tool.Purpose, 4, 256) || (tool.Tier != "fast" && tool.Tier != "standard" && tool.Tier != "slow") {
		return domainShapeSetupError("tool metadata requires bounded purpose and fast, standard or slow tier")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, exists := fields["notes"]; exists && !text(tool.Notes, 4, 512) {
		return domainShapeSetupError("tool notes must contain 4..512 characters")
	}
	if _, exists := fields["cost_hint"]; exists {
		if !text(tool.CostHint, 4, 128) || strings.ContainsFunc(tool.CostHint, func(r rune) bool { return r < 32 || r == 127 }) {
			return domainShapeSetupError("tool cost_hint must contain 4..128 characters without control characters")
		}
	}
	return validateSetupToolPaths(root, fields, map[string]string{"config_path": tool.ConfigPath, "automation_path": tool.AutomationPath})
}

func validateSetupToolPaths(root *os.Root, fields map[string]json.RawMessage, paths map[string]string) error {
	for key, name := range paths {
		if _, exists := fields[key]; !exists {
			continue
		}
		if len(name) < 3 || len(name) > 512 || filepath.Clean(name) != name || filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsFunc(name, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-", r))
		}) {
			return domainShapeSetupError("tool path is not a contained repository path: " + key)
		}
		for parent := filepath.ToSlash(filepath.Dir(name)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			info, err := root.Lstat(parent)
			if err != nil || !info.IsDir() {
				return domainShapeSetupError("tool path ancestor is not a real directory: " + parent)
			}
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return domainShapeSetupError("tool path is not an existing regular file: " + name)
		}
	}
	return nil
}

func validateSetupRatchet(data []byte, packages knowledgePackageMapDocument) error {
	var ratchet DomainShapeRatchet
	if err := domainShapeJSON(data, &ratchet); err != nil || ratchet.SchemaVersion != DomainShapeProtocolVersion || ratchet.MixedRoots == nil || ratchet.Imports == nil || len(ratchet.MixedRoots) > DomainShapeMaxItems || len(ratchet.Imports) > DomainShapeMaxItems {
		return domainShapeSetupError("ratchet requires bounded protocol 1.0 mixed_roots and imports arrays")
	}
	roots := map[string]string{}
	for _, entry := range packages.Packages {
		role := entry.Role
		if packages.SchemaVersion == packageMapSchemaVersion {
			role = "mixed"
		}
		roots[entry.Path] = role
	}
	seenRoots := map[string]bool{}
	for _, name := range ratchet.MixedRoots {
		if roots[name] != "mixed" || seenRoots[name] {
			return domainShapeSetupError("ratchet names an unknown, non-mixed or duplicate root: " + name)
		}
		seenRoots[name] = true
	}
	seenImports := map[DomainShapeImport]bool{}
	for _, edge := range ratchet.Imports {
		if roots[edge.SourceRoot] == "" || roots[edge.TargetRoot] == "" || seenImports[edge] {
			return domainShapeSetupError("ratchet names unknown or duplicate import roots")
		}
		seenImports[edge] = true
	}
	return nil
}

// Apply materializes the prepared proposal with exclusive creation, without Git
// or DB effects. A non-nil error may accompany Created/Updated. Individual writes
// publish atomically, but the complete set is not crash-atomic or fsync-durable;
// setup does not claim transactionality.
func (p *DomainShapeSetupPlan) Apply() (DomainShapeSetupResult, error) {
	result := DomainShapeSetupResult{Created: []string{}, Updated: []string{}}
	if p == nil {
		return result, domainShapeSetupError("setup plan is nil")
	}
	fresh, err := PrepareDomainShapeSetup(p.request)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(p.observed, fresh.observed) || !reflect.DeepEqual(p.writes, fresh.writes) || !reflect.DeepEqual(p.dirs, fresh.dirs) {
		return result, domainShapeSetupError("setup inputs changed after preparation")
	}
	root, err := openDomainShapeSetupRoot(p.request.RepoPath)
	if err != nil {
		return result, err
	}
	defer root.Close()
	for _, dir := range p.dirs {
		if err := root.Mkdir(dir, 0755); err != nil {
			return result, fmt.Errorf("setup stopped at %s; earlier reported paths may remain: %w", dir, err)
		}
		result.Created = append(result.Created, dir)
	}
	names := make([]string, 0, len(p.writes))
	for name := range p.writes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := fs.FileMode(0644)
		if original, exists := p.observed[name]; exists {
			mode = original.Mode.Perm()
		}
		_, replacing := p.observed[name]
		published, err := publishDomainShapeFile(root, name, p.writes[name], mode, replacing)
		if published {
			if replacing {
				result.Updated = append(result.Updated, name)
			} else {
				result.Created = append(result.Created, name)
			}
		}
		if err != nil {
			return result, fmt.Errorf("setup stopped at %s; earlier reported paths may remain: %w", name, err)
		}
	}
	return result, nil
}

func publishDomainShapeFile(root *os.Root, name string, data []byte, mode fs.FileMode, replacing bool) (published bool, err error) {
	stage := filepath.ToSlash(filepath.Join(filepath.Dir(name), ".domain-shape-"+rand.Text()))
	file, err := root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return false, err
	}
	defer func() {
		if cleanup := root.Remove(stage); cleanup != nil && !os.IsNotExist(cleanup) {
			err = errors.Join(err, fmt.Errorf("cannot remove setup staging file %s: %w", stage, cleanup))
		}
	}()
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return false, err
	}
	if replacing {
		// Rename replaces the leaf, never follows it. Caller serialization is
		// still required: a concurrent user's edit must not be overwritten.
		err = root.Rename(stage, name)
	} else {
		// Link publishes the whole file but refuses a concurrently created leaf.
		err = root.Link(stage, name)
	}
	return err == nil, err
}
