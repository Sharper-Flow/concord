package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// DomainShapeSnapshot is a derived, single-home revision, not an authority or
// execution receipt. Hashes name the exact committed fact bytes and canonical
// root/registry inputs. Consumers must bound their selected response separately.
type DomainShapeSnapshot struct {
	Roots                 []DomainShapeRoot `json:"roots"`
	Facts                 []DomainFact      `json:"facts"`
	KnowledgeCommit       string            `json:"knowledge_commit"`
	ContentHash           string            `json:"content_hash"`
	RegistryContentHash   string            `json:"registry_content_hash"`
	MapContentHash        string            `json:"map_content_hash"`
	FactContentHashes     map[string]string `json:"fact_content_hashes"`
	ToolingCommit         string            `json:"tooling_commit"`
	ToolingContentHash    string            `json:"tooling_content_hash"`
	RequiredChecks        []string          `json:"required_checks"`
	ReferenceVerification string            `json:"reference_verification"`
	ExtractorVerification string            `json:"extractor_verification"`
}

type domainShapeProjection struct {
	Snapshot         DomainShapeSnapshot
	RawFacts         map[string][]byte
	Registry         KnowledgeDomainRegistry
	FactRoots        map[string]DomainFactRoot
	RootTreeIdentity []byte
}

func domainShapeProjectionFailure(detail string) error {
	return newFailure(KindInvalidNoteProof, "domain_shape_projection", detail, false, "repair the committed Domain Shape inputs and their Product-scoped references")
}

func domainShapeBytesHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newDomainShapeProjection(commit string, manifest KnowledgeManifest, architecture knowledgeArchitecture) *domainShapeProjection {
	shape := &domainShapeProjection{Registry: manifest.DomainRegistry}
	shape.Snapshot = DomainShapeSnapshot{Roots: []DomainShapeRoot{}, Facts: []DomainFact{}, KnowledgeCommit: commit,
		RegistryContentHash: domainRegistryContentHash(manifest.DomainRegistry), MapContentHash: architecture.MapHash,
		FactContentHashes: map[string]string{}, RequiredChecks: []string{}, ReferenceVerification: "unverified", ExtractorVerification: "unverified"}
	for _, entry := range architecture.Packages {
		role := entry.Role
		if role == "" {
			role = "mixed"
		}
		shape.Snapshot.Roots = append(shape.Snapshot.Roots, DomainShapeRoot{Path: entry.Path, Role: role, OwnerDomainID: entry.OwnerDomainID, DomainIDs: entry.DomainIDs})
	}
	return shape
}

// prepareDomainShapeProjection does committed Git reads only. It deliberately
// never calls the Product-declared check runner or resolves code symbols.
func prepareDomainShapeProjection(ctx context.Context, home KnowledgeHome, commit string, shards knowledgeShards, manifest KnowledgeManifest, architecture knowledgeArchitecture, designated bool) (*domainShapeProjection, error) {
	if !designated {
		if len(shards.facts) != 0 {
			return nil, domainShapeProjectionFailure("facts require the Product's designated knowledge home; a registered source cannot carry facts")
		}
		return nil, nil
	}
	shape := newDomainShapeProjection(commit, manifest, architecture)
	shape.RawFacts = shards.facts
	version := packageMapSchemaVersion
	if shards.packageMapPresent {
		document, err := parseKnowledgePackageMap(shards.packageMap, manifest.DomainRegistry)
		if err != nil {
			return nil, err
		}
		version = document.SchemaVersion
		shape.FactRoots = document.domainFactRoots()
	}
	// Historical maps without facts remain readable without retroactively
	// claiming that they proved committed directory existence.
	if version == packageMapShapeVersion || len(shards.facts) != 0 {
		var err error
		shape.RootTreeIdentity, err = domainShapeRootTreeIdentity(ctx, home, commit, architecture.Packages)
		if err != nil {
			return nil, err
		}
	}
	if len(shards.facts) > DomainShapeMaxItems {
		return nil, domainShapeProjectionFailure("fact shard count exceeds 512")
	}
	if len(shards.facts) != 0 {
		toolingCommit, toolingHash, checks, err := domainShapeToolingAt(ctx, home, commit)
		if err != nil {
			return nil, err
		}
		shape.Snapshot.ToolingCommit, shape.Snapshot.ToolingContentHash, shape.Snapshot.RequiredChecks = toolingCommit, toolingHash, checks
	}
	return shape, nil
}

