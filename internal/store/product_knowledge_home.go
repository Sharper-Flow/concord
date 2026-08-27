package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// knowledgeHomePayload is the shared event payload for Product knowledge-home
// designation. PM6 §2 permits a Product to hold zero or one designation, so a
// designation event replaces any previous row and a clear event removes it.
type knowledgeHomePayload struct {
	ProductID        string `json:"product_id"`
	ProjectID        string `json:"project_id"`
	LocatorID        string `json:"locator_id"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

func decodeKnowledgeHomePayload(event Event) (knowledgeHomePayload, error) {
	var payload knowledgeHomePayload
	if err := decodePayload(event, &payload); err != nil {
		return payload, err
	}
	if payload.ProductID == "" || payload.ProjectID == "" || payload.LocatorID == "" || payload.Reason == "" {
		return payload, newFailure(KindInvalidPayload, "fold_event", "knowledge home payload requires product_id, project_id, locator_id, and reason", false,
			"supply a member Project, one of its canonical-path locators, and a non-empty reason")
	}
	return payload, nil
}

// verifyKnowledgeHomeEligibility enforces PM6 §2: the home is a member
// Project's designated git locator, and Q9 Product scope resolves through the
// canonical-path join, so only a canonical-path locator can serve.
func verifyKnowledgeHomeEligibility(ctx context.Context, tx queryer, payload knowledgeHomePayload) error {
	var locatorProject string
	var locatorKind string
	err := tx.QueryRowContext(ctx, `SELECT project_id, kind FROM project_locators WHERE locator_id = ?`, payload.LocatorID).Scan(&locatorProject, &locatorKind)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "Project locator does not exist", false,
			"add the canonical-path locator to the Project before designating it a knowledge home")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Project locator", true, "retry once the database is readable", err)
	}
	if locatorProject != payload.ProjectID {
		return newFailure(KindProjectionConflict, "fold_event", "Project locator belongs to a different Project", false,
			"designate a locator that belongs to the member Project")
	}
	if locatorKind != string(LocatorCanonicalPath) {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge home locator is not a canonical path", false,
			"designate the Project's canonical_path locator")
	}
	var member int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM product_projects WHERE product_id = ? AND project_id = ?`, payload.ProductID, payload.ProjectID).Scan(&member)
	if err == sql.ErrNoRows {
		return newFailure(KindMembershipConflict, "fold_event", "Project is not a member of the Product", false,
			"add the Product/Project membership before designating the knowledge home")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Product membership", true, "retry once the database is readable", err)
	}
	return nil
}

func foldProductKnowledgeHomeDesignated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	payload, err := decodeKnowledgeHomePayload(event)
	if err != nil {
		return err
	}
	if payload.ProductID != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge home payload names a different Product", false,
			"designate the home on the event's own Product")
	}
	if err := verifyKnowledgeHomeEligibility(ctx, tx, payload); err != nil {
		return err
	}
	// One designation per Product (primary key) and one Product per locator
	// (unique pair). A replacement keeps a single row; a contested locator is
	// a typed conflict, never a silent takeover.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO product_knowledge_homes(product_id, project_id, locator_id)
		VALUES (?, ?, ?)
		ON CONFLICT(product_id) DO UPDATE SET project_id = excluded.project_id, locator_id = excluded.locator_id`,
		payload.ProductID, payload.ProjectID, payload.LocatorID); err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "Project locator is already another Product's knowledge home", false,
				"clear the other Product's designation or designate a different locator")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot designate Product knowledge home", true,
			"retry once the database is writable", err)
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

func foldProductKnowledgeHomeCleared(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload knowledgeHomePayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.ProductID == "" || payload.Reason == "" {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge home payload requires product_id and reason", false,
			"supply the Product and a non-empty reason")
	}
	if payload.ProductID != event.SubjectID {
		return newFailure(KindInvalidPayload, "fold_event", "knowledge home payload names a different Product", false,
			"clear the home on the event's own Product")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM product_knowledge_homes WHERE product_id = ?`, payload.ProductID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot clear Product knowledge home", true,
			"retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify Product knowledge home removal", true,
			"retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "Product has no knowledge home", false,
			"designate a knowledge home before clearing it")
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

// ProductKnowledgeHomeDesignation is the operator request shape for PM6 §2.
type ProductKnowledgeHomeDesignation struct {
	ProductID       string
	ProjectID       string
	LocatorID       string
	Reason          string
	ExpectedVersion int64
}

// DesignateProductKnowledgeHome records a Product's durable knowledge home as
// event-sourced operator configuration (PM6 §2/§3).
func (s *Store) DesignateProductKnowledgeHome(ctx context.Context, request ProductKnowledgeHomeDesignation) (ApplyOperationResult, error) {
	if request.ProductID == "" || request.ProjectID == "" || request.LocatorID == "" || request.ExpectedVersion < 1 {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_knowledge_home_designate", "Product, member Project, locator, and positive Product version are required", false,
			"supply an existing Product, its current version, and a member Project locator")
	}
	if request.Reason == "" {
		request.Reason = "operator designation"
	}
	encoded, err := json.Marshal(knowledgeHomePayload{
		ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
		Reason: request.Reason, ExpectedVersion: request.ExpectedVersion, ResultingVersion: request.ExpectedVersion + 1,
	})
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{{
			EventID: operatorEventID("product.knowledge_home_designated", request.ProductID), Kind: "product.knowledge_home_designated",
			SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: encoded,
		}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedVersion},
	})
}

// ClearProductKnowledgeHome removes a Product's knowledge home designation.
// PM6 §7 locks a designation only inside an in-flight publish; no publish
// state machine exists yet, so the clear is unguarded here and the lock lands
// with the compaction path that needs it.
func (s *Store) ClearProductKnowledgeHome(ctx context.Context, request ProductKnowledgeHomeDesignation) (ApplyOperationResult, error) {
	if request.ProductID == "" || request.ExpectedVersion < 1 {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_knowledge_home_clear", "Product and positive Product version are required", false,
			"supply an existing Product and its current version")
	}
	if request.Reason == "" {
		request.Reason = "operator clear"
	}
	encoded, err := json.Marshal(knowledgeHomePayload{
		ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
		Reason: request.Reason, ExpectedVersion: request.ExpectedVersion, ResultingVersion: request.ExpectedVersion + 1,
	})
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{{
			EventID: operatorEventID("product.knowledge_home_cleared", request.ProductID), Kind: "product.knowledge_home_cleared",
			SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: encoded,
		}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedVersion},
	})
}
