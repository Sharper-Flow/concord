package store

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// architectureCapacityWitnessCapacity is the maximal-architecture witness
// allocation: eight named variable reserves summing to exactly 31,744
// beside the fixed 1,024-byte structural slot, for a whole packet bound
// P = 32,768.
func architectureCapacityWitnessCapacity() *WorkflowDesignArchitectureCapacity {
	return &WorkflowDesignArchitectureCapacity{
		DesignRecord:      10240,
		WorkerJob:         10240,
		WorkContext:       4096,
		LawContext:        1024,
		ProposalRecord:    512,
		WorkRecord:        512,
		TaskPremise:       4096,
		OutcomePredicates: 1024,
	}
}

// architectureCapacityFixtureString returns a canonical JSON string value of
// exactly want serialized bytes: the prefix plus ASCII filler. The prefix's
// escaped cost is measured, never assumed, so escaping-heavy prefixes land
// the value exactly on its target.
func architectureCapacityFixtureString(prefix string, want int) json.RawMessage {
	encoded, err := json.Marshal(prefix)
	if err != nil {
		panic(err)
	}
	filler := want - len(encoded)
	if filler < 0 {
		panic("fixture prefix already exceeds its byte target")
	}
	encoded, err = json.Marshal(prefix + strings.Repeat("a", filler))
	if err != nil {
		panic(err)
	}
	return json.RawMessage(encoded)
}

// architectureCapacityFixtureNote wraps one padded note in the minimal
// closed object shape {"note":"..."} the fixture uses for record members:
// nine fixed bytes around the inner string.
func architectureCapacityFixtureNote(prefix string, want int) json.RawMessage {
	return json.RawMessage(`{"note":` + string(architectureCapacityFixtureString(prefix, want-9)) + `}`)
}

// architectureCapacityPacketFixture builds the synthetic packet3 member
// inventory at the real packet locations: the seven root scalars, the fixed
// inputs members binding and report_protocol, and every variable member
// present, each slot filled exactly to its declared reserve. extra adds
// value bytes to the named member (capacity overage probes); omit drops
// named members (presence probes). worker_job must never be omitted: it is
// the lexicographically last present inputs member and carries the only
// comma-free span.
func architectureCapacityPacketFixture(laneDigestPad int, extra map[string]int, omit map[string]bool) map[string]json.RawMessage {
	members := map[string]json.RawMessage{
		"schema_version":  json.RawMessage(`"architecture_packet3_v1"`),
		"attempt_id":      json.RawMessage(`"attempt:41f3ad9c"`),
		"lane_id":         json.RawMessage(`"lane-implement"`),
		"lane_version":    json.RawMessage(`"7"`),
		"lane_digest":     json.RawMessage(`"` + strings.Repeat("d", laneDigestPad) + `"`),
		"work_id":         json.RawMessage(`"work-con-960"`),
		"step_id":         json.RawMessage(`"design"`),
		"binding":         json.RawMessage(`{"objective_source":"work_question","work_version":7}`),
		"report_protocol": json.RawMessage(`{"format":"json"}`),
	}
	value := func(key string, span int) int {
		v := span - len(key) - 3 - 1 // key span plus this member's trailing comma
		if key == "worker_job" {
			v++ // the last present inputs member carries no comma
		}
		return v + extra[key]
	}
	if !omit["task"] {
		members["task"] = architectureCapacityFixtureString("implement @all <&>", value("task", 4096))
	}
	if !omit["design_record"] {
		members["design_record"] = architectureCapacityFixtureNote("plan <&>\n", value("design_record", 10240))
	}
	if !omit["worker_job"] {
		members["worker_job"] = architectureCapacityFixtureNote("oracle \"q\"\x01", value("worker_job", 10240))
	}
	if !omit["law_context"] {
		members["law_context"] = architectureCapacityFixtureNote("law é 🛰", value("law_context", 1024))
	}
	if !omit["proposal_record"] {
		members["proposal_record"] = architectureCapacityFixtureNote("p <", value("proposal_record", 512))
	}
	if !omit["work_record"] {
		members["work_record"] = architectureCapacityFixtureNote("w &", value("work_record", 512))
	}
	if !omit["outcome_predicates"] {
		members["outcome_predicates"] = json.RawMessage("[" + string(architectureCapacityFixtureNote("pred \"\n", value("outcome_predicates", 1024)-2)) + "]")
	}
	if !omit["checkpoint"] {
		members["checkpoint"] = architectureCapacityFixtureNote("cp </>", value("checkpoint", 1365))
	}
	if !omit["correction"] {
		members["correction"] = architectureCapacityFixtureNote("fix \"k\"\\", value("correction", 1365))
	}
	if !omit["work_context"] {
		members["work_context"] = architectureCapacityFixtureNote("ctx", value("work_context", 1366))
	}
	return members
}

