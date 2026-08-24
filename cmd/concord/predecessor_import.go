package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/sharper-flow/concord/internal/predecessor"
	"github.com/sharper-flow/concord/internal/store"
)

// importOperatorActor is the actor recorded on every event the predecessor
// import appends. It is distinct from the operator service's "operator" actor
// so provenance queries can separate migration writes from ordinary setup.
const importOperatorActor = "operator:predecessor-import"

// importFailureOp is the Op reported on typed failures raised by this verb so
// CLI diagnostics name the import, not a generic store operation.
const importFailureOp = "predecessor.import"

// validateProductStageAllowed mirrors store.validateProductStage without
// importing the unexported symbol. The closed enum is fixed by CD-0006 /
// CD-0017 D1 and surfaced in commandSpecs; importing it here keeps refusal
// diagnostics consistent with the rest of the operator surface.
func validateProductStageAllowed(maturity, audience string) bool {
	switch maturity {
	case "prototype", "alpha", "beta", "production", "deprecated":
	default:
		return false
	}
	switch audience {
	case "operator_only", "limited", "public":
	default:
		return false
	}
	return true
}

// importRequest is the strict JSON input to `predecessor import`. The shape is
// owned by this verb; the commandSpec enforces every required field at the
// boundary before the request reaches this struct.
type importRequest struct {
	SnapshotPath    string              `json:"snapshot_path"`
	Product         importProductDecl   `json:"product"`
	Projects        []importProjectDecl `json:"projects"`
	SelectChangeIDs []string            `json:"select_change_ids"`
	DryRun          bool                `json:"dry_run"`
}

// importProductDecl declares the Concord-side Product. The stage enum is
// re-validated here so the failure kind matches the rest of the operator
// surface; an invalid enum never reaches the store.
type importProductDecl struct {
	ProductID               string `json:"product_id"`
	DisplayName             string `json:"display_name"`
	StageMaturity           string `json:"stage_maturity"`
	StageAudienceCommitment string `json:"stage_audience_commitment"`
}

// importProjectDecl maps one snapshot project_id to its Concord identity.
type importProjectDecl struct {
	SnapshotProjectID string `json:"snapshot_project_id"`
	ProjectID         string `json:"project_id"`
	DisplayName       string `json:"display_name"`
	Role              string `json:"role"`
}

// importedWork reports one work item the import created or counted.
type importedWork struct {
	ChangeID         string   `json:"change_id"`
	WorkID           string   `json:"work_id"`
	ExternalRef      string   `json:"external_ref"`
	PredecessorPhase string   `json:"predecessor_phase"`
	CompletedGates   []string `json:"predecessor_completed_gates"`
}

// importReport is the bounded JSON report the verb emits on stdout. Totals and
// per-work-item fields are present on every successful or idempotent report;
// dry_run adds the flag without changing the shape.
type importReport struct {
	DryRun           bool           `json:"dry_run"`
	ProductsCreated  int            `json:"products_created"`
	ProjectsCreated  int            `json:"projects_created"`
	WorkImported     int            `json:"work_imported"`
	AlreadyImported  int            `json:"already_imported"`
	ImportedProducts []string       `json:"imported_products"`
	ImportedProjects []string       `json:"imported_projects"`
	Work             []importedWork `json:"work"`
}

// runPredecessorImport is the verb handler. The verb routes through the normal
// store-open path because every accepted command writes durable authority;
// predecessor inventory remains the only read-only operator verb.
func runPredecessorImport(raw []byte, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()

	var request importRequest
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-import", err.Error())
		return 1
	}
	if err := validateImportRequest(&request); err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-import", err.Error())
		return 1
	}

	snapshot, err := predecessor.Load(request.SnapshotPath)
	if err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-import", err.Error())
		return 1
	}

	resolved, err := resolveImportSelection(snapshot, &request)
	if err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-import", err.Error())
		return 1
	}

	report := importReport{
		DryRun:           request.DryRun,
		ImportedProducts: []string{},
		ImportedProjects: []string{},
		Work:             []importedWork{},
	}

	if request.DryRun {
		// dry_run reports what an import WOULD do without opening the store.
		// The pre-validation and selection-resolution steps above already
		// verify the request against the snapshot.
		return writeJSON(out, report, errOut)
	}

	if err := executePredecessorImport(ctx, s, &request, resolved, &report); err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-import", err.Error())
		return 1
	}
	if err := s.SyncDurable(ctx); err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-import", err.Error())
		return 1
	}
	return writeJSON(out, report, errOut)
}