func domainShapeRootTreeIdentity(ctx context.Context, home KnowledgeHome, commit string, roots []knowledgePackageEntry) ([]byte, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	args := []string{"--literal-pathspecs", "ls-tree", "-t", "-z", commit, "--"}
	for _, root := range roots {
		if !validPackageRootPath(root.Path) {
			return nil, domainShapeProjectionFailure("invalid root path: " + root.Path)
		}
		args = append(args, root.Path)
	}
	raw, err := runGit(ctx, home.RepoPath, args...)
	if err != nil {
		return nil, err
	}
	entries, err := parseTreeEntries(raw)
	if err != nil {
		return nil, err
	}
	declared := map[string]treeEntry{}
	for _, entry := range entries {
		declared[entry.path] = entry
	}
	identities := make([]string, 0, len(roots))
	for _, root := range roots {
		entry := declared[root.Path]
		if entry.kind != "tree" || entry.mode != "040000" {
			return nil, domainShapeProjectionFailure("root is not a committed directory: " + root.Path)
		}
		identities = append(identities, root.Path+"="+entry.mode+":"+entry.oid)
	}
	sort.Strings(identities)
	return []byte(strings.Join(identities, "\n")), nil
}

// Required check declarations come from the pinned default revision, never the
// candidate's working tree or an anchor copied from a fact.
func domainShapeDefaultTooling(ctx context.Context, home KnowledgeHome) (string, string, []string, error) {
	ref, err := worktreeAuditDefaultRef(ctx, runGitRunner{}, home.RepoPath, "")
	if err != nil {
		return "", "", nil, err
	}
	out, err := runGit(ctx, home.RepoPath, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", "", nil, err
	}
	commit := strings.TrimSpace(string(out))
	return domainShapeToolingAt(ctx, home, commit)
}

// domainShapeToolingAt reads declarations, not execution evidence, from the
// same immutable commit as its caller's other inputs.
func domainShapeToolingAt(ctx context.Context, home KnowledgeHome, commit string) (string, string, []string, error) {
	listing, err := runGit(ctx, home.RepoPath, "ls-tree", "-z", commit, "--", projectToolingManifestPath)
	if err != nil {
		return "", "", nil, err
	}
	entries, err := parseTreeEntries(listing)
	if err != nil {
		return "", "", nil, err
	}
	if len(entries) == 0 {
		return commit, "", []string{}, nil
	}
	entry := entries[0]
	if entry.kind != "blob" || entry.mode != "100644" {
		return "", "", nil, domainShapeProjectionFailure("tooling declaration is not a regular committed file")
	}
	raw, err := runGit(ctx, home.RepoPath, "cat-file", "blob", commit+":"+projectToolingManifestPath)
	if err != nil {
		return "", "", nil, err
	}
	tooling, err := ParseProjectToolingManifest(raw)
	if err != nil {
		return "", "", nil, err
	}
	checks := []string{}
	if tooling.DomainShape != nil {
		checks = append(checks, tooling.DomainShape.RequiredChecks...)
	}
	sort.Strings(checks)
	return commit, domainShapeBytesHash(raw), checks, nil
}

