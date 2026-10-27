package store

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// DomainShapeResultMaxBytes bounds the whole marshalled result, not each fact.
const DomainShapeResultMaxBytes = 32768

// DomainShapeSelector selects exactly one path, current Domain, or exact typed
// subject. It never performs fuzzy matching or expands to an overview.
type DomainShapeSelector struct {
	Path     string `json:"path,omitempty"`
	DomainID string `json:"domain_id,omitempty"`
	Subject  string `json:"subject,omitempty"`
}

type DomainShapeFact struct {
	Fact        DomainFact `json:"fact"`
	Path        string     `json:"path"`
	ContentHash string     `json:"content_hash"`
}

// DomainShapeResult pins both the selection and its sources. Fact paths/hashes
// name raw shards at KnowledgeCommit in the named home; GoverningLaw carries
// each law's own committed identity. SnapshotContentHash is not a result hash.
// A required-reading consumer can pin the exact json.Marshal(result) bytes.
type DomainShapeResult struct {
	ProductID             string              `json:"product_id"`
	HomeProjectID         string              `json:"home_project_id"`
	HomeLocatorID         string              `json:"home_locator_id"`
	Selector              DomainShapeSelector `json:"selector"`
	KnowledgeCommit       string              `json:"knowledge_commit"`
	SnapshotContentHash   string              `json:"snapshot_content_hash"`
	RegistryContentHash   string              `json:"registry_content_hash"`
	PackageMapContentHash string              `json:"package_map_content_hash"`
	ToolingCommit         string              `json:"tooling_commit"`
	ToolingContentHash    string              `json:"tooling_content_hash"`
	Roots                 []DomainShapeRoot   `json:"roots"`
	DomainIDs             []string            `json:"domain_ids"`
	Facts                 []DomainShapeFact   `json:"facts"`
	GoverningLaw          []DomainLawRecord   `json:"governing_law"`
	ReferenceVerification string              `json:"reference_verification"`
	ExtractorVerification string              `json:"extractor_verification"`
}

func validateDomainShapeSelector(selector DomainShapeSelector) error {
	count := 0
	for _, value := range []string{selector.Path, selector.DomainID, selector.Subject} {
		if value != "" {
			count++
		}
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return domainShapeReadInvalid("selector must be valid single-line UTF-8")
		}
	}
	if count != 1 {
		return domainShapeReadInvalid("supply exactly one path, Domain or subject")
	}
	if selector.Path != "" && (!validFootprintPath(selector.Path) || strings.Contains(selector.Path, "\\")) {
		return domainShapeReadInvalid("path must be a bounded normalized relative repository path")
	}
	if selector.DomainID != "" && !validManifestID(selector.DomainID) {
		return domainShapeReadInvalid("Domain must be a bounded registry identity")
	}
	if selector.Subject != "" && !domainFactText(selector.Subject, DomainFactMaxTextBytes) {
		return domainShapeReadInvalid("subject must be nonblank and at most 1024 bytes")
	}
	return nil
}

func domainShapeReadInvalid(detail string) error {
	return newFailure(KindInvalidInput, "domain_shape_read", detail, false, "supply one bounded path, current Domain ID or exact typed subject")
}