// validateImportRequest enforces the structural rules on the request before the
// snapshot is loaded. Every error here is a typed diagnostic; the verb refuses
// before reading the snapshot so a malformed request never reaches the harvest
// contract.
func validateImportRequest(request *importRequest) error {
	if request.SnapshotPath == "" {
		return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
			Detail: "snapshot_path must be a non-empty path"}
	}
	if request.Product.ProductID == "" {
		return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
			Detail: "product.product_id must be a non-empty string"}
	}
	if request.Product.DisplayName == "" {
		return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
			Detail: "product.display_name must be a non-empty string"}
	}
	if !validateProductStageAllowed(request.Product.StageMaturity, request.Product.StageAudienceCommitment) {
		return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
			Detail: fmt.Sprintf("product stage values %q/%q are not accepted", request.Product.StageMaturity, request.Product.StageAudienceCommitment)}
	}
	if len(request.Projects) == 0 {
		return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
			Detail: "projects must contain at least one entry"}
	}
	primaryCount := 0
	projectIDs := make(map[string]string, len(request.Projects))
	for index, project := range request.Projects {
		if project.SnapshotProjectID == "" {
			return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail: fmt.Sprintf("projects[%d].snapshot_project_id must be a non-empty string", index)}
		}
		if project.ProjectID == "" {
			return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail: fmt.Sprintf("projects[%d].project_id must be a non-empty string", index)}
		}
		if project.DisplayName == "" {
			return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail: fmt.Sprintf("projects[%d].display_name must be a non-empty string", index)}
		}
		if project.Role != "primary" && project.Role != "secondary" {
			return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail: fmt.Sprintf("projects[%d].role %q is not accepted; use primary or secondary", index, project.Role)}
		}
		if other, exists := projectIDs[project.ProjectID]; exists {
			return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail: fmt.Sprintf("projects[%d].project_id %q duplicates projects[%q]", index, project.ProjectID, other)}
		}
		projectIDs[project.ProjectID] = project.SnapshotProjectID
		if project.Role == "primary" {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
			Detail: fmt.Sprintf("projects must declare exactly one primary role, found %d", primaryCount)}
	}
	return nil
}

// resolvedImport binds the request to the snapshot data the import will read.
// Pre-computing the selection here keeps the apply loop linear and lets the
// selection-refusal cases share one validation surface.
type resolvedImport struct {
	// snapshotProjects maps each declared project_id back to the snapshot
	// project entry it consumes.
	snapshotProjects map[string]*predecessor.Project
	// primaryProject is the declared project whose role is primary. It is the
	// one combined with the Product by the bootstrap.
	primaryProject *importProjectDecl
	// secondaryProjects is every declared project whose role is secondary.
	secondaryProjects []importProjectDecl
	// selectedChanges is the ordered list of snapshot active changes the
	// import will import. Order matches the request's select_change_ids.
	selectedChanges []selectedChange
}

// selectedChange carries the per-work immutable inputs the import uses to
// build the work.created and work.memberships_replaced events.
type selectedChange struct {
	ChangeID       string
	SnapshotPhase  string
	CompletedGates []string
	ProjectID      string // concord project_id the work is assigned to
}

