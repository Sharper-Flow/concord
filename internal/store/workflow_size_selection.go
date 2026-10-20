package store

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
)

// The reasons small work keeps a family's full composition (CD-0229 D5).
// None of them changes the size or earns proof credit for a check.
const (
	// A footprint check has no executing suite: no ordinary verify argv
	// proves it, or the Project's CI runs its package only as shards, or the
	// size record read no shard declaration for its go_package check.
	WorkflowRetainedUnexecutableChecks = "unexecutable_checks"
	// The size record predates the complete check set, so nothing shows
	// which checks the work owes.
	WorkflowRetainedIncompleteInventory = "incomplete_check_inventory"
	// A same-family carry-forward keeps the instance's full composition.
	WorkflowRetainedExistingFullInstance = "existing_full_instance"
)

// workflowSizeSelection records the effective size a workflow selection read
// and, when small work keeps the full composition, why.
type workflowSizeSelection struct {
	Size      WorkSize               `json:"size,omitempty"`
	Retention *workflowFullRetention `json:"full_retention,omitempty"`
}

type workflowFullRetention struct {
	Reason string           `json:"reason"`
	Checks []FootprintCheck `json:"checks,omitempty"`
}

// workflowSelection is one derived choice: the composition size it pins
// (empty is the full composition) and the record the pin carries.
type workflowSelection struct {
	Variant WorkSize
	Record  workflowSizeSelection
}

// deriveWorkflowSelectionTx derives the composition a work item's recorded
// effective size selects in family ref. carried is the composition size of a
// same-family instance the selection carries forward, or nil for a fresh
// selection. Capture, revision, resize, and the fold all call it, so the pin
// and its check cannot disagree.
func deriveWorkflowSelectionTx(ctx context.Context, q queryer, workID, ref string, carried *WorkSize) (workflowSelection, error) {
	decision, sized, err := readEffectiveWorkSize(ctx, q, workID)
	if err != nil || !sized {
		return workflowSelection{}, err
	}
	out := workflowSelection{Record: workflowSizeSelection{Size: decision.Effective}}
	if decision.Effective != WorkSizeSmall || !workflowFamilyServesSize(ref, WorkSizeSmall) {
		return out, nil
	}
	switch {
	case carried != nil && *carried == "":
		out.Record.Retention = &workflowFullRetention{Reason: WorkflowRetainedExistingFullInstance}
	case carried != nil:
		out.Variant = *carried
	default:
		out.Record.Retention = workflowFullRetentionFor(decision.Model.Footprint)
		if out.Record.Retention == nil {
			out.Variant = WorkSizeSmall
		}
	}
	return out, nil
}

// workflowFullRetentionFor names why a small footprint cannot take the small
// composition, or returns nil when every check it owes has an executing suite.
func workflowFullRetentionFor(footprint WorkSizeFootprint) *workflowFullRetention {
	checks, complete := workflowFootprintChecks(footprint)
	if !complete {
		return &workflowFullRetention{Reason: WorkflowRetainedIncompleteInventory}
	}
	var retained []FootprintCheck
	for _, check := range checks {
		_, executable := workflowFootprintCheckCommand(check)
		if pkg, isPackage := workflowPackageCheckPackage(check.Value); executable && check.Kind == "go_package" && isPackage {
			executable = footprint.ShardsRead && !slices.Contains(footprint.ShardedPackages, pkg)
		}
		if !executable {
			retained = append(retained, check)
		}
	}
	if len(retained) == 0 {
		return nil
	}
	return &workflowFullRetention{Reason: WorkflowRetainedUnexecutableChecks, Checks: retained}
}

func workflowFamilyServesSize(ref string, size WorkSize) bool {
	for _, composition := range builtinWorkflowCompositions() {
		if composition.Ref == ref && composition.Size == size {
			return true
		}
	}
	return false
}

// workflowCarriedVariantTx reads the composition size a same-family pin
// stays in: the instance's own pin when it names ref, at any version, since
// an instance never changes composition in place. A pin of another family,
// or none, starts a fresh selection.
func workflowCarriedVariantTx(ctx context.Context, q queryer, workID, ref string) (*WorkSize, error) {
	var currentRef string
	var currentVersion int64
	err := q.QueryRowContext(ctx, `SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentRef, &currentVersion)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_select", "cannot read the workflow pin", true, "retry once the database is readable", err)
	}
	if currentRef != ref {
		return nil, nil
	}
	size := workflowCompositionSize(ref, currentVersion)
	return &size, nil
}

// SelectWorkflowDefinitionTx resolves the definition a capture, revision,
// or resize pins in family ref: the latest version of the composition the
// work item's recorded size selects (CD-0229 D5). An instance already
// pinned in ref carries forward within its own composition.
func SelectWorkflowDefinitionTx(ctx context.Context, transaction *Transaction, ref, workID string) (RegisteredDefinition, error) {
	tx, err := transactionSQL(transaction, "workflow_select")
	if err != nil {
		return RegisteredDefinition{}, err
	}
	return selectWorkflowDefinitionTx(ctx, tx, ref, workID)
}

func selectWorkflowDefinitionTx(ctx context.Context, q queryer, ref, workID string) (RegisteredDefinition, error) {
	if _, err := LiveWorkflowDefinitionForRef(ref); err != nil {
		return RegisteredDefinition{}, err
	}
	carried, err := workflowCarriedVariantTx(ctx, q, workID, ref)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	selection, err := deriveWorkflowSelectionTx(ctx, q, workID, ref, carried)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	return builtinWorkflowDefinitionForSize(ref, selection.Variant)
}

// workflowPinSelectionTx derives the record a definition_selected event
// carries for ref at version, and refuses a version that is not the
// composition the recorded size selects.
func workflowPinSelectionTx(ctx context.Context, q queryer, workID, ref string, version int64, op string) (*workflowSizeSelection, error) {
	carried, err := workflowCarriedVariantTx(ctx, q, workID, ref)
	if err != nil {
		return nil, err
	}
	selection, err := deriveWorkflowSelectionTx(ctx, q, workID, ref, carried)
	if err != nil {
		return nil, err
	}
	if selection.Variant != workflowCompositionSize(ref, version) {
		return nil, workflowVariantFailure(op, ref, version, selection.Variant)
	}
	return &selection.Record, nil
}

func workflowVariantFailure(op, ref string, version int64, variant WorkSize) error {
	want := "full"
	if variant != "" {
		want = string(variant)
	}
	return newFailure(KindInvalidOperation, op, workflowRefExcerpt(ref)+" v"+strconv.FormatInt(version, 10)+" is not the "+want+" composition the work item's recorded size selects", false, "resolve the definition through the size selector")
}
