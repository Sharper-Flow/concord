package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

const workContextNavigationCompanion = ".concord/domain-navigation.v1.json"
const workContextNavigationInventory = ".concord/navigation/inventory.json"

type workContextNavigationSource struct {
	projectID string
	repo      string
	commitOID string
}

// Source revisions come from existing authorities: the registry scan, each
// registered source's knowledge scan (or native Git source resolution before
// its first scan), claimed worktree bases, and declared repository readings.
// Project/commit identifies a tree; Project/path/commit identifies a reading.
// No checkout file supplies either a revision or navigation bytes.
func workContextNavigationSources(ctx context.Context, q queryer, workID string, view *WorkContextView) (string, string, string, []workContextNavigationSource, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND wp.role='primary' ORDER BY pp.product_id`, workID)
	if err != nil {
		return "", "", "", nil, err
	}
	var products []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", "", "", nil, err
		}
		products = append(products, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", "", "", nil, err
	}
	if len(products) == 0 {
		return "", "", "", nil, nil
	}
	var productID, rootID, registryHash string
	sources := []workContextNavigationSource{}
	repos := map[string]string{}
	for _, id := range products {
		root, hash, resolved, err := navigationProductSources(ctx, q, id)
		if err != nil {
			return "", "", "", nil, err
		}
		if len(products) == 1 {
			productID, rootID, registryHash = id, root, hash
		}
		for _, source := range resolved {
			sources = append(sources, source)
			repos[source.projectID] = source.repo
		}
	}
	// Cross-approved unadopted work needs no new Product selection. The
	// consuming reader refuses ambiguity only when a source has adopted cards.
	rows, err = q.QueryContext(ctx, `SELECT project_id,path,base_sha FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id`, WorktreeSetID(workID))
	if err != nil {
		return "", "", "", nil, err
	}
	for rows.Next() {
		var source workContextNavigationSource
		if err := rows.Scan(&source.projectID, &source.repo, &source.commitOID); err != nil {
			rows.Close()
			return "", "", "", nil, err
		}
		sources = append(sources, source)
		if repos[source.projectID] == "" {
			repos[source.projectID] = source.repo
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", "", "", nil, err
	}
	for _, reading := range view.RequiredReading {
		if reading.Source.Kind != WorkContextSourceRepositoryFile {
			continue
		}
		project := reading.Source.ProjectID
		repo := repos[project]
		if repo == "" {
			// Match the existing canonical Project locator order, through q,
			// never the s.db-backed ProjectCanonicalPath method.
			homes, err := projectCanonicalHomeCandidates(ctx, q, project)
			if err != nil {
				return "", "", "", nil, err
			}
			if len(homes) == 0 {
				continue // Historical unadopted declarations have no locator proof.
			}
			repo = homes[0].RepoPath
			repos[project] = repo
		}
		sources = append(sources, workContextNavigationSource{project, repo, reading.Source.CommitOID})
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].projectID != sources[j].projectID {
			return sources[i].projectID < sources[j].projectID
		}
		return sources[i].commitOID < sources[j].commitOID
	})
	return productID, rootID, registryHash, sources, nil
}

func navigationProductSources(ctx context.Context, q queryer, productID string) (string, string, []workContextNavigationSource, error) {
	var rootID, registryHash, projectID, locatorID, oid string
	err := q.QueryRowContext(ctx, `SELECT root_domain_id,content_hash,home_project_id,home_locator_id,scanned_commit_oid FROM domain_registries WHERE product_id=?`, productID).Scan(&rootID, &registryHash, &projectID, &locatorID, &oid)
	var sources []workContextNavigationSource
	if err == sql.ErrNoRows {
		homes, homeErr := productKnowledgeHomeCandidates(ctx, q, productID)
		if homeErr != nil {
			return "", "", nil, homeErr
		}
		if len(homes) > 1 {
			return "", "", nil, workContextNavigationFailure(KindAmbiguousScope, "navigation context has more than one knowledge home")
		}
		if len(homes) == 1 {
			home := homes[0]
			oid, _ = resolveKnowledgeHeadCheap(home.RepoPath, home.HeadRef)
			sources = append(sources, workContextNavigationSource{home.HomeProjectID, home.RepoPath, oid})
		}
	} else if err != nil {
		return "", "", nil, err
	} else {
		repo, err := workflowLawHomeRepo(ctx, q, projectID, locatorID)
		if err != nil {
			return "", "", nil, err
		}
		sources = append(sources, workContextNavigationSource{projectID, repo, oid})
	}
	registered, err := productKnowledgeSourceRegistrations(ctx, q, productID)
	if err != nil {
		return "", "", nil, err
	}
	for _, home := range registered {
		var oid string
		err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&oid)
		if err == sql.ErrNoRows {
			oid, _ = resolveKnowledgeHeadCheap(home.RepoPath, home.HeadRef)
		} else if err != nil {
			return "", "", nil, err
		}
		sources = append(sources, workContextNavigationSource{home.HomeProjectID, home.RepoPath, oid})
	}
	return rootID, registryHash, sources, nil
}

// Presence is only a refusal discriminator when Git cannot inspect an old
// pin. It never supplies content or turns an absent pinned companion into an
// adoption. Non-Git legacy fixtures keep their historical reading sets.
func workContextNavigationPresentOnDisk(repo string) bool {
	for _, path := range []string{workContextNavigationCompanion, workContextNavigationInventory} {
		if _, err := os.Lstat(filepath.Join(repo, path)); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func workContextNavigationFailure(kind FailureKind, detail string) error {
	return workContextFailure(kind, "work_context_read", detail, "restore the pinned source objects and regenerate complete Domain navigation at the applicable revision")
}

// Optional absence is distinct from an unreachable tree and a non-regular
// object. The shared Git tree and blob readers supply all actual bytes.
func workContextNavigationBlob(ctx context.Context, source workContextNavigationSource, path string, optional bool) ([]byte, error) {
	if !workContextCommitOID(source.commitOID) {
		return nil, workContextNavigationFailure(KindInvalidNoteProof, "navigation source has no immutable commit OID")
	}
	out, err := runGit(ctx, source.repo, "ls-tree", "-z", source.commitOID, "--", path)
	if err != nil {
		return nil, workContextNavigationFailure(KindGitUnreachable, "cannot inspect pinned navigation source "+source.projectID+" at "+source.commitOID)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return nil, workContextNavigationFailure(KindInvalidNoteProof, "navigation tree entries are malformed")
	}
	if optional && len(entries) == 0 {
		return nil, nil
	}
	entry, err := gitTreeEntry(ctx, source.repo, source.commitOID, path)
	if err != nil {
		return nil, workContextNavigationFailure(KindInvalidNoteProof, "missing pinned navigation object: "+path)
	}
	if entry.kind != "blob" || (entry.mode != "100644" && entry.mode != "100755") {
		return nil, workContextNavigationFailure(KindInvalidNoteProof, "navigation path is not an ordinary pinned blob: "+path)
	}
	content, err := runGit(ctx, source.repo, "cat-file", "blob", source.commitOID+":"+path)
	if err != nil {
		return nil, workContextNavigationFailure(KindInvalidNoteProof, "cannot read pinned navigation blob: "+path)
	}
	return content, nil
}

func workContextNavigationDecode(data []byte, value any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return workContextNavigationFailure(KindInvalidNoteProof, "navigation JSON has duplicate or malformed keys")
	}
	if err := json.Unmarshal(data, value); err != nil {
		return workContextNavigationFailure(KindInvalidNoteProof, "navigation JSON is malformed")
	}
	return nil
}

func workContextNavigationCards(ctx context.Context, source workContextNavigationSource) (map[string]string, *string, error) {
	companion, err := workContextNavigationBlob(ctx, source, workContextNavigationCompanion, true)
	if err != nil {
		// Do not make historical unadopted pins depend on source objects.
		// An adopted checkout can only cause refusal, never substitute bytes.
		var failure *Failure
		unreachablePin := !workContextCommitOID(source.commitOID) || (errors.As(err, &failure) && failure.Kind == KindGitUnreachable)
		if unreachablePin && ctx.Err() == nil {
			if workContextNavigationNonGit(source.repo) {
				head := ""
				return nil, &head, nil
			}
			head, headErr := resolveKnowledgeHeadCheap(source.repo, "HEAD")
			if headErr != nil {
				return nil, nil, workContextNavigationFailure(KindStaleRequiresReview, "historical navigation source HEAD cannot be revalidated without a subprocess")
			}
			out, inspectErr := runGit(ctx, source.repo, "ls-tree", "-z", head, "--", workContextNavigationCompanion, workContextNavigationInventory)
			if inspectErr != nil {
				return nil, nil, workContextNavigationFailure(KindGitUnreachable, "cannot verify navigation absence at historical source HEAD")
			}
			entries, parseErr := parseTreeEntries(out)
			if parseErr != nil {
				return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "historical navigation tree entries are malformed")
			}
			if len(entries) == 0 && !workContextNavigationPresentOnDisk(source.repo) {
				current, currentErr := resolveKnowledgeHeadCheap(source.repo, "HEAD")
				if currentErr != nil || current != head {
					return nil, nil, workContextNavigationFailure(KindStaleRequiresReview, "historical source HEAD changed during navigation inspection")
				}
				return nil, &head, nil
			}
		}
		return nil, nil, err
	}
	if companion == nil {
		inventory, err := workContextNavigationBlob(ctx, source, workContextNavigationInventory, true)
		if err != nil {
			return nil, nil, err
		}
		if inventory != nil {
			var format struct {
				SchemaVersion string `json:"schema_version"`
			}
			if err := workContextNavigationDecode(inventory, &format); err != nil {
				return nil, nil, err
			}
			if format.SchemaVersion != "1.0" {
				return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "complete or unsupported navigation inventory has no pinned companion")
			}
		}
		return nil, nil, nil
	}
	objectKind, err := runGit(ctx, source.repo, "cat-file", "-t", source.commitOID)
	if err != nil || string(bytes.TrimSpace(objectKind)) != "commit" {
		return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "navigation source OID does not name a commit")
	}
	var adoption struct {
		SchemaVersion string            `json:"schema_version"`
		Unresolved    []json.RawMessage `json:"unresolved"`
	}
	if err := workContextNavigationDecode(companion, &adoption); err != nil {
		return nil, nil, err
	}
	if adoption.SchemaVersion == "1.0" {
		return nil, nil, nil
	}
	if adoption.SchemaVersion != "1.1" {
		return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "unsupported adopted navigation format")
	}
	if adoption.Unresolved == nil || len(adoption.Unresolved) != 0 {
		return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "complete navigation companion has unresolved paths")
	}
	data, err := workContextNavigationBlob(ctx, source, workContextNavigationInventory, false)
	if err != nil {
		return nil, nil, err
	}
	var inventory struct {
		SchemaVersion   string            `json:"schema_version"`
		UnresolvedCount *int              `json:"unresolved_count"`
		Unresolved      []json.RawMessage `json:"unresolved"`
		Domains         []struct {
			DomainID string `json:"domain_id"`
			CardPath string `json:"card_path"`
		} `json:"domains"`
	}
	if err := workContextNavigationDecode(data, &inventory); err != nil {
		return nil, nil, err
	}
	if inventory.SchemaVersion != "1.1" || inventory.UnresolvedCount == nil || *inventory.UnresolvedCount != 0 || inventory.Unresolved == nil || len(inventory.Unresolved) != 0 || inventory.Domains == nil {
		return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "unsupported or incomplete navigation inventory")
	}
	cards, paths := map[string]string{}, map[string]bool{}
	for _, domain := range inventory.Domains {
		if !validateWorkContextDomainID(domain.DomainID) || cards[domain.DomainID] != "" || paths[domain.CardPath] {
			return nil, nil, workContextNavigationFailure(KindInvalidNoteProof, "duplicate or invalid navigation card identity")
		}
		if err := validateWorkContextRepoPath(domain.CardPath); err != nil {
			return nil, nil, err
		}
		cards[domain.DomainID], paths[domain.CardPath] = domain.CardPath, true
	}
	return cards, nil, nil
}

func assembleWorkContextNavigation(ctx context.Context, q queryer, workID string, affected []string, view *WorkContextView) (map[string][]WorkContextDomainCard, error) {
	if proof, ok := ctx.Value(workContextNavigationProofKey{}).(*workContextNavigationProof); ok && proof.workErrors[workID] != nil {
		return nil, proof.workErrors[workID]
	}
	productID, rootID, registryHash, sources, err := workContextNavigationSources(ctx, q, workID, view)
	if err != nil {
		var failure *Failure
		if !errors.As(err, &failure) {
			return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot resolve navigation source authority", true, "retry once the source projection is readable", err)
		}
		return nil, err
	}
	result := map[string][]WorkContextDomainCard{}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, "work_context_read")
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	var bindingHash string
	if err == nil {
		err = q.QueryRowContext(ctx, `SELECT domain_registry_content_hash FROM workflow_architecture_bindings WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&bindingHash)
		if err != nil && err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot read navigation architecture binding", true, "retry once the contract projection is readable", err)
		}
	}
	domains := append([]string{}, affected...)
	if !containsString(domains, rootID) && rootID != "" {
		domains = append(domains, rootID)
	}
	seenTrees := map[string]bool{}
	readings := map[WorkContextReadingSource]int{}
	for ordinal, reading := range view.RequiredReading {
		readings[reading.Source] = ordinal
	}
	for _, source := range sources {
		key := source.projectID + "\x00" + source.commitOID
		if seenTrees[key] {
			continue
		}
		seenTrees[key] = true
		material, err := readWorkContextNavigationMaterial(ctx, source)
		if err != nil {
			return nil, err
		}
		cards := material.cards
		if cards == nil {
			continue
		}
		if productID == "" {
			return nil, workContextNavigationFailure(KindAmbiguousScope, "adopted navigation context has more than one primary Product")
		}
		if rootID == "" {
			return nil, workContextNavigationFailure(KindDomainRegistryAbsent, "complete navigation requires the shared Product Domain registry")
		}
		if bindingHash != "" && bindingHash != registryHash {
			return nil, workContextNavigationFailure(KindStaleRequiresReview, "navigation contract Domain registry binding is stale")
		}
		for domainID := range cards {
			var status, hash string
			err := q.QueryRowContext(ctx, `SELECT status,registry_content_hash FROM domains WHERE product_id=? AND domain_id=?`, productID, domainID).Scan(&status, &hash)
			if err != nil && err != sql.ErrNoRows {
				return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot validate navigation Domain identity", true, "retry once the Domain projection is readable", err)
			}
			if err != nil || status != "current" || hash != registryHash {
				return nil, workContextNavigationFailure(KindStaleRequiresReview, "navigation inventory names an unknown or stale Domain: "+domainID)
			}
		}
		for _, domainID := range domains {
			path := cards[domainID]
			if path == "" {
				return nil, workContextNavigationFailure(KindInvalidNoteProof, "navigation inventory lacks required Domain: "+domainID)
			}
			if err := material.cardErrors[domainID]; err != nil {
				return nil, err
			}
			ref := WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: source.projectID, Path: path, CommitOID: source.commitOID}
			if ordinal, exists := readings[ref]; exists {
				if view.RequiredReading[ordinal].DomainID != domainID {
					return nil, workContextNavigationFailure(KindStaleRequiresReview, "existing card reading has a stale Domain binding: "+path)
				}
			} else {
				if len(view.RequiredReading) >= WorkContextRequiredReadingMax {
					return nil, workContextNavigationFailure(KindLimitExceeded, "mandatory Domain cards exceed the existing 32-reading bound")
				}
				reading := WorkContextReading{DomainID: domainID, Reason: "Read the pinned Domain navigation card.", Source: ref}
				if domainID == rootID {
					reading.ProductWideRationale = "The root card describes Product-wide navigation for the affected Domains."
				}
				if err := validateWorkContextReading(reading); err != nil {
					return nil, err
				}
				readings[ref] = len(view.RequiredReading)
				view.RequiredReading = append(view.RequiredReading, reading)
			}
			result[domainID] = append(result[domainID], WorkContextDomainCard(ref))
		}
	}
	return result, nil
}

func validateWorkContextNavigationCard(content []byte, domainID, path string) error {
	// This generated identity binds the inventory path, not its filename.
	identity := []byte("Domain: `" + domainID + "`. Coarse navigation owner: `domain:" + domainID + "`.")
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 4 || !bytes.Equal(lines[3], identity) || bytes.Count(content, []byte("\nDomain: `")) != 1 {
		return workContextNavigationFailure(KindStaleRequiresReview, "navigation card has a stale Domain binding: "+path)
	}
	return nil
}
