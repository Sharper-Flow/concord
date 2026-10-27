package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
)

// Admission consumes the union of the recorded and accumulated-diff
// footprints. Missing scope is unknown, not empty. Tooling binds each check
// to a pinned recipe case; only qualified native execution proves that case.
// Source resolution runs outside the transaction. This gate uses SQL only,
// after the existing refinement, integration, and Review checks.

// workflowRequiredAnchorProof is one typed required-proof entry: the
// footprint anchor, the declared coverage binding it must join, and the scope
// side the requirement came from.
type workflowRequiredAnchorProof struct {
	Anchor   FootprintCheck
	Coverage *ProjectResolvedCoverage
	Source   string
}

// workflowFootprintProofRuns derives the candidate and its qualifying
// verification runs through the same owners the delivery admission uses, so
// the completion guard answers from one derivation. SQL only.
func workflowFootprintProofRuns(ctx context.Context, q queryer, workID, accepting string) (*WorkerReviewCandidate, []workflowVerificationRun, error) {
	candidate, err := workflowVerificationCandidate(ctx, q, workID, accepting)
	if err != nil || candidate == nil {
		return nil, nil, err
	}
	refs, err := workflowCandidateVerificationRefs(ctx, q, workID, accepting)
	if err != nil {
		return candidate, nil, err
	}
	runs := make([]workflowVerificationRun, 0, len(refs))
	for _, ref := range refs {
		producer, command, project, why, err := workflowCandidateVerificationRun(ctx, q, workID, ref, accepting)
		if err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
				return candidate, nil, err
			}
		}
		runs = append(runs, workflowVerificationRun{Command: command, Scope: project, Disqualifier: why, Failure: err, Producer: producer})
	}
	return candidate, runs, nil
}

// workflowFootprintProofRefusal is the one typed required-proof derivation
// every delivering route and completion consumes. A nil refusal with a nil
// error reports no applicable check; the callers above it keep their existing
// refine argv, integration, and Review gates.
func workflowFootprintProofRefusal(ctx context.Context, q queryer, workID string, tooling *ProjectToolingManifest, candidate *WorkerReviewCandidate, runs []workflowVerificationRun, resolved *ResolvedWorkflowFootprint, subject string) (*Failure, error) {
	required, refusal, err := workflowFootprintRequiredAnchorsTx(ctx, q, workID, tooling, candidate, resolved, subject)
	if err != nil || refusal != nil {
		return refusal, err
	}
	if len(required) == 0 {
		return nil, nil
	}
	contractVersion, contractErr := activeWorkflowContractVersion(ctx, q, workID, subject)
	if contractErr == sql.ErrNoRows {
		return workflowFootprintBindingFailure(required[0], "the work has no approved contract to bind the proof's provenance", ""), nil
	}
	if contractErr != nil {
		return nil, contractErr
	}
	for _, requirement := range required {
		var deficient string
		binding := requirement.Coverage
		if binding == nil {
			return workflowFootprintBindingFailure(requirement, "", ProjectToolingDeclaredText(tooling)), nil
		}
		qualified := false
		for _, run := range runs {
			if run.Failure != nil || run.Disqualifier != "" || run.Producer == nil {
				continue
			}
			if err := validateNativeOracleProducerTx(ctx, q, workID, run.Producer, "pass"); err != nil {
				// The producer validator answers a receipt that is not
				// qualified with a typed missing-evidence failure; only an
				// unavailable read propagates as an error.
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind == KindUnavailable {
					return nil, err
				}
				deficient = "the retained run " + workflowRefExcerpt(run.Producer.OperationRef) + " carries no qualified native execute receipt"
				continue
			}
			why := nativeOracleReceiptAnchorDeficiency(workID, run.Producer.Oracle, candidate, contractVersion, binding)
			if why != "" {
				deficient = "the retained run " + workflowRefExcerpt(run.Producer.OperationRef) + " " + why
				continue
			}
			// The receipt joins the exact pinned recipe, case, witnesses,
			// files, candidate, and contract provenance this anchor's
			// coverage declares: its proof scope covers exactly this
			// anchor's mapped obligation and nothing else claims it.
			qualified = true
			break
		}
		if !qualified {
			if deficient == "" {
				deficient = "no retained run carries the anchor's pinned native case"
			}
			return workflowFootprintExecutionFailure(requirement, deficient), nil
		}
	}
	return nil, nil
}

