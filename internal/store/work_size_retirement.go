package store

const (
	// WorkResized records one resize (retired).
	WorkResized = "work.resized"
	// WorkerJobSplit records one atomic split of a worker-job revision (retired).
	WorkerJobSplit = "worker.job_split"
	// WorkerJobSplitDischarged records one split's discharge (retired).
	WorkerJobSplitDischarged = "worker.job_split_discharged"
)

// retireSizeEventAppendRefused names the decision that retired the size
// kinds. The planning kinds keep their own message in
// retiredEventAppendRefused.
func retireSizeEventAppendRefused(kind string) error {
	return newFailure(KindInvalidOperation, "apply_operation",
		"event kind "+kind+" is retired by CD-0242 D6: no production-line cap, size measurement, or mandatory split applies", false,
		"continue without a size assessment or mandatory split")
}

// retiredEventAppendRefused routes each retired kind to the refusal that
// names the decision that retired it.
func retiredEventAppendRefused(kind string) error {
	switch kind {
	case "work.sized":
		return newFailure(KindInvalidOperation, "apply_operation", "event kind work.sized is retired", false, "continue without a size assessment")
	case WorkResized, WorkerJobSplit, WorkerJobSplitDischarged:
		return retireSizeEventAppendRefused(kind)
	}
	return planningRetiredEventAppendRefused(kind)
}
