package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

// knowledgeSourcePayload is the shared event payload for Product
// knowledge-source registration. CD-0200 federates knowledge placement: a
// Product resolves its shared law from one designated home plus the member
// Project canonical-path locators the operator registers as sources.
// PM6 §2 keeps exactly one designated home; registration never moves it.
type knowledgeSourcePayload struct {
	ProductID        string `json:"product_id"`
	ProjectID        string `json:"project_id"`
	LocatorID        string `json:"locator_id"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

func decodeKnowledgeSourcePayload(event Event) (knowledgeSourcePayload, error) {
	var payload knowledgeSourcePayload
	if err := decodePayload(event, &payload); err != nil {
		return payload, err
	}
	if payload.ProductID == "" || payload.ProjectID == "" || payload.LocatorID == "" || payload.Reason == "" {
		return payload, newFailure(KindInvalidPayload, "fold_event", "knowledge source payload requires product_id, project_id, locator_id, and reason", false,
			"supply a member Project, one of its canonical-path locators, and a non-empty reason")
	}
	return payload, nil
}

// verifyKnowledgeSourceEligibility reuses the PM6 §2 home eligibility checks
// (member Project, canonical-path locator) and adds the CD-0200 source rules:
// the designated home is always a source, so registering its locator is a
// typed conflict, and a locator that serves another Product's home or source
// set cannot join this Product.
func verifyKnowledgeSourceEligibility(ctx context.Context, tx queryer, payload knowledgeSourcePayload) error {
	var locatorProject string
	var locatorKind string
	err := tx.QueryRowContext(ctx, `SELECT project_id, kind FROM project_locators WHERE locator_id = ?`, payload.LocatorID).Scan(&locatorProject, &locatorKind)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "Project locator does not exist", false,
			"add the canonical-path locator to the Project before registering it a knowledge source")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Project locator", true, "retry once the database is readable", err)
	}
	if locatorProject != payload.ProjectID {
		return newFailure(KindProjectionConflict, "fold_event", "Project locator belongs to a different Project", false,
			"register a locator that belongs to the member Project")
	}
	if locatorKind != string(LocatorCanonicalPath) {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge source locator is not a canonical path", false,
			"register the Project's canonical_path locator")
	}
	var member int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM product_projects WHERE product_id = ? AND project_id = ?`, payload.ProductID, payload.ProjectID).Scan(&member)
	if err == sql.ErrNoRows {
		return newFailure(KindMembershipConflict, "fold_event", "Project is not a member of the Product", false,
			"add the Product/Project membership before registering the knowledge source")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Product membership", true, "retry once the database is readable", err)
	}
	var homeProject, homeLocator string
	err = tx.QueryRowContext(ctx, `SELECT project_id, locator_id FROM product_knowledge_homes WHERE product_id = ?`, payload.ProductID).Scan(&homeProject, &homeLocator)
	if err != nil && err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the Product knowledge home", true, "retry once the database is readable", err)
	}
	if err == nil && homeProject == payload.ProjectID && homeLocator == payload.LocatorID {
		return newFailure(KindProjectionConflict, "fold_event", "locator is the Product's designated knowledge home, which is always a source", false,
			"register a different member Project canonical-path locator")
	}
	return nil
}

func foldProductKnowledgeSourceRegistered(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	payload, err := decodeKnowledgeSourcePayload(event)
	if err != nil {
		return err
	}
	if payload.ProductID != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge source payload names a different Product", false,
			"register the source on the event's own Product")
	}
	if err := verifyKnowledgeSourceEligibility(ctx, tx, payload); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO product_knowledge_sources(product_id, project_id, locator_id, registered_at)
		VALUES (?, ?, ?, ?)`,
		payload.ProductID, payload.ProjectID, payload.LocatorID, event.OccurredAt); err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "locator already serves another Product's knowledge home or source set", false,
				"remove the other Product's registration or register a different locator")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot register Product knowledge source", true,
			"retry once the database is writable", err)
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

func foldProductKnowledgeSourceRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload knowledgeSourcePayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.ProductID == "" || payload.Reason == "" {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge source payload requires product_id and reason", false,
			"supply the Product and a non-empty reason")
	}
	if payload.ProductID != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge source payload names a different Product", false,
			"remove the source on the event's own Product")
	}
	var projectID, locatorID string
	err := tx.QueryRowContext(ctx, `SELECT project_id, locator_id FROM product_knowledge_sources WHERE product_id = ? AND project_id = ? AND locator_id = ?`,
		payload.ProductID, payload.ProjectID, payload.LocatorID).Scan(&projectID, &locatorID)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "locator is not a registered knowledge source of the Product", false,
			"register the source before removing it")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the registered knowledge source", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_knowledge_sources WHERE product_id = ? AND project_id = ? AND locator_id = ?`,
		payload.ProductID, payload.ProjectID, payload.LocatorID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Product knowledge source", true,
			"retry once the database is writable", err)
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

