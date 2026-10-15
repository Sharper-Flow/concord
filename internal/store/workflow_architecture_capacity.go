package store

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// The architecture_packet3_v1 capacity value model: one typed, pure owner
// that measures actual canonical packet and frame projections against a
// declared eight-slot allocation. Byte counts and digests are computed here
// from the supplied projections; a caller-reported count is never evidence
// and an unpinned profile certifies nothing.
const (
	ArchitectureCapacityStructuralMaxBytes           = 1024
	ArchitectureCapacityPacketMaxBytes               = 32768
	ArchitectureCapacityMutationFrameBytes           = 16384
	ArchitectureCapacityReplyMaxBytes                = ArchitectureCapacityPacketMaxBytes + ArchitectureCapacityMutationFrameBytes
	ArchitectureCapacityInvokeNonPacketBytes         = 1048576
	ArchitectureCapacityInvokeTotalMaxBytes          = ArchitectureCapacityPacketMaxBytes + ArchitectureCapacityInvokeNonPacketBytes
	ArchitectureCapacityTaskPromptMaxBytes           = 6 * ArchitectureCapacityPacketMaxBytes
	ArchitectureCapacityTaskPromptJSONStringMaxBytes = 6*ArchitectureCapacityTaskPromptMaxBytes + 2
)

type architectureCapacitySlot string

const (
	architectureSlotDesignRecord      architectureCapacitySlot = "design_record"
	architectureSlotWorkerJob         architectureCapacitySlot = "worker_job"
	architectureSlotWorkContext       architectureCapacitySlot = "work_context"
	architectureSlotLawContext        architectureCapacitySlot = "law_context"
	architectureSlotProposalRecord    architectureCapacitySlot = "proposal_record"
	architectureSlotWorkRecord        architectureCapacitySlot = "work_record"
	architectureSlotTaskPremise       architectureCapacitySlot = "task_premise"
	architectureSlotOutcomePredicates architectureCapacitySlot = "outcome_predicates"
)

var architectureCapacitySlotsInOrder = []architectureCapacitySlot{architectureSlotDesignRecord, architectureSlotWorkerJob, architectureSlotWorkContext, architectureSlotLawContext, architectureSlotProposalRecord, architectureSlotWorkRecord, architectureSlotTaskPremise, architectureSlotOutcomePredicates}

// The closed packet3 member inventory at its real locations; the
// work_context slot covers the present members among work_context,
// checkpoint, and correction.
var (
	architectureCapacityRootMembers = map[string]bool{
		"schema_version": true, "attempt_id": true, "lane_id": true, "lane_version": true,
		"lane_digest": true, "work_id": true, "step_id": true,
	}
	architectureCapacityStructuralInputs = map[string]bool{"binding": true, "report_protocol": true}
	architectureCapacitySlotForMember    = map[string]architectureCapacitySlot{
		"design_record": architectureSlotDesignRecord, "worker_job": architectureSlotWorkerJob,
		"work_context": architectureSlotWorkContext, "checkpoint": architectureSlotWorkContext,
		"correction": architectureSlotWorkContext, "law_context": architectureSlotLawContext,
		"proposal_record": architectureSlotProposalRecord, "work_record": architectureSlotWorkRecord,
		"task": architectureSlotTaskPremise, "outcome_predicates": architectureSlotOutcomePredicates,
	}
)

// The core-complete packet: the structural skeleton, the fixed inputs
// members, and the task and design record every architecture-capable
// dispatch carries; the next state alone adds the ready worker job.
var architectureCapacityRequiredCore = map[string]bool{
	"schema_version": true, "attempt_id": true, "lane_id": true, "lane_version": true,
	"lane_digest": true, "work_id": true, "step_id": true, "binding": true,
	"report_protocol": true, "task": true, "design_record": true,
}

func architectureCapacityRequired(nextWorkerJob bool) map[string]bool {
	required := make(map[string]bool, len(architectureCapacityRequiredCore)+1)
	for member := range architectureCapacityRequiredCore {
		required[member] = true
	}
	if nextWorkerJob {
		required["worker_job"] = true
	}
	return required
}

func architectureCapacityClassifyMember(key string) (architectureCapacitySlot, bool, bool) {
	if slot, ok := architectureCapacitySlotForMember[key]; ok {
		return slot, true, true
	}
	if architectureCapacityStructuralInputs[key] {
		return "", true, true
	}
	return "", false, architectureCapacityRootMembers[key]
}

func architectureCapacityFailure(kind FailureKind, detail, remedy string) error {
	return newFailure(kind, "workflow_architecture_capacity", detail, false, remedy)
}

func architectureCapacityOverage(name string, required, allowed int) error {
	return architectureCapacityFailure(KindLimitExceeded,
		fmt.Sprintf("architecture capacity %s requires %d canonical bytes against %d allowed; the packet is refused, never trimmed", name, required, allowed),
		"reduce the member through its owner, or reallocate reserves in a quiescent revise_design that preserves all retained inputs")
}