// resolveImportSelection cross-checks every declared project_id and every
// selected change_id against the snapshot. The refusals here are the law
// working: an unknown snapshot project, an unknown change id, a change that
// belongs to an undeclared project, or a change whose phase is terminal all
// surface as typed diagnostics.
func resolveImportSelection(snapshot predecessor.Snapshot, request *importRequest) (*resolvedImport, error) {
	projectByID := make(map[string]*predecessor.Project, len(snapshot.Projects))
	for i := range snapshot.Projects {
		projectByID[snapshot.Projects[i].ProjectID] = &snapshot.Projects[i]
	}

	resolved := &resolvedImport{
		snapshotProjects: make(map[string]*predecessor.Project, len(request.Projects)),
	}
	for i := range request.Projects {
		project := request.Projects[i]
		snapshotProject, ok := projectByID[project.SnapshotProjectID]
		if !ok {
			return nil, &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail:         fmt.Sprintf("projects[%d].snapshot_project_id %q is not present in the snapshot", i, project.SnapshotProjectID),
				RecoveryAction: "declare every snapshot_project_id the snapshot enumerates"}
		}
		resolved.snapshotProjects[project.SnapshotProjectID] = snapshotProject
		if project.Role == "primary" {
			decl := project
			resolved.primaryProject = &decl
		} else {
			resolved.secondaryProjects = append(resolved.secondaryProjects, project)
		}
	}

	declaredSnapshots := make(map[string]bool, len(request.Projects))
	for _, project := range request.Projects {
		declaredSnapshots[project.SnapshotProjectID] = true
	}

	for _, changeID := range request.SelectChangeIDs {
		if changeID == "" {
			return nil, &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail: "select_change_ids must not contain empty strings"}
		}
		found := false
		for projectID, project := range projectByID {
			for _, change := range project.ActiveChanges {
				if change.ChangeID != changeID {
					continue
				}
				if !declaredSnapshots[projectID] {
					return nil, &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
						Detail:         fmt.Sprintf("select_change_ids entry %q belongs to snapshot project %q which is not declared in projects", changeID, projectID),
						RecoveryAction: "declare every snapshot project the selected changes belong to"}
				}
				if isTerminalActiveChangeStatus(change.Status) {
					return nil, &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
						Detail:         fmt.Sprintf("select_change_ids entry %q has terminal phase %q in snapshot project %q", changeID, change.Status, projectID),
						RecoveryAction: "select only active, non-terminal changes"}
				}
				declaredProjectID := ""
				for _, candidate := range request.Projects {
					if candidate.SnapshotProjectID == projectID {
						declaredProjectID = candidate.ProjectID
						break
					}
				}
				resolved.selectedChanges = append(resolved.selectedChanges, selectedChange{
					ChangeID:       change.ChangeID,
					SnapshotPhase:  change.Status,
					CompletedGates: append([]string(nil), change.CompletedGates...),
					ProjectID:      declaredProjectID,
				})
				found = true
			}
		}
		if !found {
			return nil, &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail:         fmt.Sprintf("select_change_ids entry %q is not an active change in any declared snapshot project", changeID),
				RecoveryAction: "select only change ids the snapshot enumerates as active"}
		}
	}
	return resolved, nil
}

// terminalActiveChangeStatuses is the closed set of snapshot statuses the
// design treats as terminal. The snapshot schema does not name them, so the
// mapping lives here, owned by the verb. The list matches the predecessor's
// gate lifecycle: once a change is released/closed it stops being an active
// change the import should move.
var terminalActiveChangeStatuses = map[string]bool{
	"released": true,
	"closed":   true,
	"archived": true,
}

func isTerminalActiveChangeStatus(status string) bool {
	return terminalActiveChangeStatuses[status]
}