func domainShapeReferencesTx(ctx context.Context, q queryer, product string, shape *domainShapeProjection, manifest KnowledgeManifest) (DomainFactReferences, error) {
	refs := DomainFactReferences{Registry: shape.Registry, Roots: shape.FactRoots, ValidationMode: DomainFactUnverifiedReferences,
		AcceptedLawIDs: map[string]bool{}, Facts: map[string]DomainFactKind{}, ProjectIDs: map[string]bool{}, RequiredCheckIDs: map[string]bool{}}
	for _, record := range manifest.Records {
		if manifestLawBearingKinds[record.Kind] && record.Status == "accepted" {
			refs.AcceptedLawIDs[record.ID] = true
		}
	}
	for _, check := range shape.Snapshot.RequiredChecks {
		refs.RequiredCheckIDs[check] = true
	}
	if err := readDomainShapeModelReferencesTx(ctx, q, product, &refs); err != nil {
		return refs, err
	}
	return refs, nil
}

func readDomainShapeModelReferencesTx(ctx context.Context, q queryer, product string, refs *DomainFactReferences) error {
	rows, err := q.QueryContext(ctx, `SELECT project_id FROM product_projects WHERE product_id=?`, product)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		refs.ProjectIDs[id] = true
	}
	err = rows.Err()
	_ = rows.Close()
	return err
}

// Project membership can change without a Git edit, so the reading
// transaction rechecks the model-owned Project joins the snapshot facts name.
// Resource sources are bounded fact-local identifier strings since the
// inventory retired, and no registration stands behind them.
func validateDomainShapeSnapshotModelTx(ctx context.Context, q queryer, product string, snapshot DomainShapeSnapshot) error {
	if len(snapshot.Facts) == 0 {
		return nil
	}
	refs := DomainFactReferences{ProjectIDs: map[string]bool{}}
	if err := readDomainShapeModelReferencesTx(ctx, q, product, &refs); err != nil {
		return err
	}
	for _, fact := range snapshot.Facts {
		valid := true
		switch fact.Kind {
		case DomainFactStateAuthority:
			for _, copy := range fact.StateAuthority.DerivedCopies {
				valid = valid && refs.ProjectIDs[copy.RebuildRoute.ProjectID]
			}
		case DomainFactConfigCoupling:
			valid = refs.ProjectIDs[fact.ConfigCoupling.ReadBy.ProjectID]
		}
		if !valid {
			return domainShapeProjectionFailure("fact no longer resolves to current member Projects: " + fact.ID)
		}
	}
	return nil
}

func validateDomainShapeProjectionTx(ctx context.Context, q queryer, product string, shape *domainShapeProjection, manifest KnowledgeManifest) error {
	if shape == nil {
		return nil
	}
	shape.Snapshot.Facts = []DomainFact{}
	shape.Snapshot.FactContentHashes = map[string]string{}
	shape.Snapshot.ContentHash = ""
	refs, err := domainShapeReferencesTx(ctx, q, product, shape, manifest)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(shape.RawFacts))
	for name, raw := range shape.RawFacts {
		var fact DomainFact
		if len(raw) > DomainFactMaxBytes || decodeStrictKnowledgeJSON(raw, &fact) != nil || name != fact.ID+".json" {
			return domainShapeProjectionFailure("invalid fact shard: " + name)
		}
		refs.Facts[fact.ID] = fact.Kind
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := shape.RawFacts[name]
		fact, err := ParseDomainFact(name, raw, refs)
		if err != nil {
			return err
		}
		shape.Snapshot.Facts = append(shape.Snapshot.Facts, fact)
		shape.Snapshot.FactContentHashes[fact.ID] = domainShapeBytesHash(raw)
	}
	// Hash source identities, not a claimed check result. Tooling declarations
	// share the facts' knowledge commit, independently of explicit check authority.
	raw, err := json.Marshal(shape.Snapshot)
	if err != nil {
		return err
	}
	shape.Snapshot.ContentHash = domainShapeBytesHash(raw)
	return nil
}

