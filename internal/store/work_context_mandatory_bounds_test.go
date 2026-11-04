package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestWorkContextMandatoryBounds(t *testing.T) {
	t.Parallel()
	t.Run("context view one byte over includes framing in recovery", func(t *testing.T) {
		buildView := func(commonRefLen, firstRefLen int) WorkContextView {
			view := WorkContextView{SourceEventFrontier: 7}
			for i := 0; i < 24; i++ {
				refs := make([]string, 0, 8)
				for ref := 0; ref < 8; ref++ {
					length := 256
					if i == 0 && ref == 0 {
						length = firstRefLen
					} else if ref == 7 {
						length = commonRefLen
					}
					refs = append(refs, fmt.Sprintf("evidence:boundary-%02d-%s", i, strings.Repeat("e", length-21)))
				}
				view.Findings = append(view.Findings, WorkContextFindingView{
					FindingID: fmt.Sprintf("finding:%d:0", 100+i), Statement: fmt.Sprintf("the retained context finding %02d carries its sized detail ", i) + strings.Repeat("m", 455),
					SubjectRef: "owner:repair", EvidenceRefs: refs, DomainID: workContextTestChild, Origin: WorkContextOriginWorkerReport,
					Status: WorkContextFindingStatusReported, SourceEventID: "source:event", SourceEventSeq: int64(100 + i), Ordinal: 0,
				})
			}
			return view
		}
		view := buildView(256, 256)
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		delta := len(raw) - 65537
		if delta < 0 {
			t.Fatalf("boundary fixture measures %d bytes, want at least 65537 before tuning", len(raw))
		}
		per := delta / 24
		remainder := delta - per*24
		view = buildView(256-per, 256-remainder)
		raw, err = json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != 65537 {
			t.Fatalf("boundary view measures %d bytes, want exactly 65537", len(raw))
		}
		unframed := len(mustJSONValue(view.SubjectCommit))
		for _, finding := range view.Findings {
			unframed += len(mustJSONValue(finding))
		}
		if unframed > WorkContextCombinedMaxBytes {
			t.Fatalf("unframed mandatory sum = %d bytes, want at most 65536 so the framing bytes carry the overflow", unframed)
		}
		recovery := workContextOverflowRecovery()
		if !strings.Contains(recovery, "reduce the declaration's optional context") {
			t.Fatalf("boundary recovery text = %q, want the optional reduction named", recovery)
		}
		if !strings.Contains(recovery, "if mandatory context alone exceeds the bound") || !strings.Contains(recovery, "revise_intent with complete contract content only when the agreed scope permits reducing mandatory context") || !strings.Contains(recovery, "stop the work") {
			t.Fatalf("boundary recovery text = %q, want the mandatory contract update or stop route explicitly conditional", recovery)
		}
	})
	t.Run("33 ordinary finding slots refuse without truncation", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-count-mandatory-regression")
		defer fixture.store.Close()
		seedMandatoryNativeEvidence(t, fixture, 33)
		_, readErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		var failure *Failure
		if readErr == nil || !errors.As(readErr, &failure) || failure.Kind != KindLimitExceeded {
			t.Fatalf("33-open-slot read = %v, want %s refusing past the 32 shared slots", readErr, KindLimitExceeded)
		}
		if !strings.Contains(failure.RecoveryAction, "revise_intent with complete contract content") || !strings.Contains(failure.RecoveryAction, "stop the work") {
			t.Fatalf("33-open-slot refusal remedy = %q, want the mandatory contract update or stop route", failure.RecoveryAction)
		}
	})
	t.Run("context overflow retains findings and refuses atomically", func(t *testing.T) {
		fixture := seedWorkContextFixture(t, "ctx-mandatory-native-regression")
		defer fixture.store.Close()
		seedMandatoryNativeEvidence(t, fixture, 24)
		_, readErr := readWorkContextView(context.Background(), fixture.store.DatabaseForTesting(), fixture.workID)
		var failure *Failure
		if readErr == nil || !errors.As(readErr, &failure) || failure.Kind != KindLimitExceeded || !strings.Contains(failure.RecoveryAction, "revise_intent with complete contract content") {
			t.Fatalf("mandatory-only read = %v, want %s naming the ordinary contract update or stop remedy", readErr, KindLimitExceeded)
		}
		retained := countRows(t, fixture.store, "work_context_findings")
		version, events := readWorkVersion(t, fixture.store, fixture.workID), declarationEventCount(t, fixture)
		before := outsideRepairDatabaseSnapshot(t, fixture.store)
		declErr := fixture.recordWorkContext(t, map[string]any{"finding_refs": []string{}, "context_findings": []map[string]any{}})
		if declErr == nil || !errors.As(declErr, &failure) || failure.Kind != KindLimitExceeded {
			t.Fatalf("mandatory-only declaration error = %v, want %s refusing whole", declErr, KindLimitExceeded)
		}
		if got := readWorkVersion(t, fixture.store, fixture.workID); got != version {
			t.Fatalf("refused declaration moved the version to %d, want %d", got, version)
		}
		if got := declarationEventCount(t, fixture); got != events {
			t.Fatalf("refused declaration left %d declaration events, want %d", got, events)
		}
		if outsideRepairDatabaseSnapshot(t, fixture.store) != before {
			t.Fatal("refused declaration changed durable state")
		}
		if after := countRows(t, fixture.store, "work_context_findings"); after != retained {
			t.Fatal("retained context findings changed")
		}
	})
}
