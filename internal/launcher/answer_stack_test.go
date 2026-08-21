package launcher

import (
	"fmt"
	"reflect"
	"testing"
)

func TestS2AnswerStackPanelOrderIsFixed(t *testing.T) {
	want := []S2Panel{S2PanelDomain, S2PanelBlocked, S2PanelNext}
	if got := S2PanelOrder(); !reflect.DeepEqual(got, want) {
		t.Fatalf("panel order=%v, want %v", got, want)
	}

	stack := (Snapshot{Screen: ScreenProduct}).S2AnswerStack()
	if got := stack.Panels; !reflect.DeepEqual(got, want) {
		t.Fatalf("composed panel order=%v, want %v", got, want)
	}
}

func TestS2AnswerStackSummaryValuesAreStoreMaterialized(t *testing.T) {
	snapshot := Snapshot{
		Screen:  ScreenProduct,
		Domains: DomainSection{Read: true, State: "authoritative", Overlaps: []OverlapPair{{From: "w-1", To: "w-2", State: "absent", SharedDomains: []string{"d-1"}}}},
		Ranked: []RankedWork{
			{ID: "w-1", Title: "First", Blocked: true, Blockers: []Blocker{{ID: "w-0", Title: "Gate", Authority: "ci"}}},
			{ID: "w-2", Title: "Second", Ready: true},
		},
	}
	stack := snapshot.S2AnswerStack()
	if got := stack.Domain.Domain.UnresolvedOverlaps; !reflect.DeepEqual(got, snapshot.Domains.Overlaps) {
		t.Fatalf("domain summary=%v, want %v", got, snapshot.Domains.Overlaps)
	}
	if stack.Blocked.Work == nil || !reflect.DeepEqual(*stack.Blocked.Work, snapshot.Ranked[0]) {
		t.Fatalf("blocked summary=%#v, want top ranked item", stack.Blocked.Work)
	}
	if stack.Next.Work == nil || !reflect.DeepEqual(*stack.Next.Work, snapshot.Ranked[0]) {
		t.Fatalf("next summary=%#v, want top ranked item", stack.Next.Work)
	}
}

func TestS2NoUnresolvedOverlapIsDistinctFromUnavailable(t *testing.T) {
	clean := (Snapshot{Screen: ScreenProduct, Domains: DomainSection{Read: true, State: "authoritative", Overlaps: []OverlapPair{{From: "w-1", To: "w-2", State: "resolved"}}}}).S2AnswerStack().Domain.Domain
	if !clean.Evaluated || clean.UnavailableReason != "" || len(clean.UnresolvedOverlaps) != 0 {
		t.Fatalf("clean domain summary=%#v", clean)
	}

	unavailable := (Snapshot{Screen: ScreenProduct, Domains: DomainSection{Read: true, State: "unavailable", Reason: "domain registry unavailable"}}).S2AnswerStack().Domain.Domain
	if unavailable.Evaluated || unavailable.UnavailableReason != "domain registry unavailable" {
		t.Fatalf("unavailable domain summary=%#v", unavailable)
	}
}

func TestS2AnswerStackRenderIsIdempotent(t *testing.T) {
	snapshot := Snapshot{Screen: ScreenProduct, Domains: DomainSection{Read: true, State: "authoritative"}, Ranked: []RankedWork{{ID: "w-1", Title: "First", Ready: true}}}
	first := fmt.Sprintf("%#v", snapshot.S2AnswerStack())
	second := fmt.Sprintf("%#v", snapshot.S2AnswerStack())
	if first != second {
		t.Fatalf("unchanged answer stack changed between renders:\n%s\n%s", first, second)
	}
}

func TestS2PanelFocusCyclesAndS3SectionsRemainSeparate(t *testing.T) {
	m := New(nil)
	m.RestoreSnapshot(Snapshot{Screen: ScreenProduct, Section: SectionDomains})
	if got := m.PanelFocus(); got != S2PanelDomain {
		t.Fatalf("initial S2 focus=%q, want %q", got, S2PanelDomain)
	}
	for _, want := range []S2Panel{S2PanelBlocked, S2PanelNext, S2PanelDomain} {
		if got := m.CyclePanelFocus(); got != want {
			t.Fatalf("cycled S2 focus=%q, want %q", got, want)
		}
	}
	m.RestoreSnapshot(Snapshot{Screen: ScreenWork, Section: SectionRelations})
	if got := m.CyclePanelFocus(); got != S2PanelDomain {
		t.Fatalf("S3 changed S2 focus=%q", got)
	}
	if got := m.Section(); got != SectionRelations {
		t.Fatalf("S3 section=%q, want %q", got, SectionRelations)
	}
}