// executePredecessorImport applies the validated request to the store. Each
// logical write uses a deterministic event id so a re-run hits
// KindDuplicateEvent and is reported as already_imported rather than as an
// error.
func executePredecessorImport(ctx context.Context, s *store.Store, request *importRequest, resolved *resolvedImport, report *importReport) error {
	productExisted, currentProductVersion, currentMembership, err := inspectExistingProductMembership(ctx, s, request.Product.ProductID)
	if err != nil {
		return err
	}
	if productExisted {
		if !productMembershipMatches(currentMembership, request.Projects) {
			return &store.Failure{Kind: store.KindInvalidOperation, Op: importFailureOp,
				Detail:         fmt.Sprintf("Product %q already exists with a different project membership; partial-Product import is refused", request.Product.ProductID),
				RecoveryAction: "re-run the import with the same product and project set, or accept the divergence as a separate decision"}
		}
	}

	createdProduct, err := writeProductAndProjects(ctx, s, request, resolved, currentProductVersion, productExisted)
	if err != nil {
		return err
	}
	if createdProduct {
		report.ProductsCreated = 1
		report.ImportedProducts = append(report.ImportedProducts, request.Product.ProductID)
		// The primary project is bootstrapped together with the Product, so
		// it counts as a created project on a fresh import.
		report.ProjectsCreated++
		report.ImportedProjects = append(report.ImportedProjects, resolved.primaryProject.ProjectID)
	} else {
		report.AlreadyImported += 2 // Product + primary project bootstrap is atomic; both are already-imported.
	}

	productVersion, err := currentProjectVersionAfterWrite(ctx, s, request.Product.ProductID, createdProduct, currentProductVersion)
	if err != nil {
		return err
	}
	createdProjectIDs, alreadyProjectCount, err := writeSecondaryProjects(ctx, s, request, resolved, productVersion)
	if err != nil {
		return err
	}
	report.ProjectsCreated += len(createdProjectIDs)
	report.ImportedProjects = append(report.ImportedProjects, createdProjectIDs...)
	report.AlreadyImported += alreadyProjectCount

	importedCount, alreadyImported, err := writeSelectedWork(ctx, s, request, resolved)
	if err != nil {
		return err
	}
	report.WorkImported = importedCount
	report.AlreadyImported += alreadyImported

	// Populate the work report regardless of import mode: a re-run still
	// reports the change → work_id mapping the operator needs to navigate.
	for _, change := range resolved.selectedChanges {
		gates := change.CompletedGates
		if gates == nil {
			gates = []string{}
		}
		report.Work = append(report.Work, importedWork{
			ChangeID:         change.ChangeID,
			WorkID:           importWorkID(change.ChangeID),
			ExternalRef:      "advance:" + change.ChangeID,
			PredecessorPhase: change.SnapshotPhase,
			CompletedGates:   gates,
		})
	}
	sort.SliceStable(report.Work, func(i, j int) bool {
		return report.Work[i].ChangeID < report.Work[j].ChangeID
	})
	return nil
}

// inspectExistingProductMembership probes the Product and its current
// product_projects membership. The returned `existed` is true when the
// Product row is already present; in that case `currentMembership` lists the
// concord project_ids in deterministic order and `currentProductVersion`
// carries the version the next membership event must expect.
func inspectExistingProductMembership(ctx context.Context, s *store.Store, productID string) (existed bool, version int64, membership []string, err error) {
	version, lookupErr := s.EntityVersion(ctx, store.SubjectProduct, productID)
	if lookupErr != nil {
		var failure *store.Failure
		if errors.As(lookupErr, &failure) && failure.Kind == store.KindProjectionNotFound {
			return false, 0, nil, nil
		}
		return false, 0, nil, lookupErr
	}
	membership, err = s.ProductMembership(ctx, productID)
	if err != nil {
		return false, 0, nil, wrapOperatorFailure(store.KindUnavailable, "cannot read existing Product membership", err)
	}
	return true, version, membership, nil
}

// productMembershipMatches reports whether the existing Product membership
// (sorted concord project_ids) matches the declared project set in the
// request, regardless of order. A declared role mismatch is also a refusal:
// if the existing Product carries the same concord project_ids but a
// different role, the import must not silently rewrite the authority.
func productMembershipMatches(existing []string, declared []importProjectDecl) bool {
	if len(existing) != len(declared) {
		return false
	}
	byID := make(map[string]struct{}, len(declared))
	for _, project := range declared {
		byID[project.ProjectID] = struct{}{}
	}
	for _, id := range existing {
		if _, ok := byID[id]; !ok {
			return false
		}
	}
	return true
}