// architectureCapacityMutationFrame builds the mutation envelope preimage
// with the null retry packet placeholder at its fixed path
// result.retry_worker_packet, padded by a result notice.
func architectureCapacityMutationFrame(pad int) json.RawMessage {
	return json.RawMessage(`{"changed_refs":[{"path":"internal/store/workflow.go"}],"outcome":"ok","result":{"exit_code":0,"notice":"` + strings.Repeat("m", pad) + `","retry_worker_packet":null}}`)
}

// architectureCapacityInvokeFrame builds the core invoke request preimage
// with the null packet placeholder at its fixed path
// input.fields.worker_packet, padded by the call envelope directory.
func architectureCapacityInvokeFrame(pad int) json.RawMessage {
	return json.RawMessage(`{"call_envelope":{"directory":"/workspace/` + strings.Repeat("w", pad) + `","principal_ref":"principal:op","request_id":"req-con-960","schema_version":"1.0","session_ref":"ses-con-960"},"input":{"action_id":"dispatch_worker","expected_version":7,"fields":{"attempt_id":"attempt:41f3ad9c","worker_packet":null},"idempotency_key":"key-con-960","work_id":"work-con-960"},"operation":"workflow_action","tool":"concord_work_transition"}`)
}

// architectureCapacityFramePad solves the filler length that lands a frame
// preimage's non-packet contribution exactly on want bytes: substituting
// the packet replaces the four-byte null, so the non-packet contribution is
// the canonical frame length minus four.
func architectureCapacityFramePad(t *testing.T, frame func(pad int) json.RawMessage, want int) int {
	t.Helper()
	atOne, err := canonicalJSON(frame(1))
	if err != nil {
		t.Fatalf("frame preimage cannot canonicalize: %v", err)
	}
	base := len(atOne) - 4
	pad := want - base + 1
	if pad < 1 {
		t.Fatalf("frame pad arithmetic left nothing: want %d base %d", want, base)
	}
	return pad
}

// architectureCapacityStructuralPad returns the lane_digest filler length
// that lands the witness skeleton on exactly the 1,024 structural bytes.
func architectureCapacityStructuralPad(t *testing.T) int {
	t.Helper()
	profile, err := NewArchitectureCapacityProfile(architectureCapacityWitnessCapacity())
	if err != nil {
		t.Fatalf("witness profile refuses: %v", err)
	}
	atZero, err := CheckArchitecturePacketCapacity(profile, architectureCapacityPacketFixture(0, nil, nil), false)
	if err != nil {
		t.Fatalf("witness packet at zero structural pad refuses: %v", err)
	}
	pad := ArchitectureCapacityStructuralMaxBytes - atZero.StructuralBytes
	if pad < 0 {
		t.Fatalf("witness skeleton already exceeds the structural slot: %d bytes", atZero.StructuralBytes)
	}
	return pad
}

func architectureCapacityWitnessProfile(t *testing.T) ArchitectureCapacityProfile {
	t.Helper()
	profile, err := NewArchitectureCapacityProfile(architectureCapacityWitnessCapacity())
	if err != nil {
		t.Fatalf("witness profile refuses: %v", err)
	}
	return profile
}

