package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

// LaneDefinition is the generated, immutable contract for one worker lane.
type LaneDefinition struct {
	ID                  string
	Version             int64
	Digest              string
	Purpose             string
	CapabilityClass     string
	Capabilities        []string
	PacketSchemaRef     string
	ReportSchemaRef     string
	Budgets             LaneBudgets
	EvidenceObligations []string
	LifecycleStates     []string
	PinnedModel         string
}

type LaneBudgets struct {
	CostUSDMax       float64 `json:"cost_usd_max"`
	ContextTokensMax int64   `json:"context_tokens_max"`
	TimeSecondsMax   int64   `json:"time_seconds_max"`
}

// LaneRegistry is a closed lookup of generated lane definitions.
type LaneRegistry struct {
	entries map[string]LaneDefinition
}

var laneIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
var laneRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,127}$`)
var laneDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var laneModelPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*/[^/ ]+$`)

func laneKey(id string, version int64) string { return fmt.Sprintf("%s:%d", id, version) }

// NewBuiltinLaneRegistry validates and loads only generated definitions.
func NewBuiltinLaneRegistry() LaneRegistry {
	entries := make(map[string]LaneDefinition, len(generatedLaneDefinitions))
	for _, definition := range generatedLaneDefinitions {
		if err := ValidateLaneDefinition(definition); err != nil {
			panic(err)
		}
		entries[laneKey(definition.ID, definition.Version)] = cloneLaneDefinition(definition)
	}
	return LaneRegistry{entries: entries}
}

var builtinLaneRegistry = NewBuiltinLaneRegistry()

func BuiltinLaneRegistry() LaneRegistry { return builtinLaneRegistry }

func BuiltinLaneDefinitions() []LaneDefinition {
	result := make([]LaneDefinition, 0, len(generatedLaneDefinitions))
	for _, definition := range generatedLaneDefinitions {
		result = append(result, cloneLaneDefinition(definition))
	}
	return result
}

// Lookup returns a lane only when all three identity components match. A
// missing id/version and a mismatched digest are deliberately different typed
// failures so callers can distinguish an unsupported contract from drift.
func (r LaneRegistry) Lookup(id string, version int64, digest string) (LaneDefinition, error) {
	definition, ok := r.entries[laneKey(id, version)]
	if !ok {
		return LaneDefinition{}, newFailure(KindLaneDefinitionNotRegistered, "lane_registry", "lane identity is not registered", false, "select a registered lane identity")
	}
	if definition.Digest != digest || !laneDigestPattern.MatchString(digest) {
		return LaneDefinition{}, newFailure(KindLaneDefinitionDigestMismatch, "lane_registry", "lane contract digest does not match the registered definition", false, "reread the lane registry and retry with its digest")
	}
	return cloneLaneDefinition(definition), nil
}

func LookupLane(id string, version int64, digest string) (LaneDefinition, error) {
	return builtinLaneRegistry.Lookup(id, version, digest)
}

func ValidateLaneDefinition(definition LaneDefinition) error {
	if !validLaneID(definition.ID) || definition.Version < 1 || definition.Version > 2147483647 || !laneDigestPattern.MatchString(definition.Digest) {
		return newFailure(KindLaneDefinitionInvalid, "lane_registry", "lane identity is invalid", false, "repair the generated lane manifest")
	}
	if len(definition.Purpose) < 2 || len(definition.Purpose) > 512 || !laneRefPattern.MatchString(definition.CapabilityClass) || len(definition.Capabilities) < 1 || len(definition.Capabilities) > 16 || !uniqueBoundedLaneStrings(definition.Capabilities, 64) {
		return newFailure(KindLaneDefinitionInvalid, "lane_registry", "lane purpose or capability set is invalid", false, "repair the generated lane manifest")
	}
	if definition.PacketSchemaRef != "agent-lane-packet.v1" || definition.ReportSchemaRef != "agent-lane-report.v1" || !laneModelPattern.MatchString(definition.PinnedModel) {
		return newFailure(KindLaneDefinitionInvalid, "lane_registry", "lane schema reference or pinned model is invalid", false, "pin a valid packet/report schema and model")
	}
	if definition.Budgets.CostUSDMax <= 0 || definition.Budgets.CostUSDMax > 1000 || definition.Budgets.ContextTokensMax < 1 || definition.Budgets.ContextTokensMax > 1000000 || definition.Budgets.TimeSecondsMax < 1 || definition.Budgets.TimeSecondsMax > 86400 {
		return newFailure(KindLaneDefinitionInvalid, "lane_registry", "lane budgets are outside accepted bounds", false, "repair the generated lane manifest")
	}
	if len(definition.EvidenceObligations) < 1 || len(definition.EvidenceObligations) > 16 || !uniqueBoundedLaneStrings(definition.EvidenceObligations, 64) || len(definition.LifecycleStates) < 3 || len(definition.LifecycleStates) > 4 || !sameLaneStrings(definition.LifecycleStates, []string{"dispatched", "completed", "failed"}) {
		return newFailure(KindLaneDefinitionInvalid, "lane_registry", "lane evidence or lifecycle contract is invalid", false, "repair the generated lane manifest")
	}
	computed, err := LaneDefinitionDigest(definition)
	if err != nil || computed != definition.Digest {
		return newFailure(KindLaneDefinitionDigestMismatch, "lane_registry", "lane definition digest does not match its canonical content", false, "regenerate the lane registry projections")
	}
	return nil
}

func validLaneID(value string) bool {
	switch value {
	case "research", "implement", "review", "verify":
		return true
	default:
		return false
	}
}

func LaneDefinitionDigest(definition LaneDefinition) (string, error) {
	body := map[string]any{
		"id": definition.ID, "version": definition.Version, "purpose": definition.Purpose,
		"capability_class": definition.CapabilityClass, "capabilities": nonNilStrings(definition.Capabilities),
		"packet_schema_ref": definition.PacketSchemaRef, "report_schema_ref": definition.ReportSchemaRef,
		"budgets":              map[string]any{"cost_usd_max": definition.Budgets.CostUSDMax, "context_tokens_max": definition.Budgets.ContextTokensMax, "time_seconds_max": definition.Budgets.TimeSecondsMax},
		"evidence_obligations": nonNilStrings(definition.EvidenceObligations), "lifecycle_states": nonNilStrings(definition.LifecycleStates),
		"pinned_model": definition.PinnedModel,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneLaneDefinition(definition LaneDefinition) LaneDefinition {
	definition.Capabilities = append([]string(nil), definition.Capabilities...)
	definition.EvidenceObligations = append([]string(nil), definition.EvidenceObligations...)
	definition.LifecycleStates = append([]string(nil), definition.LifecycleStates...)
	return definition
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func uniqueBoundedLaneStrings(values []string, maxLength int) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !laneRefPattern.MatchString(value) || len(value) > maxLength {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func sameLaneStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, value := range want {
		found := false
		for _, candidate := range got {
			if candidate == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
