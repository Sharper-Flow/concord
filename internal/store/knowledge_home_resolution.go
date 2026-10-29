package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
)

func resolveKnowledgeQuerySources(ctx context.Context, q queryer, productID string, op string) ([]KnowledgeHome, error) {
	candidates, err := productKnowledgeHomeCandidates(ctx, q, productID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, newFailure(KindUnknownScope, op, "Product has no designated knowledge home", false, "designate exactly one Product knowledge home")
	}
	if len(candidates) > 1 {
		return nil, newAmbiguousScopeFailure(op, "Product has multiple canonical knowledge homes", "designate exactly one Product knowledge home", knowledgeHomeCandidateIDs(candidates))
	}
	return candidates, nil
}

func resolveKnowledgeSourceRole(ctx context.Context, q queryer, home KnowledgeHome) (productID string, designated bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT product_id FROM product_knowledge_homes WHERE project_id = ? AND locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&productID)
	if err == nil {
		return productID, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot resolve the knowledge home designation", true, "retry once the database is readable", err)
	}
	return "", false, nil
}

func knowledgeSourceSetDigest(sources []KnowledgeHome) string {
	hash := sha256.New()
	for _, source := range sources {
		hash.Write([]byte(source.HomeProjectID))
		hash.Write([]byte{0})
		hash.Write([]byte(source.HomeLocatorID))
		hash.Write([]byte{0x1e})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
