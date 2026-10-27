package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// ResolvedWorkflowFootprint is an internal, sealed source read. It is never
// decoded from a tool payload. Failed resolution travels to the owning gate
// instead of preempting refinement, Review, or action admission refusals.
type ResolvedWorkflowFootprint struct {
	snapshot   workflowFootprintSnapshot
	accepting  string
	footprints []workflowResolvedFootprint
	err        error
}

type workflowFootprintSnapshot struct {
	work, product, repo, claim, base, size, definition string
	version, contract, authorization                   int64
	candidate                                          WorkerReviewCandidate
}

type workflowResolvedFootprint struct {
	prepared preparedDomainFootprint
	result   DomainFootprintResult
}

// Non-delivering definitions (including research) do not owe engineering
// footprint proof. Missing scope on a delivering definition is never an
// exemption. A local accept without a delivery assertion is not delivery.
func workflowFootprintActionRequired(definition WorkflowDefinition, step, action string, payload json.RawMessage) bool {
	if !workflowDefinitionProducesDelivery(definition) {
		return false
	}
	switch action {
	case "record_delivery":
		return true
	case "accept_worker_result":
		return workflowAcceptDeliveryAdmissionActive(definition, step) && preflightAcceptAssertsDelivery(payload)
	case "complete":
		return true
	}
	return false
}

// ResolveWorkflowFootprint runs source reads before either preflight or the
// mutation opens its transaction. Both callers use this resolver and the same
// internal input. It creates no evidence and executes no checks.
func ResolveWorkflowFootprint(ctx context.Context, s *Store, registry DefinitionRegistry, work, action string, payload json.RawMessage) *ResolvedWorkflowFootprint {
	return resolveWorkflowFootprint(ctx, s, registry, work, action, payload, CommittedReadGitRunner{})
}

func resolveWorkflowFootprint(ctx context.Context, s *Store, registry DefinitionRegistry, work, action string, payload json.RawMessage, runner GitRunner) *ResolvedWorkflowFootprint {
	if action != "record_delivery" && action != "accept_worker_result" && action != "complete" {
		return nil
	}
	r := &ResolvedWorkflowFootprint{}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, work)
	if err != nil {
		r.err = err
		return r
	}
	step, err := workflowDiscoveryCurrentStep(ctx, s, work)
	if err != nil {
		r.err = err
		return r
	}
	if !workflowFootprintActionRequired(entry.Definition, step, action, payload) {
		return nil
	}
	if action == "accept_worker_result" {
		fields, err := workflowActionObject(payload)
		if err != nil {
			r.err = err
			return r
		}
		r.accepting = workflowFieldStringDefault(fields, "attempt_id", "")
	}
	ctx, cancel := context.WithTimeout(ctx, workerDispatchReadingResolutionDeadline)
	defer cancel()
	r.err = s.readSnapshot(ctx, func(q queryer) error {
		var err error
		r.snapshot, err = sampleWorkflowFootprint(ctx, q, work, r.accepting)
		return err
	})
	if r.err != nil {
		return r
	}
	// The terminal observation owner supplies the read-only full delta, but
	// its base comes from the review diff owner's first editing authorization,
	// never from the latest attempt-relative completion.
	observed, err := observeWorkerDelivery(ctx, runner, r.snapshot.repo, r.snapshot.base)
	if err != nil {
		r.err = err
		return r
	}
	if observed.State != "clean" || observed.Commit != r.snapshot.candidate.Commit || observed.Tree != r.snapshot.candidate.Tree || observed.ChangedPaths == nil {
		r.err = fmt.Errorf("accumulated diff requires the exact clean integrated candidate and a complete changed-path set")
		return r
	}
	var sized WorkSizeRecord
	if err := json.Unmarshal([]byte(r.snapshot.size), &sized); err != nil {
		r.err = err
		return r
	}
	paths := append([]string{}, observed.ChangedPaths...)
	for _, path := range sized.Footprint.Paths {
		paths = append(paths, path.Path)
	}
	paths = uniqueSorted(paths)
	for _, path := range paths {
		if !validFootprintPath(path) {
			r.err = fmt.Errorf("accumulated diff contains an unresolved path %q", path)
			return r
		}
	}
	// Read every law, not a bounded page. Each batch stays within the existing
	// footprint selector limit; no path or obligation is truncated.
	for start := 0; start < len(paths) || start == 0; start += footprintMaxPaths {
		end := min(start+footprintMaxPaths, len(paths))
		prepared, err := s.prepareDomainFootprint(ctx, DomainFootprintRequest{Product: r.snapshot.product, Paths: paths[start:end], Behavior: sized.Footprint.Behavior})
		if err != nil {
			r.err = err
			return r
		}
		r.footprints = append(r.footprints, workflowResolvedFootprint{prepared: prepared})
		if end == len(paths) {
			break
		}
	}
	r.err = s.readSnapshot(ctx, func(q queryer) error {
		for i := range r.footprints {
			f := &r.footprints[i]
			var err error
			f.result, err = f.prepared.ownershipTx(ctx, q)
			if err != nil {
				return err
			}
			if err := workflowFootprintComplete(f.result); err != nil {
				return err
			}
			if err := workflowFootprintIndexComplete(ctx, q, f.prepared.home); err != nil {
				return err
			}
		}
		return r.revalidate(ctx, q, work, r.snapshot.candidate)
	})
	if r.err != nil {
		return r
	}
	after, err := observeWorkerDelivery(ctx, runner, r.snapshot.repo, r.snapshot.base)
	if err != nil {
		r.err = err
	} else if !reflect.DeepEqual(observed, after) {
		r.err = fmt.Errorf("candidate changed while resolving the accumulated footprint")
	}
	return r
}

