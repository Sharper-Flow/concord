package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ProductCreation is the atomic operator bootstrap operation. PM5 requires a
// Product to have a Project membership, so Product and its first Project are
// created together rather than exposing an invalid intermediate state.
type ProductCreation struct {
	ProductID                              string
	DisplayName                            string
	StageMaturity                          string
	StageAudienceCommitment                string
	ProjectID                              string
	ProjectDisplayName                     string
	ProjectStageMaturityOverride           string
	ProjectStageAudienceCommitmentOverride string
	Role                                   string
	Reason                                 string
}

type ProjectCreation struct {
	ProjectID                       string
	DisplayName                     string
	StageMaturityOverride           string
	StageAudienceCommitmentOverride string
	ProductID                       string
	Role                            string
	Reason                          string
	ExpectedProductVersion          int64
}

type ProjectStageChange struct {
	ProjectID                       string
	StageMaturityOverride           string
	StageAudienceCommitmentOverride string
	ExpectedVersion                 int64
	Reason                          string
}

type ProductMembershipAddition struct {
	ProductID       string
	ProjectID       string
	Role            string
	Reason          string
	ExpectedVersion int64
}

// EntityVersion reads the current aggregate version needed to chain operator
// setup commands without maintaining a second version ledger outside SQLite.
func (s *Store) EntityVersion(ctx context.Context, subjectType SubjectType, id string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindInvalidOperation, "entity_version", "entity and ID are required", false, "supply a known entity")
	}
	return entityVersion(ctx, s.db, subjectType, id)
}

func entityVersion(ctx context.Context, q queryer, subjectType SubjectType, id string) (int64, error) {
	if id == "" {
		return 0, newFailure(KindInvalidOperation, "entity_version", "entity and ID are required", false, "supply a known entity")
	}
	table := ""
	label := ""
	switch subjectType {
	case SubjectProduct:
		table, label = "products", "Product"
	case SubjectProject:
		table, label = "projects", "Project"
	default:
		return 0, newFailure(KindInvalidSubject, "entity_version", "entity type is not versioned by operator setup", false, "use product or project")
	}
	var version int64
	if err := q.QueryRowContext(ctx, "SELECT version FROM "+table+" WHERE id=?", id).Scan(&version); err == sql.ErrNoRows {
		return 0, newFailure(KindProjectionNotFound, "entity_version", label+" does not exist", false, "create the entity before reading its version")
	} else if err != nil {
		return 0, wrapFailure(KindUnavailable, "entity_version", "cannot read "+label+" version", true, "retry once the database is readable", err)
	}
	return version, nil
}

func (s *Store) CreateProductWithProject(ctx context.Context, request ProductCreation) (ApplyOperationResult, error) {
	if request.ProductID == "" || request.DisplayName == "" || request.ProjectID == "" || request.ProjectDisplayName == "" || !validateProductStage(request.StageMaturity, request.StageAudienceCommitment) || !validateProjectStageOverride(request.ProjectStageMaturityOverride, request.ProjectStageAudienceCommitmentOverride) {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_create", "Product and initial Project fields are required", false, "supply valid Product stage and non-empty identities")
	}
	if request.Role != "primary" && request.Role != "secondary" {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_create", "membership role is not recognized", false, "use primary or secondary")
	}
	if request.Reason == "" {
		request.Reason = "operator bootstrap"
	}
	when := s.now()
	productPayload, _ := json.Marshal(map[string]string{
		"display_name": request.DisplayName, "stage_maturity": request.StageMaturity,
		"stage_audience_commitment": request.StageAudienceCommitment,
	})
	projectPayload, _ := json.Marshal(projectCreatedEventPayload(request.ProjectDisplayName, request.ProjectStageMaturityOverride, request.ProjectStageAudienceCommitmentOverride))
	projectPayloadVersion := 1
	if request.ProjectStageMaturityOverride != "" {
		projectPayloadVersion = 2
	}
	membershipPayload, _ := json.Marshal(map[string]any{
		"product_id": request.ProductID, "project_id": request.ProjectID, "role": request.Role,
		"reason": request.Reason, "expected_version": 1, "resulting_version": 2,
	})
	return ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{
			{EventID: operatorEventID("product.created", request.ProductID), Kind: "product.created", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: when, PayloadVersion: 1, Payload: productPayload},
			{EventID: operatorEventID("project.created", request.ProjectID), Kind: "project.created", SubjectType: SubjectProject, SubjectID: request.ProjectID, Actor: "operator", OccurredAt: when, PayloadVersion: projectPayloadVersion, Payload: projectPayload},
			{EventID: operatorEventID("product_project.added", request.ProductID+":"+request.ProjectID), Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: when, PayloadVersion: 1, Payload: membershipPayload},
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): 0, VersionRef(SubjectProject, request.ProjectID): 0},
	})
}

