// Package workflowcorpus contains the small cross-package coverage contract
// shared by the store and agent corpus runners. It deliberately contains no
// runner behavior or expected classifications.
package workflowcorpus

var AgentBoundaryScenarioIDs = []string{
	"WF25-operator-takeover-approval",
	"WF39-action-error-envelope",
	"WF46-event-version-fail-closed",
	"WF56-session-vacate-then-reclaim-passes-gate",
}
