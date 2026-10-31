package store

import (
	"context"
	"testing"
)

// syntheticMixedWorkerResult builds one worker's planned durable_mixed sample
// set without touching a store. It mirrors runDurableMixedScenario's plan:
// three reads per iteration, the durable claim at iteration 0, the durable
// completion plus the fence read-back at the last iteration.
func syntheticMixedWorkerResult(worker int) WorkerResult {
	result := WorkerResult{Worker: worker, PID: 1000 + worker, Outcome: outcomeAccepted, DurableCommits: durableMixedDurableOpsPerWorker}
	read := WorkerSample{Outcome: outcomeAccepted, Profile: mixedSampleProfileRead}
	durable := WorkerSample{Outcome: outcomeAccepted, Profile: mixedSampleProfileDurable}
	for iteration := 0; iteration < durableMixedIterations; iteration++ {
		for i := 0; i < durableMixedReadsPerIteration; i++ {
			result.Samples = append(result.Samples, read)
		}
		if iteration == 0 {
			result.Samples = append(result.Samples, durable)
		}
		if iteration == durableMixedIterations-1 {
			result.Samples = append(result.Samples, durable, read)
		}
	}
	return result
}

func TestDurableMixedWorkerPlanIdentitiesAreUniqueAndPaired(t *testing.T) {
	t.Parallel()

	seenOps := map[string]int{}
	seenClaimKeys := map[string]int{}
	seenCompletionKeys := map[string]int{}
	for worker := 0; worker < 10; worker++ {
		claim, completion := durableMixedWorkerPlan(worker)
		if claim.OpID != completion.OpID {
			t.Fatalf("worker %d plan split its operation: claim %q completion %q", worker, claim.OpID, completion.OpID)
		}
		if claim.IdempotencyKey == completion.IdempotencyKey {
			t.Fatalf("worker %d reuses one idempotency key across claim and completion", worker)
		}
		if completion.AttemptEpoch != 1 || completion.ResultKind != ResultCompleted {
			t.Fatalf("worker %d completion = epoch %d kind %q, want epoch 1 completed", worker, completion.AttemptEpoch, completion.ResultKind)
		}
		seenOps[claim.OpID]++
		seenClaimKeys[claim.IdempotencyKey]++
		seenCompletionKeys[completion.IdempotencyKey]++
	}
	for name, seen := range map[string]map[string]int{"op": seenOps, "claim key": seenClaimKeys, "completion key": seenCompletionKeys} {
		if len(seen) != 10 {
			t.Fatalf("%s identities = %d distinct, want 10", name, len(seen))
		}
	}
}

func TestSummarizeMixedPopulationMatchesThePlan(t *testing.T) {
	t.Parallel()

	results := make([]WorkerResult, 0, 10)
	for worker := 0; worker < 10; worker++ {
		results = append(results, syntheticMixedWorkerResult(worker))
	}
	population := summarizeMixedPopulation(results)
	if err := validateMixedPopulation(population, 10); err != nil {
		t.Fatalf("planned mixed population failed validation: %v", err)
	}
	if population.ReadSamples != durableMixedExpectedReadSamples(10) {
		t.Fatalf("read samples = %d, want %d", population.ReadSamples, durableMixedExpectedReadSamples(10))
	}
	if population.DurableSamples != durableMixedExpectedDurableSamples(10) {
		t.Fatalf("durable samples = %d, want %d", population.DurableSamples, durableMixedExpectedDurableSamples(10))
	}
	if population.Accepted != population.ReadSamples+population.DurableSamples {
		t.Fatalf("accepted = %d, want %d", population.Accepted, population.ReadSamples+population.DurableSamples)
	}
}

func TestSummarizeMixedPopulationIgnoresOtherProfiles(t *testing.T) {
	t.Parallel()

	results := []WorkerResult{{
		Worker: 0,
		Samples: []WorkerSample{
			{Outcome: outcomeAccepted, Profile: "other"},
			{Outcome: outcomeAccepted, Profile: mixedSampleProfileRead},
		},
	}}
	population := summarizeMixedPopulation(results)
	if population.ReadSamples != 1 || population.DurableSamples != 0 || population.Accepted != 1 {
		t.Fatalf("non-mixed profiles leaked into the mixed population: %+v", population)
	}
	if err := validateMixedPopulation(population, 1); err == nil {
		t.Fatal("a worker without durable samples passed structural validation")
	}
}