// currentProjectVersionAfterWrite resolves the Product version after the
// Product+primary-project bootstrap. When the Product was freshly created,
// the bootstrap commits version 2 (create + membership). When the Product
// already existed, the version is unchanged.
func currentProjectVersionAfterWrite(ctx context.Context, s *store.Store, productID string, createdProduct bool, existingVersion int64) (int64, error) {
	if createdProduct {
		return 2, nil
	}
	version, lookupErr := s.EntityVersion(ctx, store.SubjectProduct, productID)
	if lookupErr != nil {
		return 0, lookupErr
	}
	return version, nil
}

// writeProductAndProjects appends the Product, the primary Project, and the
// primary membership as one transactional bootstrap. The deterministic
// event_ids mean a re-run after a successful first import hits
// KindDuplicateEvent on every event and the whole transaction is reported as
// already_imported.
//
// When the Product already exists with a matching membership, this step is a
// no-op: the loop returns createdProduct=false without writing anything.
func writeProductAndProjects(ctx context.Context, s *store.Store, request *importRequest, resolved *resolvedImport, currentProductVersion int64, productExisted bool) (created bool, err error) {
	if productExisted {
		return false, nil
	}
	primary := resolved.primaryProject
	now := s.Now()
	productPayload, _ := json.Marshal(map[string]string{
		"display_name":              request.Product.DisplayName,
		"stage_maturity":            request.Product.StageMaturity,
		"stage_audience_commitment": request.Product.StageAudienceCommitment,
	})
	projectPayload, _ := json.Marshal(map[string]any{"display_name": primary.DisplayName})
	membershipPayload, _ := json.Marshal(map[string]any{
		"product_id":        request.Product.ProductID,
		"project_id":        primary.ProjectID,
		"role":              primary.Role,
		"reason":            "predecessor import",
		"expected_version":  1,
		"resulting_version": 2,
	})
	operation := store.Operation{
		Events: []store.Event{
			{
				EventID:        importEventID("product", request.Product.ProductID),
				Kind:           "product.created",
				SubjectType:    store.SubjectProduct,
				SubjectID:      request.Product.ProductID,
				Actor:          importOperatorActor,
				OccurredAt:     now,
				PayloadVersion: 1,
				Payload:        productPayload,
			},
			{
				EventID:        importEventID("project", primary.ProjectID),
				Kind:           "project.created",
				SubjectType:    store.SubjectProject,
				SubjectID:      primary.ProjectID,
				Actor:          importOperatorActor,
				OccurredAt:     now,
				PayloadVersion: 1,
				Payload:        projectPayload,
			},
			{
				EventID:        importMembershipEventID(request.Product.ProductID, primary.ProjectID),
				Kind:           "product_project.added",
				SubjectType:    store.SubjectProduct,
				SubjectID:      request.Product.ProductID,
				Actor:          importOperatorActor,
				OccurredAt:     now,
				PayloadVersion: 1,
				Payload:        membershipPayload,
			},
		},
		ExpectedVersions: map[store.SubjectRef]int64{
			store.VersionRef(store.SubjectProduct, request.Product.ProductID): 0,
			store.VersionRef(store.SubjectProject, primary.ProjectID):         0,
		},
	}
	txErr := s.Transact(ctx, func(tx *store.Transaction) error {
		_, applyErr := store.ApplyOperationTx(ctx, tx, operation)
		return applyErr
	})
	if txErr != nil {
		// The deterministic event ids mean a re-run after a successful first
		// import hits KindDuplicateEvent on every event. That whole-tx
		// rejection is the idempotency signal, not an error.
		if isDuplicateEvent(txErr) {
			return false, nil
		}
		return false, txErr
	}
	_ = currentProductVersion // referenced for symmetry with the secondary-project path
	return true, nil
}