func sampleWorkflowFootprint(ctx context.Context, q queryer, work, accepting string) (workflowFootprintSnapshot, error) {
	var out workflowFootprintSnapshot
	out.work = work
	record, found, err := readWorkSize(ctx, q, work)
	if err != nil {
		return out, err
	}
	if !found || !record.Footprint.ChecksComplete || record.Footprint.KnowledgeCommit == "" || record.Footprint.RegistryContentHash == "" || len(record.Footprint.Paths) == 0 && record.Footprint.Behavior == "" {
		return out, fmt.Errorf("required recorded footprint is missing or incomplete")
	}
	out.size = workflowJSON(record)
	candidate, err := workflowVerificationCandidate(ctx, q, work, accepting)
	if err != nil {
		return out, err
	}
	if candidate == nil || candidate.ProjectID == "" || !worktreeSHAPattern.MatchString(candidate.Commit) || !worktreeSHAPattern.MatchString(candidate.Tree) {
		return out, fmt.Errorf("required integrated candidate identity is missing or incomplete")
	}
	out.candidate = *candidate
	out.contract, err = activeWorkflowContractVersion(ctx, q, work, "workflow_footprint")
	if err != nil {
		return out, err
	}
	if err := q.QueryRowContext(ctx, `SELECT w.version,i.definition_digest,COALESCE((SELECT MAX(seq) FROM worker_authorizations WHERE work_id=w.id),0) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, work).Scan(&out.version, &out.definition, &out.authorization); err != nil {
		return out, err
	}
	entry, err := activeWorktreeEntryForProject(ctx, q, "workflow_footprint", work, candidate.ProjectID)
	if err != nil {
		return out, err
	}
	out.repo, out.claim = entry.Path, workflowJSON(entry)
	out.base, err = firstWorkerEditingSubject(ctx, q, work, workerWorktreeIdentity(entry.Path), out.authorization+1)
	if err != nil {
		return out, err
	}
	out.product, err = architectureBindingProductID(ctx, q, work)
	return out, err
}

func workflowFootprintComplete(result DomainFootprintResult) error {
	if result.KnowledgeCommit == "" || result.RegistryContentHash == "" || result.NextCursor != nil {
		return fmt.Errorf("actual footprint resolution is incomplete")
	}
	for _, path := range result.Paths {
		if path.Unmapped {
			return fmt.Errorf("actual path %s has incomplete footprint ownership", path.Path)
		}
	}
	return nil
}

func workflowFootprintIndexComplete(ctx context.Context, q queryer, home KnowledgeHome) error {
	var complete bool
	if err := q.QueryRowContext(ctx, `SELECT complete FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&complete); err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("actual footprint knowledge index is incomplete")
	}
	return nil
}

func (r *ResolvedWorkflowFootprint) revalidate(ctx context.Context, q queryer, work string, candidate WorkerReviewCandidate) error {
	fail := func(err error) error { return workflowFootprintResolutionFailure(&candidate, err.Error()) }
	if r.err != nil {
		return fail(r.err)
	}
	current, err := sampleWorkflowFootprint(ctx, q, work, r.accepting)
	if err != nil {
		return fail(err)
	}
	if current != r.snapshot || candidate != r.snapshot.candidate || len(r.footprints) == 0 {
		return fail(fmt.Errorf("resolved footprint identity or scope is no longer current"))
	}
	for _, f := range r.footprints {
		home, err := resolveKnowledgeQueryHome(ctx, q, current.product, "", KnowledgeHome{}, "workflow_footprint")
		if err != nil {
			return fail(err)
		}
		if !reflect.DeepEqual(home, f.prepared.home) {
			return fail(fmt.Errorf("footprint knowledge home changed"))
		}
		if err := workflowFootprintIndexComplete(ctx, q, home); err != nil {
			return fail(err)
		}
		actual, err := f.prepared.ownershipTx(ctx, q)
		if err != nil {
			return fail(err)
		}
		if !reflect.DeepEqual(actual, f.result) {
			return fail(fmt.Errorf("footprint knowledge or applicable checks changed"))
		}
	}
	return nil
}
