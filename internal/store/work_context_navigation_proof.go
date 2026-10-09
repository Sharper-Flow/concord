package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
)

// WorkContextNavigationRequest names the bounded sources a read or mutation
// will consume, including a capture's Projects and a new declaration's pins.
type WorkContextNavigationRequest struct {
	WorkIDs    []string
	ProjectIDs []string
	Sources    []WorkContextReadingSource
}

type workContextNavigationMaterial struct {
	cards      map[string]string
	cardErrors map[string]error
	err        error
	legacyHead *string
}

type workContextNavigationProof struct {
	sources    map[workContextNavigationSource]workContextNavigationMaterial
	workErrors map[string]error
}
type workContextNavigationProofKey struct{}

// EstablishWorkContextNavigationProof materializes immutable Git facts before
// the caller opens its snapshot. The transaction resolves its own source set
// through SQL and requires matching Project, locator and OID facts; a changed
// source is refused, never probed while the single connection is held. Errors
// in an object are carried to the consuming reader so replay and admission
// retain their existing refusal order. This is request-local, not a cache or
// a second context projection.
func (s *Store) EstablishWorkContextNavigationProof(ctx context.Context, request WorkContextNavigationRequest) (context.Context, error) {
	if s == nil || s.db == nil {
		return ctx, workContextNavigationFailure(KindUnavailable, "store is not open")
	}
	previous, _ := ctx.Value(workContextNavigationProofKey{}).(*workContextNavigationProof)
	proof := mergeWorkContextNavigationProof(previous)
	sources := []workContextNavigationSource{}
	for _, workID := range request.WorkIDs {
		delete(proof.workErrors, workID)
		var raw []byte
		err := s.db.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowWorkContextRecorded).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			proof.workErrors[workID] = err
			continue
		}
		var declaration workflowWorkContextRecordedPayload
		// The shared reader owns malformed declaration refusals.
		_ = json.Unmarshal(raw, &declaration)
		for _, source := range request.Sources {
			declaration.RequiredReading = append(declaration.RequiredReading, WorkContextReading{Source: source})
		}
		_, _, _, resolved, err := workContextNavigationSources(ctx, s.db, workID, &WorkContextView{RequiredReading: declaration.RequiredReading})
		if err != nil {
			proof.workErrors[workID] = err
			continue
		}
		sources = append(sources, resolved...)
	}
	for _, projectID := range request.ProjectIDs {
		products, err := navigationProjectProducts(ctx, s.db, projectID)
		if err != nil {
			continue // The consuming reader owns source-resolution refusals.
		}
		for _, productID := range products {
			_, _, resolved, err := navigationProductSources(ctx, s.db, productID)
			if err != nil {
				continue
			}
			sources = append(sources, resolved...)
		}
	}
	for _, reading := range request.Sources {
		if len(request.WorkIDs) != 0 {
			break // Work-scoped sources use the reader's locator selection above.
		}
		if reading.Kind != WorkContextSourceRepositoryFile {
			continue
		}
		homes, err := projectCanonicalHomeCandidates(ctx, s.db, reading.ProjectID)
		if err != nil {
			continue
		}
		if len(homes) != 0 {
			sources = append(sources, workContextNavigationSource{reading.ProjectID, homes[0].RepoPath, reading.CommitOID})
		}
	}
	for _, source := range sources {
		if _, exists := proof.sources[source]; exists {
			continue
		}
		proof.sources[source] = materializeWorkContextNavigation(ctx, source)
		if err := ctx.Err(); err != nil {
			return ctx, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, workContextNavigationProofKey{}, proof), nil
}

func navigationProjectProducts(ctx context.Context, q queryer, projectID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT product_id FROM product_projects WHERE project_id=? ORDER BY product_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var products []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		products = append(products, id)
	}
	return products, rows.Err()
}