// ProductKnowledgeSourceRegistration is the operator request shape for CD-0200
// source registration.
type ProductKnowledgeSourceRegistration struct {
	ProductID       string
	ProjectID       string
	LocatorID       string
	Reason          string
	ExpectedVersion int64
}

// applyProductKnowledgeConfiguration validates the shared operator request
// shape and appends one Product-scoped event that configures the Product's
// knowledge source set (PM6 §2, CD-0200): the designated home, a registered
// source, or a removed source. The event kind owns the fold; the Product
// version check serializes the mutations. The payload constructor receives
// the reason after the default fills in, so the event always carries one.
func applyProductKnowledgeConfiguration(ctx context.Context, s *Store, op, eventKind, defaultReason string, request ProductKnowledgeSourceRegistration, payload func(reason string) any) (ApplyOperationResult, error) {
	if request.ProductID == "" || request.ProjectID == "" || request.LocatorID == "" || request.ExpectedVersion < 1 {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, op, "Product, member Project, locator, and positive Product version are required", false,
			"supply an existing Product, its current version, and a member Project locator")
	}
	if request.Reason == "" {
		request.Reason = defaultReason
	}
	encoded, err := json.Marshal(payload(request.Reason))
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{{
			EventID: operatorEventID(eventKind, request.ProductID), Kind: eventKind,
			SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: encoded,
		}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedVersion},
	})
}

// RegisterProductKnowledgeSource records a member Project canonical-path
// locator as a Product knowledge source (CD-0200).
func (s *Store) RegisterProductKnowledgeSource(ctx context.Context, request ProductKnowledgeSourceRegistration) (ApplyOperationResult, error) {
	return applyProductKnowledgeConfiguration(ctx, s, "product_knowledge_source_register", "product.knowledge_source_registered", "operator source registration", request, func(reason string) any {
		return knowledgeSourcePayload{
			ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
			Reason: reason, ExpectedVersion: request.ExpectedVersion, ResultingVersion: request.ExpectedVersion + 1,
		}
	})
}

// RemoveProductKnowledgeSource removes a registered knowledge source. The
// designated home is not a removable row: it never appears in the table, so
// its removal is a typed not-found refusal (CD-0200 keeps exactly one home).
func (s *Store) RemoveProductKnowledgeSource(ctx context.Context, request ProductKnowledgeSourceRegistration) (ApplyOperationResult, error) {
	return applyProductKnowledgeConfiguration(ctx, s, "product_knowledge_source_remove", "product.knowledge_source_removed", "operator source removal", request, func(reason string) any {
		return knowledgeSourcePayload{
			ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
			Reason: reason, ExpectedVersion: request.ExpectedVersion, ResultingVersion: request.ExpectedVersion + 1,
		}
	})
}

// ProductKnowledgeSourceRegistrations names a Product's registered knowledge
// sources (CD-0200), home first, then registrations ordered by Project and
// locator.
func (s *Store) ProductKnowledgeSourceRegistrations(ctx context.Context, productID string) ([]KnowledgeHome, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "product_knowledge_sources", "store is not open", false, "open a store before reading knowledge sources")
	}
	return productKnowledgeSourceRegistrations(ctx, s.db, productID)
}

func productKnowledgeSourceRegistrations(ctx context.Context, q queryer, productID string) ([]KnowledgeHome, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT ps.project_id, ps.locator_id, pl.locator_value
		FROM product_knowledge_sources ps
		JOIN project_locators pl ON pl.locator_id = ps.locator_id AND pl.project_id = ps.project_id AND pl.kind = 'canonical_path'
		WHERE ps.product_id = ?
		ORDER BY ps.project_id, ps.locator_id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "product_knowledge_sources", "cannot read Product knowledge sources", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var homes []KnowledgeHome
	for rows.Next() {
		var home KnowledgeHome
		if err := rows.Scan(&home.HomeProjectID, &home.HomeLocatorID, &home.RepoPath); err != nil {
			return nil, wrapFailure(KindUnavailable, "product_knowledge_sources", "cannot decode Product knowledge source", true, "retry once the database is readable", err)
		}
		home.HeadRef = "HEAD"
		homes = append(homes, home)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "product_knowledge_sources", "cannot finish reading Product knowledge sources", true, "retry once the database is readable", err)
	}
	return homes, nil
}

