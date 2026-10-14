package store

import "encoding/json"

// recordedPayloadShape is derived only from a retained event's closed schema.
// It is not persisted or accepted from a write caller.
type recordedPayloadShape uint8

const (
	recordedShapeCurrent recordedPayloadShape = iota
	recordedShapeLegacyClaimLanding
	recordedShapeLegacyNativeRun
)

type recordedPayloadSchema struct {
	matches func(map[string]json.RawMessage) bool
	adapt   func(Event) (Event, error)
}

func withRecordedSchemas(registration EventKindRegistration, schemas map[int][]recordedPayloadSchema) EventKindRegistration {
	registration.RecordedSchemas = schemas
	return registration
}

func recordedIdentity(event Event) (Event, error) { return event, nil }

func recordedHasAny(payload map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, exists := payload[key]; exists {
			return true
		}
	}
	return false
}

func recordedWorkerFailureSchemas() map[int][]recordedPayloadSchema {
	return map[int][]recordedPayloadSchema{1: {
		{matches: func(p map[string]json.RawMessage) bool { return recordedHasAny(p, "observed_session_directories") }, adapt: adaptRecordedWorkerFailure},
		{matches: func(p map[string]json.RawMessage) bool { return !recordedHasAny(p, "observed_session_directories") }, adapt: recordedIdentity},
	}}
}

func recordedDispatchSchemas() map[int][]recordedPayloadSchema {
	routed := func(p map[string]json.RawMessage) bool {
		return recordedHasAny(p, "routing_policy_version", "routing_policy_digest", "resolved_model", "resolution_role", "fallback_reason")
	}
	old := func(p map[string]json.RawMessage) bool {
		var model string
		return routed(p) && json.Unmarshal(p["resolved_model"], &model) == nil && model != ""
	}
	schemas := map[int][]recordedPayloadSchema{}
	for _, version := range []int{1, 2} {
		schemas[version] = []recordedPayloadSchema{
			{matches: old, adapt: adaptRecordedDispatch},
			{matches: func(p map[string]json.RawMessage) bool { return !routed(p) }, adapt: recordedIdentity},
		}
	}
	schemas[3] = []recordedPayloadSchema{
		{matches: old, adapt: adaptRecordedDispatch},
		{matches: matchesRecordedReadbackDispatch, adapt: adaptRecordedReadbackDispatch},
		{matches: func(p map[string]json.RawMessage) bool {
			return !routed(p) && recordedHasAny(p, "packet_digest")
		}, adapt: recordedIdentity},
	}
	return schemas
}

