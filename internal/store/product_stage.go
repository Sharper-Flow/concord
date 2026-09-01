package store

import (
	"context"
	"encoding/json"
)

// ProductStageChange is the operator request shape for recording a Product's
// maturity and audience commitment. CD-0091 D1 governs promotion: the rung
// evidence is a repository manifest checked by CI, the store records the
// resulting claim, and the cited reason is the durable link between them.
type ProductStageChange struct {
	ProductID               string
	StageMaturity           string
	StageAudienceCommitment string
	Reason                  string
	ExpectedVersion         int64
}

// ChangeProductStage is the only operator write for Product stage. The pair is
// complete and accepted or refused before an event is appended; the payload
// carries the operator's reason so a promotion names the evidence it cites.
func (s *Store) ChangeProductStage(ctx context.Context, request ProductStageChange) (ApplyOperationResult, error) {
	if request.ProductID == "" || request.ExpectedVersion < 1 || !validateProductStage(request.StageMaturity, request.StageAudienceCommitment) {
		return ApplyOperationResult{}, newFailure(KindInvalidOperation, "product_stage_change", "Product, positive expected version, and accepted Product stage values are required", false,
			"supply an existing Product, its current version, and accepted Product stage values")
	}
	if request.Reason == "" {
		request.Reason = "operator stage change"
	}
	payload, err := json.Marshal(map[string]any{
		"stage_maturity":            request.StageMaturity,
		"stage_audience_commitment": request.StageAudienceCommitment,
		"reason":                    request.Reason,
		"expected_version":          request.ExpectedVersion,
		"resulting_version":         request.ExpectedVersion + 1,
	})
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return ApplyOperationWithResult(ctx, s, Operation{
		Events:           []Event{{EventID: operatorEventID("product.stage_changed", request.ProductID), Kind: "product.stage_changed", SubjectType: SubjectProduct, SubjectID: request.ProductID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, request.ProductID): request.ExpectedVersion},
	})
}