// workflowFootprintRequiredAnchorsTx joins the frozen scope with all current
// obligations resolved from the accumulated diff. Missing scope is unknown,
// not an empty check set. Callers classify non-delivering definitions before
// entering this gate, after their existing refinement and Review checks.
func workflowFootprintRequiredAnchorsTx(ctx context.Context, q queryer, workID string, tooling *ProjectToolingManifest, candidate *WorkerReviewCandidate, resolved *ResolvedWorkflowFootprint, subject string) ([]workflowRequiredAnchorProof, *Failure, error) {
	record, sized, err := readWorkSize(ctx, q, workID)
	if err != nil {
		return nil, nil, err
	}
	if !sized || !record.Footprint.ChecksComplete {
		return nil, workflowFootprintResolutionFailure(candidate, "required recorded footprint is missing or has incomplete checks"), nil
	}
	if candidate == nil {
		return nil, workflowFootprintResolutionFailure(candidate, "required candidate is missing"), nil
	}
	if resolved == nil {
		return nil, workflowFootprintResolutionFailure(candidate, "required accumulated diff footprint was not resolved"), nil
	}
	if err := resolved.revalidate(ctx, q, workID, *candidate); err != nil {
		var failure *Failure
		if failureAs(err, &failure) {
			return nil, failure, nil
		}
		return nil, nil, err
	}
	required := make([]workflowRequiredAnchorProof, 0, 8)
	add := func(laws []FootprintLaw, packages []FootprintPackageCheck, source string) {
		for _, law := range laws {
			for _, check := range law.Checks {
				required = append(required, workflowRequiredAnchorProof{Anchor: check, Source: source + law.LawID})
			}
		}
		for _, check := range packages {
			required = append(required, workflowRequiredAnchorProof{Anchor: FootprintCheck{Kind: check.Kind, Value: check.Value}, Source: source + check.Package})
		}
	}
	add(record.Footprint.Laws, record.Footprint.PackageChecks, "recorded:")
	for _, actual := range resolved.footprints {
		add(actual.result.Laws, actual.result.PackageChecks, "actual:")
	}
	sort.SliceStable(required, func(i, j int) bool {
		if required[i].Anchor.Kind != required[j].Anchor.Kind {
			return required[i].Anchor.Kind < required[j].Anchor.Kind
		}
		return required[i].Anchor.Value < required[j].Anchor.Value
	})
	required = slices.CompactFunc(required, func(left, right workflowRequiredAnchorProof) bool {
		return left.Anchor == right.Anchor
	})
	if toolingHasCoverage(tooling) {
		for index := range required {
			for i := range tooling.ResolvedCoverage {
				if tooling.ResolvedCoverage[i].Anchor == required[index].Anchor {
					binding := tooling.ResolvedCoverage[i]
					required[index].Coverage = &binding
					break
				}
			}
		}
	}
	return required, nil, nil
}

func toolingHasCoverage(tooling *ProjectToolingManifest) bool {
	if tooling == nil {
		return false
	}
	for _, tool := range tooling.Declared {
		if len(tool.Coverage) != 0 {
			return true
		}
	}
	return false
}