type ArchitectureCapacityProfile struct {
	reserves map[architectureCapacitySlot]int
}

// NewArchitectureCapacityProfile pins the declared eight-slot allocation. A
// nil capacity is a legacy pin: no profile exists there and the model stays
// inactive, because legacy definitions keep their larger individual bounds.
func NewArchitectureCapacityProfile(capacity *WorkflowDesignArchitectureCapacity) (ArchitectureCapacityProfile, error) {
	if capacity == nil {
		return ArchitectureCapacityProfile{}, architectureCapacityFailure(KindInvalidPayload,
			"architecture capacity is absent; a legacy pin carries no packet3 capacity profile and keeps its larger individual bounds",
			"pin an architecture-capable definition that declares the eight slot reserves before measuring packet capacity")
	}
	if err := validateDesignArchitectureCapacity(capacity); err != nil {
		return ArchitectureCapacityProfile{}, err
	}
	return ArchitectureCapacityProfile{reserves: map[architectureCapacitySlot]int{
		architectureSlotDesignRecord:      capacity.DesignRecord,
		architectureSlotWorkerJob:         capacity.WorkerJob,
		architectureSlotWorkContext:       capacity.WorkContext,
		architectureSlotLawContext:        capacity.LawContext,
		architectureSlotProposalRecord:    capacity.ProposalRecord,
		architectureSlotWorkRecord:        capacity.WorkRecord,
		architectureSlotTaskPremise:       capacity.TaskPremise,
		architectureSlotOutcomePredicates: capacity.OutcomePredicates,
	}}, nil
}

func (p ArchitectureCapacityProfile) Reserve(slot architectureCapacitySlot) int {
	return p.reserves[slot]
}

func (p ArchitectureCapacityProfile) VariableTotal() int {
	total := 0
	for _, slot := range architectureCapacitySlotsInOrder {
		total += p.reserves[slot]
	}
	return total
}

func (p ArchitectureCapacityProfile) PacketMax() int {
	return p.VariableTotal() + ArchitectureCapacityStructuralMaxBytes
}

// ArchitectureCapacityMeasurement reports one packet's canonical assembled
// bytes, the subtractive structural skeleton, and each slot's member bytes
// with all keys, delimiters, and escaping.
type ArchitectureCapacityMeasurement struct {
	Packet          json.RawMessage
	PacketBytes     int
	StructuralBytes int
	SlotBytes       map[architectureCapacitySlot]int
}

// assembleArchitecturePacket canonicalizes the present members, marshals
// the canonical packet object at the real member locations, and charges
// each slot its member spans: key, colon, quotes, value bytes, and the
// trailing comma every present member carries except the lexicographically
// last. The structural remainder is the subtractive whole-minus-slots
// skeleton. Every malformed or missing member refuses; none measures as
// zero.
func assembleArchitecturePacket(profile ArchitectureCapacityProfile, members map[string]json.RawMessage, nextWorkerJob bool) (json.RawMessage, int, map[architectureCapacitySlot]int, error) {
	refuse := func(detail, remedy string) (json.RawMessage, int, map[architectureCapacitySlot]int, error) {
		return nil, 0, nil, architectureCapacityFailure(KindInvalidPayload, detail, remedy)
	}
	if profile.reserves == nil {
		return refuse("no architecture capacity profile is pinned; a legacy pin never certifies a packet3 measurement",
			"pin the declared eight-slot allocation before measuring capacity")
	}
	required := architectureCapacityRequired(nextWorkerJob)
	inputs := make(map[string]json.RawMessage, len(members))
	root := make(map[string]json.RawMessage, len(architectureCapacityRootMembers)+1)
	inputKeys := make([]string, 0, len(members))
	slots := map[string]architectureCapacitySlot{}
	for key, raw := range members {
		slot, underInputs, known := architectureCapacityClassifyMember(key)
		if !known {
			return refuse(fmt.Sprintf("packet3 inputs member %q is outside the closed capacity inventory; inputs.context and inputs.constraints are not packet3 members and are never measured as zero", key),
				"emit only the closed packet3 members at their declared locations")
		}
		if len(raw) == 0 {
			return refuse(fmt.Sprintf("packet3 member %q carries no projection bytes; an unknown or unresolved member is never measured as zero", key),
				"resolve the member through its owning record before measuring capacity")
		}
		can, err := canonicalJSON(raw)
		if err != nil {
			return refuse(fmt.Sprintf("packet3 member %q cannot be canonicalized for capacity measurement", key),
				"supply the closed typed projection the packet schema declares")
		}
		delete(required, key)
		if underInputs {
			inputs[key] = can
			inputKeys = append(inputKeys, key)
			if slot != "" {
				slots[key] = slot
			}
		} else {
			root[key] = can
		}
	}
	for member := range required {
		return refuse(fmt.Sprintf("packet3 member %q is required by the capacity profile and is not present; a missing member is never measured as zero", member),
			"supply the complete packet the architecture-capable profile requires")
	}
	sort.Strings(inputKeys)
	inputsRaw, inputsErr := json.Marshal(inputs)
	root["inputs"] = inputsRaw
	packet, packetErr := json.Marshal(root)
	if inputsErr != nil || packetErr != nil {
		return nil, 0, nil, architectureCapacityFailure(KindInvariantViolation, "the packet3 projection cannot be measured", "repair the packet members")
	}
	slotBytes := make(map[architectureCapacitySlot]int, len(architectureCapacitySlotsInOrder))
	total := 0
	for i, key := range inputKeys {
		span := len(key) + 3 + len(inputs[key])
		if i < len(inputKeys)-1 {
			span++
		}
		if slot, ok := slots[key]; ok {
			slotBytes[slot] += span
			total += span
		}
	}
	return packet, len(packet) - total, slotBytes, nil
}