func materializeWorkContextNavigation(ctx context.Context, source workContextNavigationSource) workContextNavigationMaterial {
	cards, legacyHead, err := workContextNavigationCards(ctx, source)
	material := workContextNavigationMaterial{cards: cards, err: err, cardErrors: map[string]error{}, legacyHead: legacyHead}
	if err != nil {
		return material
	}
	for domainID, path := range cards {
		content, err := workContextNavigationBlob(ctx, source, path, false)
		if err == nil {
			err = validateWorkContextNavigationCard(content, domainID, path)
		}
		material.cardErrors[domainID] = err
	}
	return material
}

func readWorkContextNavigationMaterial(ctx context.Context, source workContextNavigationSource) (workContextNavigationMaterial, error) {
	if proof, ok := ctx.Value(workContextNavigationProofKey{}).(*workContextNavigationProof); ok {
		if material, exists := proof.sources[source]; exists {
			if material.legacyHead != nil {
				head, headErr := resolveKnowledgeHeadCheap(source.repo, "HEAD")
				if (headErr != nil && !workContextNavigationNonGit(source.repo)) || head != *material.legacyHead || workContextNavigationPresentOnDisk(source.repo) {
					return workContextNavigationMaterial{}, workContextNavigationFailure(KindStaleRequiresReview, "unverified historical navigation source changed after its pre-transaction inspection")
				}
			}
			return material, material.err
		}
		return workContextNavigationMaterial{}, workContextNavigationFailure(KindStaleRequiresReview, "navigation source identity changed after preparation")
	}
	// Historical non-Git subjects carry synthetic pins and no navigation.
	// A real repository or any adoption marker needs pre-transaction proof;
	// absence of a checkout file never proves absence in an immutable tree.
	if workContextNavigationNonGit(source.repo) {
		return workContextNavigationMaterial{}, nil
	}
	return workContextNavigationMaterial{}, workContextNavigationFailure(KindStaleRequiresReview, "navigation source was not materialized at its current Project, locator and commit before this transaction")
}

func prepareClaimNavigation(ctx context.Context, s *Store, req WorktreeClaimRequest, path string) *workContextNavigationProof {
	proof := mergeWorkContextNavigationProof()
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, req.WorkID, WorkflowWorkContextRecorded).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		proof.workErrors[req.WorkID] = err
		return proof
	}
	var declaration workflowWorkContextRecordedPayload
	_ = json.Unmarshal(raw, &declaration) // The consuming reader owns malformed declarations.
	sources := []workContextNavigationSource{{req.ProjectID, path, req.BaseSHA}}
	for _, reading := range declaration.RequiredReading {
		if reading.Source.Kind == WorkContextSourceRepositoryFile && reading.Source.ProjectID == req.ProjectID {
			sources = append(sources, workContextNavigationSource{req.ProjectID, path, reading.Source.CommitOID})
		}
	}
	for _, source := range sources {
		if _, exists := proof.sources[source]; !exists {
			proof.sources[source] = materializeWorkContextNavigation(ctx, source)
		}
	}
	return proof
}

func workContextNavigationNonGit(repo string) bool {
	for _, name := range []string{".git", "HEAD"} {
		if _, err := os.Lstat(filepath.Join(repo, name)); !os.IsNotExist(err) {
			return false
		}
	}
	return !workContextNavigationPresentOnDisk(repo)
}

func mergeWorkContextNavigationProof(proofs ...*workContextNavigationProof) *workContextNavigationProof {
	merged := &workContextNavigationProof{sources: map[workContextNavigationSource]workContextNavigationMaterial{}, workErrors: map[string]error{}}
	for _, proof := range proofs {
		if proof == nil {
			continue
		}
		for source, material := range proof.sources {
			merged.sources[source] = material
		}
		for workID, err := range proof.workErrors {
			merged.workErrors[workID] = err
		}
	}
	return merged
}

func prepareWorkNavigation(ctx context.Context, s *Store, workID string) (context.Context, error) {
	return s.EstablishWorkContextNavigationProof(ctx, WorkContextNavigationRequest{WorkIDs: []string{workID}})
}

func workContextNavigationTransactionContext(ctx context.Context, transaction *Transaction) context.Context {
	if transaction.navigation != nil {
		return context.WithValue(ctx, workContextNavigationProofKey{}, transaction.navigation)
	}
	return ctx
}
