// Package workflowcorpus contains the small cross-package coverage contract
// shared by the store and agent corpus runners. It deliberately contains no
// runner behavior or expected classifications.
package workflowcorpus

var AgentBoundaryScenarioIDs = []string{
	"WF39-action-error-envelope",
	"WF46-event-version-fail-closed",
}

func AgentBoundaryOwns(id string) bool {
	for _, candidate := range AgentBoundaryScenarioIDs {
		if candidate == id {
			return true
		}
	}
	return false
}
