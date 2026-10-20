package store

import (
	"sort"
	"testing"
)

// payloadVersionClass declares how one payload version bump stands toward an
// older release that reads stored payloads outside the event registry
// (CD-0008 D6). A replay-only bump keeps the name, type, and meaning of every
// such field, so it leaves the compatibility floor alone and only an older
// release's full-log replay refuses it. A breaking bump removes, renames,
// retypes, or redefines such a field and names the Breaking migration that
// raises the floor for it.
type payloadVersionClass struct {
	breaking  bool
	migration int
}

var replayOnly = payloadVersionClass{}

func breakingAt(migration int) payloadVersionClass {
	return payloadVersionClass{breaking: true, migration: migration}
}

// payloadVersionClasses declares every registered payload version above 1.
// The class is the author's signed claim: the test below holds it to this
// vocabulary and to its floor consequence, and review owns whether it is true.
var payloadVersionClasses = map[string]map[int]payloadVersionClass{
	"project.created":                  {2: replayOnly},
	"project.locator_added":            {2: breakingAt(128)},
	"project.locator_updated":          {2: breakingAt(128)},
	"work.created":                     {2: replayOnly, 3: replayOnly, 4: replayOnly},
	"work.worktree_occupancy_released": {2: replayOnly},
	"work.session_vacated":             {2: replayOnly},
	"work.session_claim_landed":        {2: replayOnly},
	"compaction_link.published":        {2: replayOnly, 3: replayOnly},
	WorkerDispatched:                   {2: replayOnly, 3: replayOnly, 4: replayOnly, 5: replayOnly, 6: replayOnly},
	WorkerCompleted:                    {2: replayOnly, 3: replayOnly, 4: replayOnly, 5: replayOnly, 6: replayOnly, 7: replayOnly},
	WorkerFailed:                       {2: replayOnly, 3: replayOnly, 4: replayOnly},
	WorkflowContractApproved:           {2: replayOnly, 3: replayOnly, 4: replayOnly, 5: replayOnly, 6: replayOnly, 7: replayOnly},
	WorkflowNativeRunRecorded:          {2: replayOnly},
	WorkflowContractSuperseded:         {2: replayOnly, 3: replayOnly, 4: replayOnly, 5: replayOnly, 6: replayOnly},
	WorkflowActionCompleted:            {2: replayOnly, 3: replayOnly, 4: replayOnly, 5: replayOnly, 6: replayOnly},
	WorkflowVerdictRecorded:            {2: replayOnly},
	WorkflowSuccessorLinked:            {2: replayOnly, 3: replayOnly},
	WorkflowImpactNoticeRecorded:       {2: replayOnly},
	WorkflowCompleted:                  {2: replayOnly},
	WorkSized:                          {2: replayOnly, 3: replayOnly},
	WorkflowDefinitionSelected:         {2: replayOnly},
	WorkflowPremiseConfirmed:           {2: replayOnly},
	// Both bumps admit an empty approval_ref for unsigned routine writes.
	// The previous release reads the correction approval_ref outside the
	// registry and requires it; migration 134 admits the empty routine
	// overlap reference, so 134 raises the floor for both.
	WorkflowDeliveryCorrected: {2: breakingAt(134)},
	WorkflowOverlapResolved:   {2: breakingAt(134)},
}

func TestPayloadVersionsDeclareClass(t *testing.T) {
	t.Parallel()
	breaking := make(map[int]bool, len(migrations))
	for _, m := range migrations {
		breaking[m.Version] = m.Breaking
	}
	kinds := make([]string, 0, len(eventKindRegistry))
	for kind := range eventKindRegistry {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		for version := 2; version <= eventKindRegistry[kind].CurrentVersion; version++ {
			class, declared := payloadVersionClasses[kind][version]
			if !declared {
				t.Errorf("%s payload version %d declares no class in payloadVersionClasses (payload_version_class_test.go): declare replayOnly when it keeps every field an older release reads outside the registry, otherwise breakingAt the Breaking migration that raises the floor for it (CD-0008 D6)", kind, version)
				continue
			}
			if !class.breaking {
				continue
			}
			if isBreaking, exists := breaking[class.migration]; !exists || !isBreaking {
				t.Errorf("%s payload version %d names migration %d, which is not a Breaking migration, so it cannot raise the compatibility floor (CD-0008 D6, CD-0111 D3)", kind, version, class.migration)
			}
		}
	}
	for kind, versions := range payloadVersionClasses {
		registration, registered := eventKindRegistry[kind]
		for version := range versions {
			if !registered || version < 2 || version > registration.CurrentVersion {
				t.Errorf("%s payload version %d is declared but not registered; remove the declaration", kind, version)
			}
		}
	}
}
