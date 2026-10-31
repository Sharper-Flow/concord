package store

import "context"

// Domain-registry consumers (work-context declarations, Domain observations,
// investigation confirmation) read the committed Domain registry from the same
// knowledgeSourceSetProof every law boundary consumes. The proof is
// established before the caller's transaction opens, because resolving the
// head and reading the committed registry are Git subprocesses the pooled
// connection must never hold (CD-0195 D2). Inside the transaction the consumer
// revalidates the proof's home binding and committed content with the bounded
// small-file checks, then reads the snapshot the proof carries. There is no
// second proof carrier, no Store-held cache, and no SQL Domain projection
// (law-folder-authority.md, Committed reads).

// EstablishDomainRegistryProof verifies the Product's designated knowledge
// home and returns the context carrying the proof that the Product's
// in-transaction registry consumers revalidate. A proof already in the context
// that binds that home for the Product is reused rather than re-read; the
// transaction revalidates it either way. An unresolvable home, an unreachable
// home, or an invalid committed law folder or registry refuses: a registry
// boundary has no degraded form.
func (s *Store) EstablishDomainRegistryProof(ctx context.Context, productID string) (context.Context, error) {
	if s == nil || s.db == nil {
		return ctx, newFailure(KindUnavailable, "domain_registry", "store is not open", false, "open a store before reading the committed Domain registry")
	}
	home, err := resolveKnowledgeQueryHome(ctx, s.db, productID, "", KnowledgeHome{}, "domain_registry")
	if err != nil {
		return ctx, err
	}
	if proof := knowledgeSourceSetProofFrom(ctx); proof != nil && proof.productID == productID && proof.refreshErr == nil && sourceBoundInProof(ctx, home) {
		return ctx, nil
	}
	verified, err := s.establishKnowledgeSourceSetProofAtHome(ctx, home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		return ctx, err
	}
	if proof := knowledgeSourceSetProofFrom(verified); proof == nil || proof.productID != productID || !sourceBoundInProof(verified, home) {
		return ctx, newFailure(KindUnavailable, "domain_registry", "the Product's knowledge home "+knowledgeSourceProofKey(home)+" could not be verified", true, "retry once the Product's knowledge home designation is readable")
	}
	return verified, nil
}

// lawSnapshotForProduct returns the committed law-folder snapshot of one
// Product's designated home after revalidating the transaction's proof for
// that home: the designation, the resolved canonical location, and the
// committed law and registry content. It reads only SQL and small files; no
// Git subprocess runs inside the transaction (CD-0195 D2).
func lawSnapshotForProduct(ctx context.Context, q queryer, productID, op string) (*lawFolderSnapshot, error) {
	home, err := resolveKnowledgeQueryHome(ctx, q, productID, "", KnowledgeHome{}, op)
	if err != nil {
		return nil, err
	}
	if err := requireKnowledgeSourceSetProof(ctx, q, home.HomeProjectID, home.HomeLocatorID); err != nil {
		return nil, err
	}
	return lawSnapshotForSource(ctx, home, op)
}

// lawSnapshotForWork returns the committed Domain registry snapshot of the
// Product that owns the work's contract: the Product of its primary Project.
// A Domain registry belongs to a Product, so a work resolves no registry from
// a standalone Project home or from another Product it is merely a member of.
func lawSnapshotForWork(ctx context.Context, q queryer, workID, op string) (*lawFolderSnapshot, error) {
	productID, err := architectureBindingProductID(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	return lawSnapshotForProduct(ctx, q, productID, op)
}

// EnsureWorkProductKnowledgeFresh verifies the knowledge home of the work's
// law source set before an approval or supersession reaches its transaction,
// so an unreachable or invalid home refuses before any other admission work.
// It carries no proof forward: the owning action establishes the proof its
// transaction consumes, and a context-free caller cannot hand one on.
func (s *Store) EnsureWorkProductKnowledgeFresh(ctx context.Context, workID string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "knowledge_index", "store is not open", false, "open a store before reading the committed Domain registry")
	}
	_, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	return err
}