// CheckArchitecturePacketCapacity measures one packet projection and
// refuses the first over-allocation: any slot above its reserve, the
// structural skeleton above its slot, or the whole packet above the
// profile bound; nextWorkerJob also requires the ready worker job.
func CheckArchitecturePacketCapacity(profile ArchitectureCapacityProfile, members map[string]json.RawMessage, nextWorkerJob bool) (ArchitectureCapacityMeasurement, error) {
	packet, structural, slotBytes, err := assembleArchitecturePacket(profile, members, nextWorkerJob)
	if err != nil {
		return ArchitectureCapacityMeasurement{}, err
	}
	for _, slot := range architectureCapacitySlotsInOrder {
		if measured := slotBytes[slot]; measured > profile.Reserve(slot) {
			return ArchitectureCapacityMeasurement{}, architectureCapacityOverage("slot "+string(slot), measured, profile.Reserve(slot))
		}
	}
	if structural > ArchitectureCapacityStructuralMaxBytes {
		return ArchitectureCapacityMeasurement{}, architectureCapacityOverage("structural", structural, ArchitectureCapacityStructuralMaxBytes)
	}
	if len(packet) > profile.PacketMax() {
		return ArchitectureCapacityMeasurement{}, architectureCapacityOverage("packet", len(packet), profile.PacketMax())
	}
	return ArchitectureCapacityMeasurement{
		Packet: packet, PacketBytes: len(packet), StructuralBytes: structural, SlotBytes: slotBytes,
	}, nil
}

// The fixed actual packet paths: the retry reply carries the packet at
// result.retry_worker_packet; the invoke request nests it at
// input.fields.worker_packet.
var (
	architectureRetryPacketPath  = []string{"result", "retry_worker_packet"}
	architectureInvokePacketPath = []string{"input", "fields", "worker_packet"}
)

// ArchitectureCapacityReservation carries the measured projections: the
// present and finite next-state packet member inventories plus the
// mutation and invoke frame preimages, each with a null placeholder at the
// fixed packet path.
type ArchitectureCapacityReservation struct {
	Current       map[string]json.RawMessage
	Next          map[string]json.RawMessage
	MutationFrame json.RawMessage
	InvokeFrame   json.RawMessage
}

// ArchitectureCapacityCertificate is one measured reservation: both packet
// projections, both frames with the next packet substituted, and the
// law-context digest of the measured current members.
type ArchitectureCapacityCertificate struct {
	Packet               json.RawMessage
	PacketBytes          int
	StructuralBytes      int
	SlotBytes            map[architectureCapacitySlot]int
	NextPacket           json.RawMessage
	NextPacketBytes      int
	RetryReply           json.RawMessage
	RetryReplyBytes      int
	MutationFrameBytes   int
	InvokeRequest        json.RawMessage
	InvokeRequestBytes   int
	InvokeNonPacketBytes int
	LawContextDigest     string
}