func (s *Store) CreateProjectForProduct(ctx context.Context, request ProjectCreation) (ApplyOperationResult, error) {
	if request.ProjectID == "" || request.DisplayName == "" || request.ProductID == "" || request.ExpectedProductVersion < 1 || !validateProjectStageOverride(request.StageMaturityOverride, request.StageAudienceCommitmentOverride) {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "project_create", "Project, Product, and positive Product version are required", false, "supply an existing Product and its current version")
	}
	if request.Role != "primary" && request.Role != "secondary" {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "project_create", "membership role is not recognized", false, "use primary or secondary")
	}
	if request.Reason == "" {
		request.Reason = "operator bootstrap"
	}
	when := s.now()
	projectPayload, _ := json.Marshal(projectCreatedEventPayload(request.DisplayName, request.StageMaturityOverride, request.StageAudienceCommitmentOverride))
	projectPayloadVersion := 1
	if request.StageMaturityOverride != "" {
		projectPayloadVersion = 2
	}
	membershipPayload, _ := json.Marshal(map[string]any{
		"product_id": request.ProductID, "project_id": request.ProjectID, "role": request.Role,
		"reason": request.Reason, "expected_version": request.ExpectedProductVersion, "resulting_version": request.ExpectedProductVersion + 1,
	})
	return ApplyOperationWithResult(ctx, s, Operation{
		Events: []Event{
			{EventID: operatorEventID("project.created", request.ProjectID), Kind: "project.created", SubjectType: SubjectProject, SubjectID: request.ProjectID, Actor: "operator", OccurredAt: when, PayloadVersion: projectPayloadVersion, Payload: projectPayload},
			{EventID: operatorEventID("product_project.added", request.ProductID+":"+request.ProjectID), Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: when, PayloadVersion: 1, Payload: membershipPayload},
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedProductVersion, VersionRef(SubjectProject, request.ProjectID): 0},
	})
}

func validateProjectStageOverride(maturity, audience string) bool {
	if maturity == "" && audience == "" {
		return true
	}
	return validateProductStage(maturity, audience)
}

func projectCreatedEventPayload(displayName, maturity, audience string) map[string]any {
	payload := map[string]any{"display_name": displayName}
	if maturity == "" && audience == "" {
		return payload
	}
	payload["stage_maturity_override"] = maturity
	payload["stage_audience_commitment_override"] = audience
	return payload
}

// ChangeProjectStage is the only operator write for Project stage. Clearing
// both values restores inheritance from the Product default; a partial pair
// is rejected before an event can be appended.
func (s *Store) ChangeProjectStage(ctx context.Context, request ProjectStageChange) (ApplyOperationResult, error) {
	if request.ProjectID == "" || request.ExpectedVersion < 1 || !validateProjectStageOverride(request.StageMaturityOverride, request.StageAudienceCommitmentOverride) {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "project_stage_change", "Project stage override must be a complete accepted pair or both empty", false, "supply both accepted override values or clear both")
	}
	if request.Reason == "" {
		request.Reason = "operator stage change"
	}
	payload := map[string]any{
		"stage_maturity_override":            nil,
		"stage_audience_commitment_override": nil,
		"reason":                             request.Reason,
		"expected_version":                   request.ExpectedVersion,
		"resulting_version":                  request.ExpectedVersion + 1,
	}
	if request.StageMaturityOverride != "" {
		payload["stage_maturity_override"] = request.StageMaturityOverride
		payload["stage_audience_commitment_override"] = request.StageAudienceCommitmentOverride
	}
	encoded, _ := json.Marshal(payload)
	return ApplyOperationWithResult(ctx, s, Operation{
		Events:           []Event{{EventID: operatorEventID("project.stage_changed", request.ProjectID), Kind: "project.stage_changed", SubjectType: SubjectProject, SubjectID: request.ProjectID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: encoded}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, request.ProjectID): request.ExpectedVersion},
	})
}

func (s *Store) AddProductProjectMembership(ctx context.Context, request ProductMembershipAddition) (ApplyOperationResult, error) {
	if request.ProductID == "" || request.ProjectID == "" || request.ExpectedVersion < 1 {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_project_add", "Product, Project, and positive expected version are required", false, "reload the Product and retry")
	}
	if request.Role != "primary" && request.Role != "secondary" {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_project_add", "membership role is not recognized", false, "use primary or secondary")
	}
	if request.Reason == "" {
		request.Reason = "operator membership"
	}
	payload, _ := json.Marshal(map[string]any{
		"product_id": request.ProductID, "project_id": request.ProjectID, "role": request.Role,
		"reason": request.Reason, "expected_version": request.ExpectedVersion, "resulting_version": request.ExpectedVersion + 1,
	})
	return ApplyOperationWithResult(ctx, s, Operation{
		Events:           []Event{{EventID: operatorEventID("product_project.added", request.ProductID+":"+request.ProjectID), Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedVersion},
	})
}

func operatorEventID(kind, subject string) string {
	return fmt.Sprintf("operator:%s:%s:%d", kind, subject, time.Now().UnixNano())
}