func TestArchitectureCapacityAllocation(t *testing.T) {
	t.Run("witness_profile_sums", func(t *testing.T) {
		capacity := architectureCapacityWitnessCapacity()
		sum := capacity.DesignRecord + capacity.WorkerJob + capacity.WorkContext + capacity.LawContext +
			capacity.ProposalRecord + capacity.WorkRecord + capacity.TaskPremise + capacity.OutcomePredicates
		if sum != designArchitectureCapacityTotalMax {
			t.Fatalf("witness reserves sum to %d, want exactly %d", sum, designArchitectureCapacityTotalMax)
		}
		if sum+ArchitectureCapacityStructuralMaxBytes != ArchitectureCapacityPacketMaxBytes {
			t.Fatalf("variable total plus structural slot is %d, want exactly %d", sum+ArchitectureCapacityStructuralMaxBytes, ArchitectureCapacityPacketMaxBytes)
		}
		profile := architectureCapacityWitnessProfile(t)
		if profile.VariableTotal() != designArchitectureCapacityTotalMax || profile.PacketMax() != ArchitectureCapacityPacketMaxBytes {
			t.Fatalf("profile totals are %d/%d, want %d/%d", profile.VariableTotal(), profile.PacketMax(), designArchitectureCapacityTotalMax, ArchitectureCapacityPacketMaxBytes)
		}
		wants := map[architectureCapacitySlot]int{
			architectureSlotDesignRecord: 10240, architectureSlotWorkerJob: 10240,
			architectureSlotWorkContext: 4096, architectureSlotLawContext: 1024,
			architectureSlotProposalRecord: 512, architectureSlotWorkRecord: 512,
			architectureSlotTaskPremise: 4096, architectureSlotOutcomePredicates: 1024,
		}
		for slot, want := range wants {
			if profile.Reserve(slot) != want {
				t.Fatalf("slot %s reserve is %d, want %d", slot, profile.Reserve(slot), want)
			}
		}
		over := architectureCapacityWitnessCapacity()
		over.DesignRecord++
		if err := validateDesignArchitectureCapacity(over); err == nil {
			t.Fatal("reserve overflow is accepted")
		}
		negative := architectureCapacityWitnessCapacity()
		negative.WorkContext = -1
		if err := validateDesignArchitectureCapacity(negative); err == nil {
			t.Fatal("negative reserve is accepted")
		}
	})

	t.Run("zero_profile_never_certifies", func(t *testing.T) {
		if _, err := CheckArchitecturePacketCapacity(ArchitectureCapacityProfile{}, nil, false); err == nil {
			t.Fatal("a zero profile certified an empty packet")
		}
		if _, err := CheckArchitecturePacketCapacity(ArchitectureCapacityProfile{}, map[string]json.RawMessage{
			"task": json.RawMessage(`"x"`),
		}, false); err == nil {
			t.Fatal("a zero profile certified a packet member")
		}
		if _, err := MeasureArchitectureCapacityReservation(ArchitectureCapacityProfile{}, ArchitectureCapacityReservation{}); err == nil {
			t.Fatal("a zero profile certified a reservation")
		}
	})

	t.Run("legacy_pin_without_capacity_is_inactive", func(t *testing.T) {
		_, err := NewArchitectureCapacityProfile(nil)
		if err == nil {
			t.Fatal("a legacy pin without a capacity declaration produced a packet3 profile")
		}
		if !strings.Contains(err.Error(), "legacy") {
			t.Fatalf("refusal does not name the legacy pin inactivity: %v", err)
		}
	})

	t.Run("required_members_complete", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		missing := architectureCapacityPacketFixture(pad, nil, nil)
		delete(missing, "task")
		_, err := CheckArchitecturePacketCapacity(profile, missing, false)
		if err == nil {
			t.Fatal("a packet without the required task member is accepted")
		}
		if !strings.Contains(err.Error(), `"task"`) || !strings.Contains(err.Error(), "required") {
			t.Fatalf("missing-member refusal does not name task: %v", err)
		}
		missing = architectureCapacityPacketFixture(pad, nil, nil)
		delete(missing, "design_record")
		if _, err := CheckArchitecturePacketCapacity(profile, missing, false); err == nil {
			t.Fatal("a packet without the required design record is accepted")
		} else if !strings.Contains(err.Error(), `"design_record"`) {
			t.Fatalf("missing-member refusal does not name the design record: %v", err)
		}
		mutPad := architectureCapacityFramePad(t, architectureCapacityMutationFrame, ArchitectureCapacityMutationFrameBytes)
		invPad := architectureCapacityFramePad(t, architectureCapacityInvokeFrame, ArchitectureCapacityInvokeNonPacketBytes)
		next := architectureCapacityPacketFixture(pad, nil, nil)
		delete(next, "worker_job")
		_, err = MeasureArchitectureCapacityReservation(profile, ArchitectureCapacityReservation{
			Current:       architectureCapacityPacketFixture(pad, nil, nil),
			Next:          next,
			MutationFrame: architectureCapacityMutationFrame(mutPad),
			InvokeFrame:   architectureCapacityInvokeFrame(invPad),
		})
		if err == nil {
			t.Fatal("a next state without the required worker job is accepted")
		}
		if !strings.Contains(err.Error(), `"worker_job"`) {
			t.Fatalf("next-state refusal does not name the worker job: %v", err)
		}
	})

	t.Run("current_state_allows_absent_worker_job", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		mutPad := architectureCapacityFramePad(t, architectureCapacityMutationFrame, ArchitectureCapacityMutationFrameBytes)
		invPad := architectureCapacityFramePad(t, architectureCapacityInvokeFrame, ArchitectureCapacityInvokeNonPacketBytes)
		frames := func() ArchitectureCapacityReservation {
			return ArchitectureCapacityReservation{
				MutationFrame: architectureCapacityMutationFrame(mutPad),
				InvokeFrame:   architectureCapacityInvokeFrame(invPad),
			}
		}
		// Before job recording the current packet carries no worker_job; the
		// reservation must still pass and the reserve must stay retained for
		// the finite next state.
		reservation := frames()
		reservation.Current = architectureCapacityPacketFixture(pad, nil, map[string]bool{"worker_job": true})
		reservation.Next = architectureCapacityPacketFixture(pad, nil, nil)
		certificate, err := MeasureArchitectureCapacityReservation(profile, reservation)
		if err != nil {
			t.Fatalf("current packet without worker_job refuses: %v", err)
		}
		if profile.Reserve(architectureSlotWorkerJob) != 10240 {
			t.Fatalf("absent current worker_job freed its reserve to %d, want 10240", profile.Reserve(architectureSlotWorkerJob))
		}
		if certificate.NextPacketBytes != ArchitectureCapacityPacketMaxBytes {
			t.Fatalf("next packet with the ready worker job measures %d, want %d", certificate.NextPacketBytes, ArchitectureCapacityPacketMaxBytes)
		}
		// The companion next state without the worker job still refuses.
		reservation = frames()
		reservation.Current = architectureCapacityPacketFixture(pad, nil, map[string]bool{"worker_job": true})
		reservation.Next = architectureCapacityPacketFixture(pad, nil, map[string]bool{"worker_job": true})
		_, err = MeasureArchitectureCapacityReservation(profile, reservation)
		if err == nil {
			t.Fatal("a next state without the required worker job is accepted")
		}
		if !strings.Contains(err.Error(), `"worker_job"`) {
			t.Fatalf("next-state refusal does not name the worker job: %v", err)
		}
	})

	t.Run("canonical_architecture_fits_design_record_slot", func(t *testing.T) {
		plan := designArchitectureAtCanonicalBound(t)
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("cannot encode the padded plan: %v", err)
		}
		if len(raw) != designArchitectureCanonicalMaxBytes {
			t.Fatalf("canonical plan is %d bytes, want exactly %d", len(raw), designArchitectureCanonicalMaxBytes)
		}
		if err := validateWorkflowDesignArchitecture(plan, designArchitectureMemberProject); err != nil {
			t.Fatalf("plan at the canonical bound refused: %v", err)
		}
		projection, err := json.Marshal(struct {
			Architecture   json.RawMessage `json:"architecture"`
			DesignRevision int             `json:"design_revision"`
			DesignDigest   string          `json:"design_digest"`
		}{raw, 3, strings.Repeat("a", 64)})
		if err != nil {
			t.Fatalf("design projection cannot marshal: %v", err)
		}
		if len(projection) > 10240 {
			t.Fatalf("design_record projection is %d bytes, want at most 10240", len(projection))
		}
		pad := architectureCapacityStructuralPad(t)
		members := architectureCapacityPacketFixture(pad, nil, nil)
		members["design_record"] = projection
		measured, err := CheckArchitecturePacketCapacity(architectureCapacityWitnessProfile(t), members, false)
		if err != nil {
			t.Fatalf("packet carrying the exact 8,192-byte canonical architecture refuses: %v", err)
		}
		if want := len(`"design_record":`) + 1 + len(projection); measured.SlotBytes[architectureSlotDesignRecord] != want {
			t.Fatalf("design_record slot measures %d, want %d", measured.SlotBytes[architectureSlotDesignRecord], want)
		}
	})

	t.Run("witness_packet_disjoint_wire_partition", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		measured, err := CheckArchitecturePacketCapacity(profile, architectureCapacityPacketFixture(pad, nil, nil), false)
		if err != nil {
			t.Fatalf("witness packet at the exact allocation refuses: %v", err)
		}
		slotSum := 0
		for slot, slotBytes := range measured.SlotBytes {
			if slotBytes != profile.Reserve(slot) {
				t.Fatalf("slot %s measures %d, want exactly its reserve %d", slot, slotBytes, profile.Reserve(slot))
			}
			slotSum += slotBytes
		}
		if slotSum != designArchitectureCapacityTotalMax {
			t.Fatalf("slot spans sum to %d, want exactly %d", slotSum, designArchitectureCapacityTotalMax)
		}
		if measured.StructuralBytes != ArchitectureCapacityStructuralMaxBytes {
			t.Fatalf("structural skeleton measures %d, want exactly %d", measured.StructuralBytes, ArchitectureCapacityStructuralMaxBytes)
		}
		if measured.PacketBytes != ArchitectureCapacityPacketMaxBytes {
			t.Fatalf("whole packet measures %d, want exactly %d", measured.PacketBytes, ArchitectureCapacityPacketMaxBytes)
		}
		if measured.StructuralBytes+slotSum != measured.PacketBytes {
			t.Fatalf("partition is not disjoint: structural %d plus slots %d is %d, packet is %d",
				measured.StructuralBytes, slotSum, measured.StructuralBytes+slotSum, measured.PacketBytes)
		}
		canonical, err := canonicalJSON(measured.Packet)
		if err != nil {
			t.Fatalf("assembled packet cannot canonicalize: %v", err)
		}
		if !bytes.Equal(canonical, measured.Packet) {
			t.Fatal("assembled packet is not Go-canonical JSON")
		}
		// Maximal escaping is actually present in the measured bytes: HTML
		// escapes, control escapes, quote and backslash escapes, and raw
		// multibyte UTF-8 all cost their canonical bytes inside the slots.
		for _, escape := range []string{`\u003c`, `\u0001`, `\n`, `\"`, `\\`, "é", "🛰"} {
			if !strings.Contains(string(measured.Packet), escape) {
				t.Fatalf("witness packet lacks the expected escaping/content %q", escape)
			}
		}
	})

	t.Run("each_slot_exact_and_cap_plus_one", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		bumps := map[architectureCapacitySlot]string{
			architectureSlotDesignRecord:      "design_record",
			architectureSlotWorkerJob:         "worker_job",
			architectureSlotWorkContext:       "work_context",
			architectureSlotLawContext:        "law_context",
			architectureSlotProposalRecord:    "proposal_record",
			architectureSlotWorkRecord:        "work_record",
			architectureSlotTaskPremise:       "task",
			architectureSlotOutcomePredicates: "outcome_predicates",
		}
		for slot, member := range bumps {
			reserve := profile.Reserve(slot)
			_, err := CheckArchitecturePacketCapacity(profile, architectureCapacityPacketFixture(pad, map[string]int{member: 1}, nil), false)
			if err == nil {
				t.Fatalf("slot %s accepts one byte above its reserve %d", slot, reserve)
			}
			for _, want := range []string{string(slot), strconv.Itoa(reserve + 1), strconv.Itoa(reserve)} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("slot %s overage error lacks %q: %v", slot, want, err)
				}
			}
			failure, ok := err.(*Failure)
			if !ok || failure.Kind != KindLimitExceeded {
				t.Fatalf("slot %s overage is not a typed limit-exceeded failure: %v", slot, err)
			}
		}
	})

	t.Run("context_population_exact", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		span := func(key string) int {
			fixture := architectureCapacityPacketFixture(pad, nil, nil)
			return len(key) + 3 + len(fixture[key]) + 1 // quotes, key, colon, value, comma
		}
		onlyContext, err := CheckArchitecturePacketCapacity(profile,
			architectureCapacityPacketFixture(pad, nil, map[string]bool{"checkpoint": true, "correction": true}), false)
		if err != nil {
			t.Fatalf("present work_context member alone refuses: %v", err)
		}
		if onlyContext.SlotBytes[architectureSlotWorkContext] != span("work_context") {
			t.Fatalf("work_context slot with one present member measures %d, want exactly %d",
				onlyContext.SlotBytes[architectureSlotWorkContext], span("work_context"))
		}
		withCheckpoint, err := CheckArchitecturePacketCapacity(profile,
			architectureCapacityPacketFixture(pad, nil, map[string]bool{"correction": true}), false)
		if err != nil {
			t.Fatalf("present work_context and checkpoint refuse: %v", err)
		}
		if withCheckpoint.SlotBytes[architectureSlotWorkContext] != span("work_context")+span("checkpoint") {
			t.Fatalf("work_context slot with two present members measures %d, want exactly %d",
				withCheckpoint.SlotBytes[architectureSlotWorkContext], span("work_context")+span("checkpoint"))
		}
		// The exact three-member population fills the slot: the disjoint
		// subtest proves the whole 4,096 bytes. Absence never frees the
		// reserve: the declared allocation keeps every reserve.
		if profile.Reserve(architectureSlotWorkContext) != 4096 {
			t.Fatalf("absent members freed work_context reserve to %d, want 4096", profile.Reserve(architectureSlotWorkContext))
		}
		if profile.VariableTotal() != designArchitectureCapacityTotalMax {
			t.Fatalf("absent members freed the allocation total to %d, want %d", profile.VariableTotal(), designArchitectureCapacityTotalMax)
		}
	})

	t.Run("structural_and_packet_caps", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		if _, err := CheckArchitecturePacketCapacity(profile, architectureCapacityPacketFixture(pad, nil, nil), false); err != nil {
			t.Fatalf("witness packet at structural 1,024 refuses: %v", err)
		}
		_, err := CheckArchitecturePacketCapacity(profile, architectureCapacityPacketFixture(pad+1, nil, nil), false)
		if err == nil {
			t.Fatal("structural skeleton at 1,025 bytes is accepted")
		}
		for _, want := range []string{"structural", "1025", "1024"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("structural overage error lacks %q: %v", want, err)
			}
		}
	})

	t.Run("reservation_frames_measured", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		current := architectureCapacityPacketFixture(pad, nil, nil)
		next := architectureCapacityPacketFixture(pad, nil, nil)
		mutPad := architectureCapacityFramePad(t, architectureCapacityMutationFrame, ArchitectureCapacityMutationFrameBytes)
		invPad := architectureCapacityFramePad(t, architectureCapacityInvokeFrame, ArchitectureCapacityInvokeNonPacketBytes)
		certificate, err := MeasureArchitectureCapacityReservation(profile, ArchitectureCapacityReservation{
			Current:       current,
			Next:          next,
			MutationFrame: architectureCapacityMutationFrame(mutPad),
			InvokeFrame:   architectureCapacityInvokeFrame(invPad),
		})
		if err != nil {
			t.Fatalf("witness reservation refuses: %v", err)
		}
		if certificate.PacketBytes != ArchitectureCapacityPacketMaxBytes || certificate.NextPacketBytes != ArchitectureCapacityPacketMaxBytes {
			t.Fatalf("current/next packets measure %d/%d, want exactly %d each",
				certificate.PacketBytes, certificate.NextPacketBytes, ArchitectureCapacityPacketMaxBytes)
		}
		if certificate.MutationFrameBytes != ArchitectureCapacityMutationFrameBytes {
			t.Fatalf("mutation frame excluding the returned packet measures %d, want exactly %d",
				certificate.MutationFrameBytes, ArchitectureCapacityMutationFrameBytes)
		}
		if certificate.RetryReplyBytes != ArchitectureCapacityReplyMaxBytes {
			t.Fatalf("whole retry reply measures %d, want exactly %d", certificate.RetryReplyBytes, ArchitectureCapacityReplyMaxBytes)
		}
		if certificate.InvokeNonPacketBytes != ArchitectureCapacityInvokeNonPacketBytes {
			t.Fatalf("invoke frame excluding the packet measures %d, want exactly %d",
				certificate.InvokeNonPacketBytes, ArchitectureCapacityInvokeNonPacketBytes)
		}
		if certificate.InvokeRequestBytes != ArchitectureCapacityInvokeTotalMaxBytes {
			t.Fatalf("whole invoke request measures %d, want exactly %d", certificate.InvokeRequestBytes, ArchitectureCapacityInvokeTotalMaxBytes)
		}
		// The certificate binds the law digest derived from the measured
		// canonical current members, not a caller-quoted digest.
		wantDigest, err := ArchitectureLawContextDigest(current["law_context"])
		if err != nil {
			t.Fatalf("law digest refuses: %v", err)
		}
		if certificate.LawContextDigest != wantDigest {
			t.Fatal("certificate law digest does not match the measured current projection")
		}
		// The next packet rides as an object at both fixed paths, never as
		// a JSON string.
		for _, probe := range []struct{ whole, marker string }{
			{string(certificate.RetryReply), `"retry_worker_packet":{"attempt_id":`},
			{string(certificate.InvokeRequest), `"worker_packet":{"attempt_id":`},
		} {
			if !strings.Contains(probe.whole, probe.marker) {
				t.Fatalf("frame does not carry the packet as an object at %s", probe.marker)
			}
		}
		if strings.Contains(string(certificate.InvokeRequest), `{\"schema_version\"`) {
			t.Fatal("invoke request string-encodes the packet")
		}
	})

	t.Run("reservation_frame_overages", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		current := architectureCapacityPacketFixture(pad, nil, nil)
		next := architectureCapacityPacketFixture(pad, nil, nil)
		mutPad := architectureCapacityFramePad(t, architectureCapacityMutationFrame, ArchitectureCapacityMutationFrameBytes)
		invPad := architectureCapacityFramePad(t, architectureCapacityInvokeFrame, ArchitectureCapacityInvokeNonPacketBytes)
		_, err := MeasureArchitectureCapacityReservation(profile, ArchitectureCapacityReservation{
			Current: current, Next: next,
			MutationFrame: architectureCapacityMutationFrame(mutPad + 1),
			InvokeFrame:   architectureCapacityInvokeFrame(invPad),
		})
		if err == nil {
			t.Fatal("mutation frame one byte above its reservation is accepted")
		}
		for _, want := range []string{"mutation frame", "16385", "16384"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("mutation frame overage error lacks %q: %v", want, err)
			}
		}
		_, err = MeasureArchitectureCapacityReservation(profile, ArchitectureCapacityReservation{
			Current: current, Next: next,
			MutationFrame: architectureCapacityMutationFrame(mutPad),
			InvokeFrame:   architectureCapacityInvokeFrame(invPad + 1),
		})
		if err == nil {
			t.Fatal("invoke frame one byte above its reservation is accepted")
		}
		for _, want := range []string{"invoke frame", "1048577", "1048576"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("invoke frame overage error lacks %q: %v", want, err)
			}
		}
		// Malformed and unresolved frames refuse; nothing measures as zero.
		for name, broken := range map[string]json.RawMessage{
			"malformed":  json.RawMessage(`{"result":`),
			"no_path":    json.RawMessage(`{"outcome":"ok"}`),
			"not_object": json.RawMessage(`{"result":7,"retry_worker_packet":null}`),
			"not_null":   json.RawMessage(`{"result":{"retry_worker_packet":{}}}`),
		} {
			if _, err := MeasureArchitectureCapacityReservation(profile, ArchitectureCapacityReservation{
				Current: current, Next: next, MutationFrame: broken,
				InvokeFrame: architectureCapacityInvokeFrame(invPad),
			}); err == nil {
				t.Fatalf("%s mutation frame is accepted", name)
			}
		}
	})

	t.Run("next_state_overflows", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		current := architectureCapacityPacketFixture(pad, nil, nil)
		mutPad := architectureCapacityFramePad(t, architectureCapacityMutationFrame, ArchitectureCapacityMutationFrameBytes)
		invPad := architectureCapacityFramePad(t, architectureCapacityInvokeFrame, ArchitectureCapacityInvokeNonPacketBytes)
		frames := func() ArchitectureCapacityReservation {
			return ArchitectureCapacityReservation{
				Current:       current,
				MutationFrame: architectureCapacityMutationFrame(mutPad),
				InvokeFrame:   architectureCapacityInvokeFrame(invPad),
			}
		}
		_, err := MeasureArchitectureCapacityReservation(profile, func() ArchitectureCapacityReservation {
			reservation := frames()
			reservation.Next = architectureCapacityPacketFixture(pad, map[string]int{"design_record": 1}, nil)
			return reservation
		}())
		if err == nil {
			t.Fatal("next state one byte above the design_record reserve is accepted")
		}
		for _, want := range []string{"design_record", "10241", "10240"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("next-state slot overage error lacks %q: %v", want, err)
			}
		}
		_, err = MeasureArchitectureCapacityReservation(profile, func() ArchitectureCapacityReservation {
			reservation := frames()
			reservation.Next = architectureCapacityPacketFixture(pad+1, nil, nil)
			return reservation
		}())
		if err == nil {
			t.Fatal("next-state structural metadata one byte over is accepted")
		}
		for _, want := range []string{"structural", "1025", "1024"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("next-state structural overage error lacks %q: %v", want, err)
			}
		}
	})

	t.Run("task_prompt_encodings", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		measured, err := CheckArchitecturePacketCapacity(architectureCapacityWitnessProfile(t), architectureCapacityPacketFixture(pad, nil, nil), false)
		if err != nil {
			t.Fatalf("witness packet refuses: %v", err)
		}
		promptMax := ArchitectureCapacityTaskPromptMaxBytes
		if promptMax != 6*ArchitectureCapacityPacketMaxBytes {
			t.Fatalf("Task prompt bound is %d, want six times the packet bound", promptMax)
		}
		// The adapter's serializeLanePacket escapes each @ as six bytes; the
		// conservative all-@ cost of the whole packet is 6P.
		escaped := strings.ReplaceAll(string(measured.Packet), "@", "\\u0040")
		if len(escaped) <= measured.PacketBytes {
			t.Fatal("fixture packet carries no @ to escape")
		}
		if len(escaped) > promptMax {
			t.Fatalf("@-escaped prompt is %d bytes, want at most %d", len(escaped), promptMax)
		}
		// Encoding that entire prompt as one JSON string is bounded by six
		// bytes per prompt byte plus the two quotes.
		jsonEncoded, err := json.Marshal(escaped)
		if err != nil {
			t.Fatalf("Task prompt cannot JSON-encode: %v", err)
		}
		if promptMax*6+2 != ArchitectureCapacityTaskPromptJSONStringMaxBytes {
			t.Fatalf("JSON-string Task bound is %d, want %d", ArchitectureCapacityTaskPromptJSONStringMaxBytes, promptMax*6+2)
		}
		if len(jsonEncoded) > ArchitectureCapacityTaskPromptJSONStringMaxBytes {
			t.Fatalf("JSON-encoded prompt is %d bytes, want at most %d", len(jsonEncoded), ArchitectureCapacityTaskPromptJSONStringMaxBytes)
		}
		if schemawire.JSONStringWireBytes(promptMax) != ArchitectureCapacityTaskPromptJSONStringMaxBytes {
			t.Fatalf("JSON-string bound %d disagrees with the schemawire rule", ArchitectureCapacityTaskPromptJSONStringMaxBytes)
		}
	})

	t.Run("law_context_digest", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		base := architectureCapacityPacketFixture(pad, nil, nil)
		present, err := ArchitectureLawContextDigest(base["law_context"])
		if err != nil {
			t.Fatalf("law_context digest refuses a canonical projection: %v", err)
		}
		if len(present) != 64 {
			t.Fatalf("law_context digest is %d hex bytes, want 64", len(present))
		}
		changed := architectureCapacityPacketFixture(pad, map[string]int{"law_context": 1}, nil)
		changedDigest, err := ArchitectureLawContextDigest(changed["law_context"])
		if err != nil {
			t.Fatalf("changed law_context digest refuses: %v", err)
		}
		if changedDigest == present {
			t.Fatal("a changed law projection kept the same certificate digest")
		}
		absent, err := ArchitectureLawContextDigest(nil)
		if err != nil {
			t.Fatalf("explicit absence refuses: %v", err)
		}
		if len(absent) != 64 {
			t.Fatalf("absence digest is %d hex bytes, want 64", len(absent))
		}
		if absent == present {
			t.Fatal("absence digests as a present projection")
		}
		again, err := ArchitectureLawContextDigest(nil)
		if err != nil || again != absent {
			t.Fatalf("absence digest is not deterministic: %v %v", again, err)
		}
		if _, err := ArchitectureLawContextDigest(json.RawMessage(`{"note":`)); err == nil {
			t.Fatal("an unparseable law projection digests instead of refusing")
		}
	})

	t.Run("absent_and_unknown_states", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		noLaw, err := CheckArchitecturePacketCapacity(profile,
			architectureCapacityPacketFixture(pad, nil, map[string]bool{"law_context": true}), false)
		if err != nil {
			t.Fatalf("absent law_context refuses: %v", err)
		}
		if noLaw.SlotBytes[architectureSlotLawContext] != 0 {
			t.Fatalf("absent law_context measures %d, want zero span", noLaw.SlotBytes[architectureSlotLawContext])
		}
		if profile.Reserve(architectureSlotLawContext) != 1024 {
			t.Fatalf("absence freed the law_context reserve to %d, want 1024", profile.Reserve(architectureSlotLawContext))
		}
		unknown := architectureCapacityPacketFixture(pad, nil, nil)
		unknown["law_context"] = json.RawMessage(`{"note":`)
		if _, err := CheckArchitecturePacketCapacity(profile, unknown, false); err == nil {
			t.Fatal("an unparseable member measured as zero instead of refusing")
		} else if !strings.Contains(err.Error(), "law_context") {
			t.Fatalf("unknown-state refusal does not name the member: %v", err)
		}
		unresolved := architectureCapacityPacketFixture(pad, nil, nil)
		unresolved["task"] = nil
		if _, err := CheckArchitecturePacketCapacity(profile, unresolved, false); err == nil {
			t.Fatal("a member without projection bytes measured as zero")
		} else if !strings.Contains(err.Error(), "task") {
			t.Fatalf("unresolved refusal does not name the member: %v", err)
		}
	})

	t.Run("closed_member_inventory", func(t *testing.T) {
		pad := architectureCapacityStructuralPad(t)
		profile := architectureCapacityWitnessProfile(t)
		for key, named := range map[string]string{
			"context":     "inputs.context",
			"constraints": "inputs.constraints",
			"mystery":     "mystery",
		} {
			members := architectureCapacityPacketFixture(pad, nil, nil)
			members[key] = json.RawMessage(`{}`)
			_, err := CheckArchitecturePacketCapacity(profile, members, false)
			if err == nil {
				t.Fatalf("member %q outside the closed inventory is accepted", key)
			}
			if !strings.Contains(err.Error(), named) {
				t.Fatalf("closed-inventory refusal lacks %q: %v", named, err)
			}
		}
	})
}