func insertDomainShapeProjection(ctx context.Context, tx *sql.Tx, home KnowledgeHome, product string, shape *domainShapeProjection) error {
	if shape == nil {
		return nil
	}
	s := shape.Snapshot
	checks, err := json.Marshal(s.RequiredChecks)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_domain_shapes(home_project_id,home_locator_id,scanned_commit_oid,product_id,content_hash,registry_content_hash,map_content_hash,tooling_commit_oid,tooling_content_hash,required_checks) VALUES(?,?,?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, s.KnowledgeCommit, product, s.ContentHash, s.RegistryContentHash, s.MapContentHash, s.ToolingCommit, s.ToolingContentHash, string(checks)); err != nil {
		return err
	}
	for _, root := range s.Roots {
		raw, err := json.Marshal(root)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_domain_roots(home_project_id,home_locator_id,scanned_commit_oid,root_path,root_json) VALUES(?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, s.KnowledgeCommit, root.Path, string(raw)); err != nil {
			return err
		}
	}
	for _, fact := range s.Facts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_domain_facts(home_project_id,home_locator_id,scanned_commit_oid,fact_id,fact_json,content_hash) VALUES(?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, s.KnowledgeCommit, fact.ID, string(shape.RawFacts[fact.ID+".json"]), s.FactContentHashes[fact.ID]); err != nil {
			return err
		}
	}
	return nil
}