// resolveKnowledgeQuerySources resolves the Product's full source set: the
// designated home first, then registered sources ordered by Project and
// locator. A Product with no registration takes a one-element set, which is
// the identical single-home path (CD-0200 single-source rule).
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
	sources := []KnowledgeHome{candidates[0]}
	registered, err := productKnowledgeSourceRegistrations(ctx, q, productID)
	if err != nil {
		return nil, err
	}
	sources = append(sources, registered...)
	sort.SliceStable(sources[1:], func(i, j int) bool {
		a, b := sources[1+i], sources[1+j]
		if a.HomeProjectID != b.HomeProjectID {
			return a.HomeProjectID < b.HomeProjectID
		}
		return a.HomeLocatorID < b.HomeLocatorID
	})
	return sources, nil
}

// resolveKnowledgeManifestRole names the manifest validation role a home
// reads under (CD-0200): the designated shared-law home keeps the
// registry-required contract, a registered source refuses a local registry,
// and a home that is neither stays a standalone corpus under the home-role
// contract it always had.
func resolveKnowledgeManifestRole(ctx context.Context, q queryer, home KnowledgeHome) (knowledgeManifestRole, error) {
	var designated string
	err := q.QueryRowContext(ctx, `SELECT product_id FROM product_knowledge_homes WHERE project_id = ? AND locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&designated)
	if err == nil {
		return manifestSharedHomeRole, nil
	}
	if err != sql.ErrNoRows {
		return manifestSharedHomeRole, wrapFailure(KindUnavailable, "knowledge_manifest_role", "cannot resolve the knowledge home designation", true, "retry once the database is readable", err)
	}
	var registered string
	err = q.QueryRowContext(ctx, `SELECT product_id FROM product_knowledge_sources WHERE project_id = ? AND locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&registered)
	if err == nil {
		return manifestRegisteredSourceRole, nil
	}
	if err != sql.ErrNoRows {
		return manifestSharedHomeRole, wrapFailure(KindUnavailable, "knowledge_manifest_role", "cannot resolve the knowledge source registration", true, "retry once the database is readable", err)
	}
	return manifestSharedHomeRole, nil
}

// resolveKnowledgeSourceRole locates the Product whose source set a home
// belongs to and whether the home is that Product's designated shared-law
// home. A home outside every source set resolves as a standalone home.
func resolveKnowledgeSourceRole(ctx context.Context, q queryer, home KnowledgeHome) (productID string, designated bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT product_id FROM product_knowledge_homes WHERE project_id = ? AND locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&productID)
	if err == nil {
		return productID, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot resolve the knowledge home designation", true, "retry once the database is readable", err)
	}
	err = q.QueryRowContext(ctx, `SELECT product_id FROM product_knowledge_sources WHERE project_id = ? AND locator_id = ?`, home.HomeProjectID, home.HomeLocatorID).Scan(&productID)
	if err == nil {
		return productID, false, nil
	}
	if err != sql.ErrNoRows {
		return "", false, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot resolve the knowledge source registration", true, "retry once the database is readable", err)
	}
	return "", false, nil
}

// knowledgeSourceSetDigest binds a federated Q9 cursor to the exact source
// set it paged over (CD-0200): a registration or removal between pages
// invalidates the outstanding cursor instead of silently changing coverage.
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