// QueryDomainShape refreshes the designated home before opening the reading
// transaction. It never runs Product checks or grants authorization to a caller.
// Agent scope admission remains the caller's responsibility.
func (s *Store) QueryDomainShape(ctx context.Context, product string, selector DomainShapeSelector) (DomainShapeResult, error) {
	if s == nil || s.db == nil {
		return DomainShapeResult{}, newFailure(KindUnavailable, "domain_shape_read", "store is not open", false, "open a store before reading Domain Shape")
	}
	if product == "" {
		return DomainShapeResult{}, domainShapeReadInvalid("Domain Shape requires an explicit Product")
	}
	if err := validateDomainShapeSelector(selector); err != nil {
		return DomainShapeResult{}, err
	}
	home, err := resolveKnowledgeQueryHome(ctx, s.db, product, "", KnowledgeHome{}, "domain_shape_read")
	if err != nil {
		return DomainShapeResult{}, err
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		return DomainShapeResult{}, err
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return DomainShapeResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := selectedDomainShapeTx(ctx, tx, home, product, selector)
	if err != nil {
		return DomainShapeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DomainShapeResult{}, err
	}
	return result, nil
}

// selectedDomainShapeTx is also the footprint selection core. The caller owns
// freshness admission; no Store method or Git probe runs inside this scope.
func selectedDomainShapeTx(ctx context.Context, q queryer, home KnowledgeHome, product string, selector DomainShapeSelector) (DomainShapeResult, error) {
	if err := validateDomainShapeSelector(selector); err != nil {
		return DomainShapeResult{}, err
	}
	snapshot, err := readDomainShapeSnapshotTx(ctx, q, home, product)
	if err != nil {
		return DomainShapeResult{}, err
	}
	registry, err := readDomainRegistry(ctx, q, product)
	if err != nil {
		return DomainShapeResult{}, err
	}
	if registry.ContentHash != snapshot.RegistryContentHash || registry.ScannedCommit != snapshot.KnowledgeCommit {
		return DomainShapeResult{}, newFailure(KindStaleContext, "domain_shape_read", "registry and shape revisions differ", true, "refresh the selected Product knowledge index")
	}
	if selector.DomainID != "" {
		if _, err := domainExistsCurrent(ctx, q, product, selector.DomainID); err != nil {
			return DomainShapeResult{}, err
		}
	}
	result := selectDomainShape(snapshot, home, product, selector)
	result.GoverningLaw, err = domainShapeGoverningLawTx(ctx, q, home, product, result)
	if err != nil {
		return DomainShapeResult{}, err
	}
	return boundDomainShapeResult(result)
}

func selectDomainShape(snapshot DomainShapeSnapshot, home KnowledgeHome, product string, selector DomainShapeSelector) DomainShapeResult {
	result := DomainShapeResult{ProductID: product, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID,
		Selector: selector, KnowledgeCommit: snapshot.KnowledgeCommit, SnapshotContentHash: snapshot.ContentHash,
		RegistryContentHash: snapshot.RegistryContentHash, PackageMapContentHash: snapshot.MapContentHash,
		ToolingCommit: snapshot.ToolingCommit, ToolingContentHash: snapshot.ToolingContentHash,
		Roots: []DomainShapeRoot{}, DomainIDs: []string{}, Facts: []DomainShapeFact{}, GoverningLaw: []DomainLawRecord{},
		ReferenceVerification: snapshot.ReferenceVerification, ExtractorVerification: snapshot.ExtractorVerification}
	rootPaths, domains := map[string]bool{}, map[string]bool{}
	owner := domainShapeOwningRoot(snapshot.Roots, selector.Path)
	if owner != "" {
		rootPaths[owner] = true
	}
	if selector.DomainID != "" {
		domains[selector.DomainID] = true
	}
	for _, fact := range snapshot.Facts {
		matches := selector.Path != "" && owner != "" && fact.OwningRoot == owner ||
			selector.DomainID != "" && fact.OwnerDomainID == selector.DomainID ||
			selector.Subject != "" && domainShapeSubjectMatches(fact, selector.Subject)
		if matches {
			result.Facts = append(result.Facts, DomainShapeFact{Fact: fact, Path: knowledgeFactTreeDir + fact.ID + ".json", ContentHash: snapshot.FactContentHashes[fact.ID]})
			rootPaths[fact.OwningRoot], domains[fact.OwnerDomainID] = true, true
		}
	}
	for _, root := range snapshot.Roots {
		if rootPaths[root.Path] || selector.DomainID != "" && slices.Contains(domainShapeRootDomains(root), selector.DomainID) {
			result.Roots = append(result.Roots, root)
			for _, domain := range domainShapeRootDomains(root) {
				domains[domain] = true
			}
		}
	}
	sort.Slice(result.Roots, func(i, j int) bool { return result.Roots[i].Path < result.Roots[j].Path })
	sort.Slice(result.Facts, func(i, j int) bool { return result.Facts[i].Fact.ID < result.Facts[j].Fact.ID })
	result.DomainIDs = sortedKeys(domains)
	return result
}

func domainShapeRootDomains(root DomainShapeRoot) []string {
	if root.OwnerDomainID != "" {
		return []string{root.OwnerDomainID}
	}
	return sliceOrEmpty(root.DomainIDs)
}

func domainShapeOwningRoot(roots []DomainShapeRoot, filePath string) string {
	entries := map[string]bool{}
	for _, root := range roots {
		entries[root.Path] = true
	}
	for dir := filePath; dir != "" && dir != "." && dir != "/"; dir = path.Dir(dir) {
		if entries[dir] {
			return dir
		}
	}
	return ""
}

func domainShapeSubjectMatches(fact DomainFact, subject string) bool {
	if fact.ID == subject {
		return true
	}
	switch fact.Kind {
	case DomainFactDataObject:
		return fact.DataObject.Name == subject || fact.DataObject.ResourceID == subject
	case DomainFactConfigCoupling:
		return fact.ConfigCoupling.Setting == subject
	case DomainFactStateAuthority:
		if fact.StateAuthority.State == subject || fact.StateAuthority.AuthoritativeResourceID == subject {
			return true
		}
		for _, copy := range fact.StateAuthority.DerivedCopies {
			if copy.ResourceID == subject {
				return true
			}
		}
	}
	return false
}

func domainShapeGoverningLawTx(ctx context.Context, q queryer, home KnowledgeHome, product string, selection DomainShapeResult) ([]DomainLawRecord, error) {
	explicit := map[string]bool{}
	for _, item := range selection.Facts {
		for _, id := range item.Fact.GoverningLawIDs {
			explicit[id] = true
		}
	}
	domains, _ := json.Marshal(selection.DomainIDs)
	laws, _ := json.Marshal(sortedKeys(explicit))
	rows, err := q.QueryContext(ctx, `WITH governing(domain_id) AS (
		SELECT domain_id FROM domains WHERE product_id=? AND domain_id IN (SELECT value FROM json_each(?))
	) SELECT DISTINCT s.law_id,s.kind,s.title,s.path,s.content_hash,s.scanned_commit_oid
	FROM law_subjects s JOIN law_domain_homes h USING(home_project_id,home_locator_id,law_id)
	WHERE s.home_project_id=? AND s.home_locator_id=? AND h.product_id=? AND s.status='accepted'
	AND (h.domain_id IN (SELECT domain_id FROM governing) OR s.law_id IN (SELECT value FROM json_each(?))
	OR EXISTS (SELECT 1 FROM law_domain_applicability a WHERE a.home_project_id=s.home_project_id AND a.home_locator_id=s.home_locator_id AND a.law_id=s.law_id AND a.product_id=? AND a.domain_id IN (SELECT domain_id FROM governing)))
	ORDER BY s.law_id`, product, string(domains), home.HomeProjectID, home.HomeLocatorID, product, string(laws), product)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DomainLawRecord{}
	for rows.Next() {
		var law DomainLawRecord
		if err := rows.Scan(&law.LawID, &law.Kind, &law.Title, &law.Path, &law.ContentHash, &law.ScannedCommit); err != nil {
			return nil, err
		}
		result = append(result, law)
	}
	return result, rows.Err()
}

func boundDomainShapeResult(result DomainShapeResult) (DomainShapeResult, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return DomainShapeResult{}, err
	}
	if len(raw) > DomainShapeResultMaxBytes {
		return DomainShapeResult{}, newFailure(KindLimitExceeded, "domain_shape_read", "selected Domain Shape result exceeds 32768 bytes", false, "select a narrower path or exact subject; results are never truncated")
	}
	return result, nil
}