// architectureFrameSubstitutePacket replaces the null placeholder at the
// fixed path with the measured packet as an object, splicing every level
// back up the chain; a malformed, missing, or non-null placeholder frame
// refuses and never measures as zero.
func architectureFrameSubstitutePacket(frame json.RawMessage, path []string, packet json.RawMessage) (json.RawMessage, error) {
	refuse := func(detail string) (json.RawMessage, error) {
		return nil, architectureCapacityFailure(KindInvalidPayload, detail,
			"supply the complete frame preimage with a null placeholder at the fixed packet path")
	}
	whole, err := canonicalJSON(frame)
	if err != nil {
		return refuse("frame preimage cannot be canonicalized for capacity measurement")
	}
	objects := make([]map[string]json.RawMessage, 0, len(path))
	for i, key := range path {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(whole, &object); err != nil || object == nil {
			return refuse(fmt.Sprintf("frame preimage carries no object at %q on the fixed packet path", key))
		}
		value, ok := object[key]
		if !ok {
			return refuse(fmt.Sprintf("frame preimage lacks %q on the fixed packet path", key))
		}
		objects = append(objects, object)
		if i == len(path)-1 {
			if string(value) != "null" {
				return refuse(fmt.Sprintf("frame preimage placeholder at %q is not the null packet marker", key))
			}
			object[key] = packet
		} else {
			whole = value
		}
	}
	for i := len(objects) - 1; i > 0; i-- {
		encoded, err := json.Marshal(objects[i])
		if err != nil {
			return refuse("frame preimage cannot encode the measured packet substitution")
		}
		objects[i-1][path[i-1]] = encoded
	}
	return json.Marshal(objects[0])
}

// MeasureArchitectureCapacityReservation measures the actual current and
// finite next-state packet projections plus both complete frame preimages
// with the next packet substituted at the fixed paths: the frame excluding
// the returned packet stays within the mutation reservation, the whole
// retry reply within the reply bound, and the invoke request within its
// non-packet reservation and total bound.
func MeasureArchitectureCapacityReservation(profile ArchitectureCapacityProfile, reservation ArchitectureCapacityReservation) (ArchitectureCapacityCertificate, error) {
	current, err := CheckArchitecturePacketCapacity(profile, reservation.Current, false)
	if err != nil {
		return ArchitectureCapacityCertificate{}, err
	}
	next, err := CheckArchitecturePacketCapacity(profile, reservation.Next, true)
	if err != nil {
		return ArchitectureCapacityCertificate{}, err
	}
	retryReply, err := architectureFrameSubstitutePacket(reservation.MutationFrame, architectureRetryPacketPath, next.Packet)
	if err != nil {
		return ArchitectureCapacityCertificate{}, err
	}
	mutationFrame := len(retryReply) - next.PacketBytes
	if mutationFrame > ArchitectureCapacityMutationFrameBytes {
		return ArchitectureCapacityCertificate{}, architectureCapacityOverage("mutation frame", mutationFrame, ArchitectureCapacityMutationFrameBytes)
	}
	if len(retryReply) > ArchitectureCapacityReplyMaxBytes {
		return ArchitectureCapacityCertificate{}, architectureCapacityOverage("retry reply", len(retryReply), ArchitectureCapacityReplyMaxBytes)
	}
	invokeRequest, err := architectureFrameSubstitutePacket(reservation.InvokeFrame, architectureInvokePacketPath, next.Packet)
	if err != nil {
		return ArchitectureCapacityCertificate{}, err
	}
	invokeNonPacket := len(invokeRequest) - next.PacketBytes
	if invokeNonPacket > ArchitectureCapacityInvokeNonPacketBytes {
		return ArchitectureCapacityCertificate{}, architectureCapacityOverage("invoke frame", invokeNonPacket, ArchitectureCapacityInvokeNonPacketBytes)
	}
	if len(invokeRequest) > ArchitectureCapacityInvokeTotalMaxBytes {
		return ArchitectureCapacityCertificate{}, architectureCapacityOverage("invoke request", len(invokeRequest), ArchitectureCapacityInvokeTotalMaxBytes)
	}
	digest, err := ArchitectureLawContextDigest(reservation.Current["law_context"])
	if err != nil {
		return ArchitectureCapacityCertificate{}, err
	}
	return ArchitectureCapacityCertificate{
		Packet: current.Packet, PacketBytes: current.PacketBytes,
		StructuralBytes: current.StructuralBytes, SlotBytes: current.SlotBytes,
		NextPacket: next.Packet, NextPacketBytes: next.PacketBytes,
		RetryReply: retryReply, RetryReplyBytes: len(retryReply), MutationFrameBytes: mutationFrame,
		InvokeRequest: invokeRequest, InvokeRequestBytes: len(invokeRequest), InvokeNonPacketBytes: invokeNonPacket,
		LawContextDigest: digest,
	}, nil
}

// ArchitectureLawContextDigest binds the exact canonical law projection: a
// present member digests its canonical bytes, an absent member the fixed
// absence token "law_context_absent", which no present projection collides
// with; one that cannot canonicalize refuses instead of digesting as zero.
func ArchitectureLawContextDigest(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		sum := sha256.Sum256([]byte("law_context_absent"))
		return fmt.Sprintf("%x", sum), nil
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return "", architectureCapacityFailure(KindInvalidPayload,
			"law_context projection cannot be canonicalized for the capacity certificate; an unknown state is never digested as zero",
			"supply the closed typed law_context projection the packet schema declares")
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", sum), nil
}