// ReadDomainShapeSnapshot reads an already refreshed index atomically. It does
// not refresh, run Product checks, or use a Store method from within its tx.
func (s *Store) ReadDomainShapeSnapshot(ctx context.Context, home KnowledgeHome, product string) (DomainShapeSnapshot, error) {
	if s == nil || s.db == nil {
		return DomainShapeSnapshot{}, newFailure(KindUnavailable, "domain_shape_snapshot", "store is not open", false, "open a store before reading the Domain Shape projection")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return DomainShapeSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := readDomainShapeSnapshotTx(ctx, tx, home, product)
	if err != nil {
		return DomainShapeSnapshot{}, err
	}
	return result, tx.Commit()
}

// readDomainShapeSnapshotTx composes under the caller's query/transaction scope.
// The caller owns freshness admission. Joining the watermark refuses mixed or
// incomplete generations rather than silently returning partial roots/facts.
func readDomainShapeSnapshotTx(ctx context.Context, q queryer, home KnowledgeHome, product string) (DomainShapeSnapshot, error) {
	result := DomainShapeSnapshot{Roots: []DomainShapeRoot{}, Facts: []DomainFact{}, FactContentHashes: map[string]string{}, RequiredChecks: []string{}, ReferenceVerification: "unverified", ExtractorVerification: "unverified"}
	selected, designated, err := resolveKnowledgeSourceRole(ctx, q, home)
	if err != nil {
		return DomainShapeSnapshot{}, err
	}
	if product == "" || selected != product || !designated {
		return DomainShapeSnapshot{}, domainShapeProjectionFailure("snapshot requires the selected Product's designated home")
	}
	var checks string
	err = q.QueryRowContext(ctx, `SELECT s.scanned_commit_oid,s.content_hash,s.registry_content_hash,s.map_content_hash,s.tooling_commit_oid,s.tooling_content_hash,s.required_checks FROM knowledge_domain_shapes s JOIN knowledge_index_watermark w ON w.home_project_id=s.home_project_id AND w.home_locator_id=s.home_locator_id AND w.scanned_commit_oid=s.scanned_commit_oid WHERE s.home_project_id=? AND s.home_locator_id=? AND s.product_id=? AND w.head_ref=? AND w.complete=1 AND w.projection_version=?`, home.HomeProjectID, home.HomeLocatorID, product, home.HeadRef, knowledgeProjectionVersion).Scan(&result.KnowledgeCommit, &result.ContentHash, &result.RegistryContentHash, &result.MapContentHash, &result.ToolingCommit, &result.ToolingContentHash, &checks)
	if err == sql.ErrNoRows {
		return DomainShapeSnapshot{}, newFailure(KindStaleContext, "domain_shape_snapshot", "no complete current-version Domain Shape projection", true, "refresh the selected Product knowledge index before reading")
	}
	if err != nil {
		return DomainShapeSnapshot{}, err
	}
	if err := json.Unmarshal([]byte(checks), &result.RequiredChecks); err != nil {
		return DomainShapeSnapshot{}, err
	}
	for _, table := range []string{"knowledge_domain_roots", "knowledge_domain_facts"} {
		columns, order := "root_json,''", "root_path"
		if table == "knowledge_domain_facts" {
			columns, order = "fact_json,content_hash", "fact_id"
		}
		rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM "+table+" WHERE home_project_id=? AND home_locator_id=? AND scanned_commit_oid=? ORDER BY "+order, home.HomeProjectID, home.HomeLocatorID, result.KnowledgeCommit) //nolint:gosec // identifiers come from the closed literals above.
		if err != nil {
			return DomainShapeSnapshot{}, err
		}
		for rows.Next() {
			var raw, hash string
			if err := rows.Scan(&raw, &hash); err != nil {
				_ = rows.Close()
				return DomainShapeSnapshot{}, err
			}
			if table == "knowledge_domain_roots" {
				var root DomainShapeRoot
				err = json.Unmarshal([]byte(raw), &root)
				result.Roots = append(result.Roots, root)
			} else {
				var fact DomainFact
				err = json.Unmarshal([]byte(raw), &fact)
				result.Facts = append(result.Facts, fact)
				result.FactContentHashes[fact.ID] = hash
			}
			if err != nil {
				_ = rows.Close()
				return DomainShapeSnapshot{}, err
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return DomainShapeSnapshot{}, err
		}
	}
	if err := validateDomainShapeSnapshotModelTx(ctx, q, product, result); err != nil {
		return DomainShapeSnapshot{}, err
	}
	return result, nil
}

// Shape references introduce dependencies outside the shard tree: committed
// root directories and the same-commit tooling declaration. Keep their identity
// in freshness without changing historical-map digest semantics.
func domainShapeKnowledgeDigest(ctx context.Context, home KnowledgeHome, commit, digest string, prepared *domainShapeProjection) (string, error) {
	raw, err := runGit(ctx, home.RepoPath, "ls-tree", "-z", commit, "--", knowledgePackageMapPath, strings.TrimSuffix(knowledgeFactTreeDir, "/"))
	if err != nil {
		return "", err
	}
	entries, err := parseTreeEntries(raw)
	if err != nil {
		return "", err
	}
	var document knowledgePackageMapDocument
	hasFacts := false
	for _, entry := range entries {
		switch entry.path {
		case strings.TrimSuffix(knowledgeFactTreeDir, "/"):
			hasFacts = true
		case knowledgePackageMapPath:
			raw, err := runGit(ctx, home.RepoPath, "cat-file", "blob", commit+":"+knowledgePackageMapPath)
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(raw, &document); err != nil {
				return "", err
			}
		}
	}
	if document.SchemaVersion != packageMapShapeVersion && !hasFacts {
		return digest, nil
	}
	parts := []string{digest}
	var rootIdentity []byte
	if prepared != nil {
		rootIdentity = prepared.RootTreeIdentity
	} else {
		rootIdentity, err = domainShapeRootTreeIdentity(ctx, home, commit, document.Packages)
		if err != nil {
			return "", err
		}
	}
	parts = append(parts, string(rootIdentity))
	if hasFacts {
		var toolCommit, toolHash string
		if prepared != nil {
			toolCommit, toolHash = prepared.Snapshot.ToolingCommit, prepared.Snapshot.ToolingContentHash
		} else {
			toolCommit, toolHash, _, err = domainShapeToolingAt(ctx, home, commit)
			if err != nil {
				return "", err
			}
		}
		parts = append(parts, toolCommit, toolHash)
	}
	return domainShapeBytesHash([]byte(strings.Join(parts, "\n"))), nil
}
