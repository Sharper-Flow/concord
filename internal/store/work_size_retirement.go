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
		"revise the work size in place; Main sizes each assignment by complexity and relatedness")
}

// retiredEventAppendRefused routes each retired kind to the refusal that
// names the decision that retired it.
func retiredEventAppendRefused(kind string) error {
	switch kind {
	case WorkResized, WorkerJobSplit, WorkerJobSplitDischarged:
		return retireSizeEventAppendRefused(kind)
	}
	return planningRetiredEventAppendRefused(kind)
}

// WorkerJobMeasuredPath is one measured inventory entry of the retired size
// system: a normalized relative repository path and its
// production, test, documentation, or generated classification. It decodes
// the measurement content a stored acceptance may carry; no live route
// records or reads it.
type WorkerJobMeasuredPath struct {
	Path         string `json:"path"`
	Class        string `json:"class"`
	GeneratorRef string `json:"generator_ref,omitempty"`
}