func matchesRecordedReadbackDispatch(payload map[string]json.RawMessage) bool {
	for _, key := range []string{"attempt_id", "lane_id", "lane_version", "lane_digest", "capability_class", "packet_schema_version", "report_schema_version", "host_provenance"} {
		if !recordedHasAny(payload, key) {
			return false
		}
	}
	for key, value := range payload {
		switch key {
		case "attempt_id", "lane_id", "lane_version", "lane_digest", "capability_class", "packet_schema_version", "report_schema_version", "host_provenance",
			"readback_model", "terminal", "terminal_failure_kind", "terminal_detail":
			if string(value) == "null" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func adaptRecordedReadbackDispatch(event Event) (Event, error) {
	var payload WorkerDispatchedPayload
	if err := decodeRegisteredPayload(event, &payload); err != nil {
		return Event{}, err
	}
	if payload.PacketSchemaVersion != WorkerPacketSchemaVersionLegacy || payload.ReportSchemaVersion != WorkerReportSchemaVersionLegacy {
		return Event{}, invalidWorkerPayload("recorded readback dispatch requires the released 1.0 schema pair")
	}
	// The readback-era v3 writer predates packet digests. Use the same
	// unavailable marker as routed v3 dispatches, without changing its header.
	return upcastWorkerDispatchedV2(event)
}

func recordedOccupancyReleaseSchemas() map[int][]recordedPayloadSchema {
	return map[int][]recordedPayloadSchema{1: {
		{matches: func(p map[string]json.RawMessage) bool {
			return recordedHasAny(p, "occupant_session_ref") && !recordedHasAny(p, "session_ref")
		}, adapt: adaptRecordedOccupancyRelease},
		{matches: func(p map[string]json.RawMessage) bool {
			return recordedHasAny(p, "session_ref") && !recordedHasAny(p, "occupant_session_ref")
		}, adapt: recordedIdentity},
	}}
}

func recordedClaimLandingSchemas() map[int][]recordedPayloadSchema {
	return map[int][]recordedPayloadSchema{1: {
		{matches: func(p map[string]json.RawMessage) bool {
			return !recordedHasAny(p, "host_pid", "host_pid_start", "claim_op_id")
		}, adapt: adaptRecordedClaimLanding},
		{matches: func(p map[string]json.RawMessage) bool {
			_, pid := p["host_pid"]
			_, start := p["host_pid_start"]
			return pid && start
		}, adapt: recordedIdentity},
	}}
}

func recordedNativeRunSchemas() map[int][]recordedPayloadSchema {
	oldUniverse := func(p map[string]json.RawMessage) bool {
		var universe map[string]json.RawMessage
		return json.Unmarshal(p["observed_universe"], &universe) == nil && recordedHasAny(universe, "anchor", "total")
	}
	return map[int][]recordedPayloadSchema{1: {
		{matches: func(p map[string]json.RawMessage) bool { return oldUniverse(p) && !recordedHasAny(p, "observation_id") }, adapt: adaptRecordedNativeRun},
		{matches: func(p map[string]json.RawMessage) bool { return !oldUniverse(p) && recordedHasAny(p, "observation_id") }, adapt: recordedIdentity},
	}}
}

// The three v1 kinds gained incompatible shapes before their writers advanced
// the header. Their current-shaped v1 records require only a version stamp.
func upcastRecordedShapeV1(event Event) (Event, error) {
	event.PayloadVersion = 2
	return event, nil
}

func adaptRecordedClaimLanding(event Event) (Event, error) {
	if err := validateLegacySessionClaimLandedPayload(event); err != nil {
		return Event{}, err
	}
	event.recordedPayloadShape = recordedShapeLegacyClaimLanding
	return event, nil
}

func adaptRecordedNativeRun(event Event) (Event, error) {
	if err := validateLegacyNativeRunPayload(event); err != nil {
		return Event{}, err
	}
	event.recordedPayloadShape = recordedShapeLegacyNativeRun
	return event, nil
}

type recordedSessionDirectory struct {
	SessionRef string `json:"session_ref"`
	Directory  string `json:"directory"`
}

type recordedWorkerFailure struct {
	AttemptID                  string                      `json:"attempt_id"`
	ReadbackModel              string                      `json:"readback_model"`
	FailureKind                string                      `json:"failure_kind"`
	Detail                     string                      `json:"detail"`
	ObservedSessionDirectories *[]recordedSessionDirectory `json:"observed_session_directories,omitempty"`
}

func adaptRecordedWorkerFailure(event Event) (Event, error) {
	var old recordedWorkerFailure
	if err := decodeRegisteredPayload(event, &old); err != nil {
		return Event{}, err
	}
	if old.FailureKind == WorkerFailureAbandoned {
		if old.ObservedSessionDirectories == nil {
			return Event{}, invalidWorkerPayload("abandoned worker failure requires the recorded host session observation")
		}
		for _, directory := range *old.ObservedSessionDirectories {
			if directory.SessionRef == "" || directory.Directory == "" {
				return Event{}, invalidWorkerPayload("abandoned worker failure has an invalid recorded host session observation")
			}
		}
	}
	payload := WorkerFailedPayload{AttemptID: old.AttemptID, ReadbackModel: old.ReadbackModel, FailureKind: old.FailureKind, Detail: old.Detail}
	if err := validateWorkerFailedPayload(event, payload); err != nil {
		return Event{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	event.Payload = raw
	return event, nil
}

// Routing metadata belongs to the recorded dispatch schema, not a current
// model assertion.
type recordedRoutingDispatch struct {
	AttemptID            string                `json:"attempt_id"`
	LaneID               string                `json:"lane_id"`
	LaneVersion          int64                 `json:"lane_version"`
	LaneDigest           string                `json:"lane_digest"`
	CapabilityClass      string                `json:"capability_class"`
	RoutingPolicyVersion string                `json:"routing_policy_version"`
	RoutingPolicyDigest  *string               `json:"routing_policy_digest,omitempty"`
	ResolvedModel        string                `json:"resolved_model"`
	ResolutionRole       *string               `json:"resolution_role,omitempty"`
	FallbackReason       *string               `json:"fallback_reason,omitempty"`
	PacketSchemaVersion  string                `json:"packet_schema_version"`
	ReportSchemaVersion  string                `json:"report_schema_version"`
	HostProvenance       *WorkerHostProvenance `json:"host_provenance,omitempty"`
	Terminal             string                `json:"terminal,omitempty"`
	TerminalFailureKind  string                `json:"terminal_failure_kind,omitempty"`
	TerminalDetail       string                `json:"terminal_detail,omitempty"`
}

func adaptRecordedDispatch(event Event) (Event, error) {
	var old recordedRoutingDispatch
	if err := decodeRegisteredPayload(event, &old); err != nil {
		return Event{}, err
	}
	if !workerVersionPattern.MatchString(old.RoutingPolicyVersion) || !workerModelPattern.MatchString(old.ResolvedModel) {
		return Event{}, invalidWorkerPayload("recorded worker dispatch has invalid routing identity")
	}
	if event.PayloadVersion == 1 {
		if old.RoutingPolicyDigest != nil || old.ResolutionRole != nil || old.FallbackReason != nil || old.HostProvenance != nil || old.Terminal != "" {
			return Event{}, invalidWorkerPayload("recorded v1 routing dispatch contains later-version members")
		}
	} else {
		if old.RoutingPolicyDigest == nil || !laneDigestPattern.MatchString(*old.RoutingPolicyDigest) || old.ResolutionRole == nil || old.FallbackReason == nil {
			return Event{}, invalidWorkerPayload("recorded routing dispatch is missing its resolution identity")
		}
		switch *old.ResolutionRole {
		case "preferred":
			if *old.FallbackReason != "" || old.Terminal != "" {
				return Event{}, invalidWorkerPayload("recorded preferred dispatch has fallback or terminal evidence")
			}
		case "fallback":
			switch *old.FallbackReason {
			case "rate_limit", "provider_unavailable", "budget_exhausted", "other":
			default:
				return Event{}, invalidWorkerPayload("recorded fallback dispatch has invalid reason")
			}
		case "undeclared":
			if old.Terminal != "failed" || old.TerminalFailureKind != WorkerFailureModelIdentity {
				return Event{}, invalidWorkerPayload("recorded undeclared dispatch must be a terminal model identity mismatch")
			}
		default:
			return Event{}, invalidWorkerPayload("recorded routing dispatch has invalid resolution role")
		}
	}
	if event.PayloadVersion < 3 && old.HostProvenance != nil {
		return Event{}, invalidWorkerPayload("recorded routing dispatch provenance is reserved for version 3")
	}
	// PM3 rebuild agrees with the projection carried by forward migrations:
	// the released routing fold seeded this column from the recorded model.
	payload := WorkerDispatchedPayload{
		AttemptID: old.AttemptID, LaneID: old.LaneID, LaneVersion: old.LaneVersion, LaneDigest: old.LaneDigest,
		CapabilityClass: old.CapabilityClass, PacketSchemaVersion: old.PacketSchemaVersion, ReportSchemaVersion: old.ReportSchemaVersion,
		HostProvenance: old.HostProvenance, Terminal: old.Terminal, TerminalFailureKind: old.TerminalFailureKind, TerminalDetail: old.TerminalDetail,
		ReadbackModel: old.ResolvedModel,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	event.Payload = raw
	if event.PayloadVersion == 3 {
		// These recorded v3 dispatches predate packet digests. The existing
		// pre-packet normalization owns the unavailable marker; retain the
		// actual source header so its later fold guards still see version 3.
		normalized, err := upcastWorkerDispatchedV2(event)
		if err != nil {
			return Event{}, err
		}
		event.Payload = normalized.Payload
	}
	return event, nil
}

type recordedOccupancyRelease struct {
	SetID              string `json:"set_id"`
	ProjectID          string `json:"project_id"`
	ClaimOpID          string `json:"claim_op_id"`
	ClaimIncarnation   int    `json:"claim_incarnation,omitempty"`
	OccupantSessionRef string `json:"occupant_session_ref"`
}

func adaptRecordedOccupancyRelease(event Event) (Event, error) {
	var old recordedOccupancyRelease
	if err := decodeRegisteredPayload(event, &old); err != nil {
		return Event{}, err
	}
	if old.SetID == "" || old.ProjectID == "" || old.ClaimOpID == "" || old.OccupantSessionRef == "" {
		return Event{}, newFailure(KindInvalidPayload, "validate_event", "recorded occupancy release is missing required identity", false, "restore a valid recorded release")
	}
	raw, err := json.Marshal(worktreeOccupancyReleasedPayload{SetID: old.SetID, ProjectID: old.ProjectID, ClaimOpID: old.ClaimOpID, ClaimIncarnation: old.ClaimIncarnation, SessionRef: old.OccupantSessionRef})
	if err != nil {
		return Event{}, err
	}
	event.Payload = raw
	return event, nil
}