// validateFederatedSourceManifest validates a manifest's cross-source law
// relations over the Product's verified source set at the rebuild boundary
// (CD-0200). It runs for both federated roles: a registered source, and the
// designated shared-law home. A relation target outside the declaring
// manifest names its source Project; the rebuild refuses a target Project
// outside the source set, an unresolved target law, a conflicts_with pair
// across sources, and — from a non-home source only — any
// supersedes/refines/subordinate_to edge toward the shared home's law: no
// inferred precedence, and conflicts block until an accepted relation or
// amendment resolves them. The shared home may declare precedence toward
// member source law; the reverse refuses at the source's own rebuild.
func validateFederatedSourceManifest(ctx context.Context, q queryer, home KnowledgeHome, manifest KnowledgeManifest) error {
	productID, designated, err := resolveKnowledgeSourceRole(ctx, q, home)
	if err != nil {
		return err
	}
	sourceRole := !designated
	if sourceRole && productID == "" {
		return newFailure(KindInvariantViolation, "rebuild_knowledge_index", "a registered source manifest validated outside a registered source role", false, "rebuild the source through the Product's registered source set")
	}
	sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "rebuild_knowledge_index")
	if err != nil {
		return err
	}
	sourceByProject := make(map[string]KnowledgeHome, len(sources))
	for _, source := range sources {
		sourceByProject[source.HomeProjectID] = source
	}
	for _, record := range manifest.Records {
		for _, relation := range record.LawRelations {
			if relation.SourceProjectID == "" {
				continue
			}
			target, known := sourceByProject[relation.SourceProjectID]
			if !known {
				return newFailure(KindUnknownScope, "rebuild_knowledge_index", "cross-source relation names a Project outside the Product's registered source set: "+relation.SourceProjectID, false, "register the target Project as a Product knowledge source, or point the relation inside the source set")
			}
			if target.HomeProjectID == home.HomeProjectID && target.HomeLocatorID == home.HomeLocatorID {
				return newFailure(KindInvalidNoteProof, "rebuild_knowledge_index", "cross-source relation names its own manifest's Project as the target source", false, "declare the relation without source_project_id when the target lives in this manifest")
			}
			if relation.Kind == "conflicts_with" {
				return newFailure(KindRelationConflict, "rebuild_knowledge_index", "cross-source conflicts_with between "+record.ID+" and "+relation.SourceProjectID+"/"+relation.TargetID+" blocks the rebuild", false, "resolve the conflict through an accepted amendment before indexing either side")
			}
			if sourceRole && target.HomeLocatorID == sharedLawHomeLocator(ctx, q, productID) && lawRelationKinds[relation.Kind] && relation.Kind != "conflicts_with" {
				return newFailure(KindRelationConflict, "rebuild_knowledge_index", "a non-home source may not declare "+relation.Kind+" toward shared-home law: "+relation.SourceProjectID+"/"+relation.TargetID, false, "amend the shared law through its authoring home, or remove the precedence declaration")
			}
			var present int
			if err := q.QueryRowContext(ctx, `SELECT 1 FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, target.HomeProjectID, target.HomeLocatorID, relation.TargetID).Scan(&present); err == sql.ErrNoRows {
				return newFailure(KindProjectionNotFound, "rebuild_knowledge_index", "cross-source relation target is unresolved: "+relation.SourceProjectID+"/"+relation.TargetID, false, "publish and rebuild the target source before declaring the relation")
			} else if err != nil {
				return wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot read the cross-source relation target", true, "retry once the database is readable", err)
			}
		}
	}
	return nil
}

// sharedLawHomeLocator reads the locator of the Product's designated shared
// home. The designation rule guarantees exactly one row; an absent row means
// no source can declare precedence toward a home that does not exist, so the
// empty result simply disables the precedence refusal.
func sharedLawHomeLocator(ctx context.Context, q queryer, productID string) string {
	var locator string
	_ = q.QueryRowContext(ctx, `SELECT locator_id FROM product_knowledge_homes WHERE product_id=?`, productID).Scan(&locator)
	return locator
}

// prepareFederatedSourceDomainProjection builds the Domain projection a
// registered source writes from the shared home's registry (CD-0200). Only
// the shared home carries the registry, so a source's Domain references
// validate against the registry the shared home projected, and the source
// writes only its law Domain homes and applicability rows.
func prepareFederatedSourceDomainProjection(ctx context.Context, q queryer, productID string, manifest KnowledgeManifest) (domainProjection, error) {
	result := domainProjection{ProductID: productID, SourceRole: true}
	var productKey, rootDomain, registryHash string
	err := q.QueryRowContext(ctx, `SELECT product_key,root_domain_id,content_hash FROM domain_registries WHERE product_id=?`, productID).Scan(&productKey, &rootDomain, &registryHash)
	if errors.Is(err, sql.ErrNoRows) {
		return result, newFailure(KindDomainRegistryAbsent, "rebuild_knowledge_index", "the shared-law home has no Domain registry projection", false, "rebuild the Product knowledge home before rebuilding a registered source")
	}
	if err != nil {
		return result, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot read the shared Domain registry", true, "retry once the database is readable", err)
	}
	result.ProductKey, result.RootDomainID, result.RegistryHash = productKey, rootDomain, registryHash
	rows, err := q.QueryContext(ctx, `SELECT domain_id FROM domains WHERE product_id=? ORDER BY domain_id`, productID)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot read the shared Domain projection", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	known := map[string]bool{}
	for rows.Next() {
		var domainID string
		if err := rows.Scan(&domainID); err != nil {
			return result, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot decode the shared Domain projection", true, "retry once the database is readable", err)
		}
		known[domainID] = true
	}
	if err := rows.Err(); err != nil {
		return result, wrapFailure(KindUnavailable, "rebuild_knowledge_index", "cannot finish reading the shared Domain projection", true, "retry once the database is readable", err)
	}
	rows.Close()
	result.LawHomes, result.LawApplicability = map[string]string{}, map[string][]string{}
	for _, record := range manifest.Records {
		if !manifestLawBearingKinds[record.Kind] || record.HomeDomainID == "" {
			continue
		}
		if !known[record.HomeDomainID] {
			return result, newFailure(KindUnknownScope, "rebuild_knowledge_index", "law home Domain is unknown to the shared registry: "+record.HomeDomainID, false, "assign the law to a Domain the shared home's registry declares")
		}
		result.LawHomes[record.ID] = record.HomeDomainID
		result.LawApplicability[record.ID] = uniqueSorted(record.AppliesToDomainIDs)
	}
	return result, nil
}