// writeSecondaryProjects adds every secondary Project to the Product with the
// expected version derived from the prior step. Each project runs as its own
// ApplyOperation so a single failure does not roll back the others.
//
// Pre-checks the Project row: a re-run lands here when the Product was
// already present on a prior import, so the membership-comparison check
// already accepted the existing set. Treating the existing project as
// already_imported here keeps the version chain consistent — a fresh write
// would otherwise push the Product version higher than the prior import.
func writeSecondaryProjects(ctx context.Context, s *store.Store, request *importRequest, resolved *resolvedImport, startingVersion int64) (created []string, alreadyImported int, err error) {
	created = []string{}
	alreadyImported = 0
	currentVersion := startingVersion
	for _, project := range resolved.secondaryProjects {
		if _, lookupErr := s.EntityVersion(ctx, store.SubjectProject, project.ProjectID); lookupErr == nil {
			// Project already exists. The membership it carries has already
			// been validated as matching the request, so count it as
			// already_imported and leave the Product version chain untouched.
			alreadyImported++
			continue
		} else {
			var failure *store.Failure
			if !(errors.As(lookupErr, &failure) && failure.Kind == store.KindProjectionNotFound) {
				return nil, 0, lookupErr
			}
		}
		now := s.Now()
		projectPayload, _ := json.Marshal(map[string]any{"display_name": project.DisplayName})
		membershipPayload, _ := json.Marshal(map[string]any{
			"product_id":        request.Product.ProductID,
			"project_id":        project.ProjectID,
			"role":              project.Role,
			"reason":            "predecessor import",
			"expected_version":  currentVersion,
			"resulting_version": currentVersion + 1,
		})
		operation := store.Operation{
			Events: []store.Event{
				{
					EventID:        importEventID("project", project.ProjectID),
					Kind:           "project.created",
					SubjectType:    store.SubjectProject,
					SubjectID:      project.ProjectID,
					Actor:          importOperatorActor,
					OccurredAt:     now,
					PayloadVersion: 1,
					Payload:        projectPayload,
				},
				{
					EventID:        importMembershipEventID(request.Product.ProductID, project.ProjectID),
					Kind:           "product_project.added",
					SubjectType:    store.SubjectProduct,
					SubjectID:      request.Product.ProductID,
					Actor:          importOperatorActor,
					OccurredAt:     now,
					PayloadVersion: 1,
					Payload:        membershipPayload,
				},
			},
			ExpectedVersions: map[store.SubjectRef]int64{
				store.VersionRef(store.SubjectProduct, request.Product.ProductID): currentVersion,
				store.VersionRef(store.SubjectProject, project.ProjectID):         0,
			},
		}
		txErr := s.Transact(ctx, func(tx *store.Transaction) error {
			_, applyErr := store.ApplyOperationTx(ctx, tx, operation)
			return applyErr
		})
		if txErr != nil {
			if isDuplicateEvent(txErr) {
				alreadyImported++
				continue
			}
			return nil, 0, txErr
		}
		created = append(created, project.ProjectID)
		currentVersion++
	}
	return created, alreadyImported, nil
}