func TestValidateMixedPopulationRefusesStructuralDrift(t *testing.T) {
	t.Parallel()

	complete := summarizeMixedPopulation([]WorkerResult{
		syntheticMixedWorkerResult(0),
		syntheticMixedWorkerResult(1),
	})
	if err := validateMixedPopulation(complete, 2); err != nil {
		t.Fatalf("complete population rejected: %v", err)
	}

	missingDurable := complete
	missingDurable.DurableSamples--
	if err := validateMixedPopulation(missingDurable, 2); err == nil {
		t.Fatal("a population with missing durable samples passed validation")
	}

	missingReads := complete
	missingReads.ReadSamples--
	if err := validateMixedPopulation(missingReads, 2); err == nil {
		t.Fatal("a population with missing read samples passed validation")
	}

	workerDrift := complete
	workerDrift.Workers = 3
	if err := validateMixedPopulation(workerDrift, 2); err == nil {
		t.Fatal("a population with the wrong worker count passed validation")
	}

	commitDrift := complete
	commitDrift.DurableCommits--
	if err := validateMixedPopulation(commitDrift, 2); err == nil {
		t.Fatal("a population with a durable commit count drift passed validation")
	}
}

// TestValidateMixedPopulationLeavesOutcomesToTheBaseGate pins that outcome
// classification is not a structural invariant: busy escapes and errors stay
// visible in the population while structural validation passes, because
// CD-0045 D2 binds zero-escape to the accepted population through the base
// gate, not through this validator.
func TestValidateMixedPopulationLeavesOutcomesToTheBaseGate(t *testing.T) {
	t.Parallel()

	results := []WorkerResult{
		syntheticMixedWorkerResult(0),
		syntheticMixedWorkerResult(1),
	}
	results[1].Samples[0] = WorkerSample{Outcome: outcomeBusyEscaped, Profile: mixedSampleProfileRead}
	results[1].Samples[1] = WorkerSample{Outcome: outcomeError, Profile: mixedSampleProfileRead}
	population := summarizeMixedPopulation(results)
	if population.BusyEscaped != 1 || population.Errors != 1 || population.Accepted != population.ReadSamples+population.DurableSamples-2 {
		t.Fatalf("outcome classification lost samples: %+v", population)
	}
	if err := validateMixedPopulation(population, 2); err != nil {
		t.Fatalf("outcome-bearing population failed structural validation: %v", err)
	}
}

// TestRunDurableMixedScenarioInProcess exercises the durable_mixed operation
// plan against one temporary database, serially and without child processes.
// Each worker opens a fresh store handle, matching the harness child, so the
// scenario's per-handle durable commit check holds. It verifies the fixture
// preparation, the durable claim and completion through the durable owner,
// the fence read-back effect check, and the parent effect verification. The
// cross-process mix itself stays owned by the ten-process harness.
func TestRunDurableMixedScenarioInProcess(t *testing.T) {
	path := copyTestDatabase(t)
	parent := openTempAtPath(t, path)
	ctx := context.Background()
	if err := prepareDurableMixedFixtures(t, ctx, parent); err != nil {
		t.Fatal(err)
	}
	for worker := 0; worker < 10; worker++ {
		handle := openTempAtPath(t, path)
		result := runDurableMixedScenario(ctx, handle, worker)
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		if result.Outcome != outcomeAccepted {
			t.Fatalf("worker %d = %+v, want accepted", worker, result)
		}
		if want := durableMixedExpectedReadSamples(1) + durableMixedExpectedDurableSamples(1); int64(len(result.Samples)) != want {
			t.Fatalf("worker %d carried %d samples, want %d", worker, len(result.Samples), want)
		}
		if result.DurableCommits != durableMixedDurableOpsPerWorker {
			t.Fatalf("worker %d committed %d durable transactions, want %d", worker, result.DurableCommits, durableMixedDurableOpsPerWorker)
		}
	}
	if err := verifyDurableMixedEffects(t, ctx, parent, 10); err != nil {
		t.Fatal(err)
	}
}