// nativeOracleReceiptAnchorDeficiency names why one qualified native receipt
// does not prove one anchor's declared binding, or "" when it joins it.
func nativeOracleReceiptAnchorDeficiency(workID string, o *NativeOracleResult, candidate *WorkerReviewCandidate, contractVersion int64, binding *ProjectResolvedCoverage) string {
	if o == nil {
		return "carries no native result"
	}
	if o.RecipeSource != binding.RecipeSource || o.ManifestDigest != binding.RecipeDigest {
		return "pinned recipe origin or bytes differ from the anchor's declared coverage"
	}
	witnesses := binding.Recipe.Witnesses
	switch binding.Recipe.Kind {
	case "go_top_level_v1":
		if o.RecipeKind != "" || len(o.CaseToWitnessMap) != 0 || len(o.SelectedWitnessIDs) != 0 {
			return "declares a witness-id selection on a top-level Go case"
		}
		if !slices.Equal(o.CaseToTestMap[binding.CaseID], witnesses) {
			return "binds the anchor's case to different witnesses than the coverage declares"
		}
		for _, witness := range witnesses {
			if !slices.Contains(o.ObservedTestNames, witness) {
				return "did not observe required witness " + witness + " executing and passing"
			}
		}
	case nativeGoPackageKind, nativeTargetKind:
		if o.RecipeKind != binding.Recipe.Kind {
			return "executed a different recipe family than the anchor's coverage declares"
		}
		if !slices.Equal(o.CaseToWitnessMap[binding.CaseID], witnesses) {
			return "binds the anchor's case to different witnesses than the coverage declares"
		}
		for _, witness := range witnesses {
			if !slices.Contains(o.ObservedWitnessIDs, witness) {
				return "did not observe required witness " + witness + " executing and passing"
			}
		}
	default:
		return "declares an unsupported recipe family"
	}
	if !workflowFootprintHarnessMatches(binding, o) {
		return "pinned harness inventory differs from the declared coverage files"
	}
	if o.WorkID != workID || o.SubjectCommit != candidate.Commit || o.ProjectID != candidate.ProjectID {
		return "names another candidate or Project"
	}
	if o.ContractVersion != contractVersion {
		return fmt.Sprintf("was recorded under contract version %d, not the active %d", o.ContractVersion, contractVersion)
	}
	return ""
}

// Reconstruct the native owner's inventory representation from the complete
// pinned coverage inputs. Large inventories use the producer's existing
// sorted-file JSON digest; receipt comparison stays with its native owner.
func workflowFootprintHarnessMatches(binding *ProjectResolvedCoverage, o *NativeOracleResult) bool {
	files := slices.Clone(binding.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	expected := nativeOraclePlan{Files: files}
	if binding.Recipe.Kind == nativeTargetKind {
		expected.Target = &nativeTargetOraclePlan{}
	} else {
		expected.Manifest.Kind = binding.Recipe.Kind
	}
	if len(files) > 32 {
		expected.HarnessInputCount = len(files)
		expected.HarnessInputDigest = nativeDigest([]byte(workflowJSON(files)))
		expected.Files = nil
	}
	return nativeGoHarnessReceiptMatches(expected, o)
}

func workflowFootprintResolutionFailure(candidate *WorkerReviewCandidate, why string) *Failure {
	commit := "<missing>"
	if candidate != nil {
		commit = workflowRefExcerpt("commit:" + candidate.Commit)
	}
	return newFailure(KindMissingEvidence, "workflow_action",
		"the applicable footprint cannot resolve on the current candidate "+commit+": "+why,
		false, "re-derive the work's footprint through revise_intent size inputs, then bind and execute the checks it names before asserting delivery")
}

func workflowFootprintBindingFailure(requirement workflowRequiredAnchorProof, prefix, suffix string) *Failure {
	detail := prefix
	if detail != "" {
		detail += ": "
	}
	detail += "required footprint check " + requirement.Anchor.Kind + " " + requirement.Anchor.Value + " (from " + requirement.Source + ") has no declared tooling coverage binding and cannot pass"
	if suffix != "" {
		detail += " " + suffix
	}
	return newFailure(KindMissingEvidence, "workflow_action", detail,
		false, "record a verification job whose approved predicate requires this check and execute its pinned native control, or revise the approved contract when no predicate requires it")
}

func workflowFootprintExecutionFailure(requirement workflowRequiredAnchorProof, why string) *Failure {
	return newFailure(KindMissingEvidence, "workflow_action",
		"required footprint check "+requirement.Anchor.Kind+" "+requirement.Anchor.Value+" (from "+requirement.Source+") has no qualified native execution proof on the current integrated candidate: "+why,
		false, "execute the pinned native control for the anchor's declared recipe case on the current integrated candidate and bind the run before asserting delivery")
}