// writeSelectedWork appends one work.created + work.memberships_replaced pair
// per selected change. Each pair uses deterministic event ids derived from the
// predecessor change_id so a re-run is idempotent.
func writeSelectedWork(ctx context.Context, s *store.Store, request *importRequest, resolved *resolvedImport) (imported, already int, err error) {
	_ = request
	for _, change := range resolved.selectedChanges {
		workID := importWorkID(change.ChangeID)
		// Pre-check: an already-imported work item means the deterministic
		// event id is already durable. Skip the redundant write so we do not
		// pay for the whole-tx duplicate path on a hot re-run.
		exists, eventErr := s.EventIDExists(ctx, importEventID("work", change.ChangeID))
		if eventErr != nil {
			return 0, 0, wrapOperatorFailure(store.KindUnavailable, "cannot probe existing import-advance event", eventErr)
		}
		if exists {
			already++
			continue
		}
		valueStatement := fmt.Sprintf("Migrated from Advance predecessor change %s (phase %s, %s). Re-contract before execution.", change.ChangeID, change.SnapshotPhase, importOperatorActor)
		now := s.Now()
		workPayload, _ := json.Marshal(map[string]any{
			"work_kind":       "task",
			"title":           importWorkTitle(snapshotChangeTitle(resolved, change.ChangeID)),
			"value_statement": valueStatement,
			"priority":        int64(3),
			"urgency":         "standard",
			"tags":            []string{"predecessor-migrated"},
			"external_ref":    "advance:" + change.ChangeID,
		})
		membershipPayload, _ := json.Marshal(map[string]any{
			"memberships": []map[string]any{
				{"project_id": change.ProjectID, "role": "primary"},
			},
			"expected_version":  1,
			"resulting_version": 2,
		})
		operation := store.Operation{
			Events: []store.Event{
				{
					EventID:        importEventID("work", change.ChangeID),
					Kind:           "work.created",
					SubjectType:    store.SubjectWorkItem,
					SubjectID:      workID,
					Actor:          importOperatorActor,
					OccurredAt:     now,
					PayloadVersion: 2,
					Payload:        workPayload,
				},
				{
					EventID:        importMembershipEventIDForWork(workID),
					Kind:           "work.memberships_replaced",
					SubjectType:    store.SubjectWorkItem,
					SubjectID:      workID,
					Actor:          importOperatorActor,
					OccurredAt:     now,
					PayloadVersion: 1,
					Payload:        membershipPayload,
				},
			},
			ExpectedVersions: map[store.SubjectRef]int64{
				store.VersionRef(store.SubjectWorkItem, workID): 0,
			},
		}
		txErr := s.Transact(ctx, func(tx *store.Transaction) error {
			_, applyErr := store.ApplyOperationTx(ctx, tx, operation)
			return applyErr
		})
		if txErr != nil {
			if isDuplicateEvent(txErr) {
				already++
				continue
			}
			return 0, 0, txErr
		}
		imported++
	}
	return imported, already, nil
}

// snapshotChangeTitle returns the snapshot change summary for the resolved
// change, used as the work item's title so an imported work item carries the
// predecessor's one-line summary forward.
func snapshotChangeTitle(resolved *resolvedImport, changeID string) string {
	for _, project := range resolved.snapshotProjects {
		for _, change := range project.ActiveChanges {
			if change.ChangeID == changeID {
				return change.Summary
			}
		}
	}
	return changeID
}

// importEventID builds the deterministic event id the import uses for
// non-membership bootstrap events. The form matches the design comment:
// `import-advance-<subject>` where subject is `product-<id>`, `project-<id>`,
// or `work-<change_id>`.
func importEventID(kind, subject string) string {
	return "import-advance-" + kind + "-" + subject
}

// importMembershipEventID builds the deterministic event id for the
// product_project.added events the import appends.
func importMembershipEventID(productID, projectID string) string {
	return "import-advance-membership-" + productID + "-" + projectID
}

// importMembershipEventIDForWork builds the deterministic event id for the
// work.memberships_replaced event the import appends alongside work.created.
func importMembershipEventIDForWork(workID string) string {
	return "import-advance-work-membership-" + workID
}

// importWorkID returns the deterministic Concord work id the import mints
// from a predecessor change id. The shape is bounded and prefix-namespaced so
// an operator can identify migrated work at a glance.
func importWorkID(changeID string) string {
	return "import-advance-work-" + changeID
}

// importWorkTitle returns the title stored on a migrated work item. When the
// snapshot summary is empty the title falls back to a stable label.
func importWorkTitle(summary string) string {
	if summary == "" {
		return "Migrated Advance change"
	}
	return summary
}

// isDuplicateEvent reports whether the failure returned by an apply call is
// the expected whole-transaction duplicate outcome from a re-run. The
// deterministic event ids mean every event in the operation hits
// KindDuplicateEvent, and applyOperationTx fails the whole transaction on
// the first one.
func isDuplicateEvent(err error) bool {
	var failure *store.Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Kind == store.KindDuplicateEvent
}

// wrapOperatorFailure builds a typed diagnostic with the verb's stable Op so
// CLI failures trace back to the import surface. Underlying causes are
// preserved on Failure.Err.
func wrapOperatorFailure(kind store.FailureKind, detail string, cause error) *store.Failure {
	failure := &store.Failure{Kind: kind, Op: importFailureOp, Detail: detail, RetrySafe: false, RecoveryAction: "rebuild the database from its durable log"}
	failure.Err = cause
	return failure
}
