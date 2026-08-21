package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// RoutingPolicyDefinition is the generated, immutable resolution policy for a
// capability class. ResolutionSet is ordered: the preferred model is first.
type RoutingPolicyDefinition struct {
	CapabilityClass string
	PreferredModel  string
	ResolutionSet   []string
}

type RoutingPolicyRegistry struct {
	entries map[string]RoutingPolicyDefinition
	version string
	digest  string
	source  string
}

var routingPolicyVersionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var routingPolicyModelPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*/[^/ ]+$`)

func routingPolicyKey(capabilityClass, version string) string { return capabilityClass + ":" + version }

func NewBuiltinRoutingPolicyRegistry() RoutingPolicyRegistry {
	registry, err := newRoutingPolicyRegistry(defaultRoutingPolicyDocument(), "default routing-policy template")
	if err != nil {
		panic(err)
	}
	return registry
}

var builtinRoutingPolicyRegistry = NewBuiltinRoutingPolicyRegistry()
var activeRoutingPolicyRegistry = builtinRoutingPolicyRegistry
var activeRoutingPolicyMu sync.RWMutex

const routingPolicySourceEnv = "CONCORD_ROUTING_POLICY"

type routingPolicyDocument struct {
	SchemaVersion *string                       `json:"schema_version"`
	Registry      *string                       `json:"registry"`
	Version       *string                       `json:"version"`
	Policies      *[]routingPolicyDocumentEntry `json:"policies"`
}

type routingPolicyDocumentEntry struct {
	CapabilityClass *string   `json:"capability_class"`
	PreferredModel  *string   `json:"preferred_model"`
	ResolutionSet   *[]string `json:"resolution_set"`
}

func defaultRoutingPolicyDocument() routingPolicyDocument {
	version := RoutingPolicyVersion
	registry := "routing_policy"
	schemaVersion := "1.0"
	entries := make([]routingPolicyDocumentEntry, 0, len(generatedRoutingPolicies))
	for _, policy := range generatedRoutingPolicies {
		capabilityClass, preferredModel := policy.CapabilityClass, policy.PreferredModel
		resolutionSet := append([]string(nil), policy.ResolutionSet...)
		entries = append(entries, routingPolicyDocumentEntry{CapabilityClass: &capabilityClass, PreferredModel: &preferredModel, ResolutionSet: &resolutionSet})
	}
	return routingPolicyDocument{SchemaVersion: &schemaVersion, Registry: &registry, Version: &version, Policies: &entries}
}

// LoadRoutingPolicyRegistry resolves the process routing policy from host state
// or the embedded default template. A set host path is never silently ignored.
func LoadRoutingPolicyRegistry() (RoutingPolicyRegistry, error) {
	source := "default routing-policy template"
	document := defaultRoutingPolicyDocument()
	if path := os.Getenv(routingPolicySourceEnv); path != "" {
		if !filepath.IsAbs(path) {
			return RoutingPolicyRegistry{}, newFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("%s must be an absolute path: %s", routingPolicySourceEnv, path), false, "set CONCORD_ROUTING_POLICY to an absolute readable JSON path")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return RoutingPolicyRegistry{}, wrapFailure(KindUnavailable, "routing_policy_load", fmt.Sprintf("cannot read routing policy %s", path), false, "repair or remove CONCORD_ROUTING_POLICY", err)
		}
		document = routingPolicyDocument{}
		if err := decodeRoutingPolicyDocument(raw, &document); err != nil {
			return RoutingPolicyRegistry{}, wrapFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("routing policy %s is invalid", path), false, "repair the host routing-policy JSON", err)
		}
		source = path
	}
	return newRoutingPolicyRegistry(document, source)
}

func decodeRoutingPolicyDocument(raw []byte, document *routingPolicyDocument) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(document); err != nil {
		return fmt.Errorf("document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("document contains trailing JSON")
	}
	return nil
}

func newRoutingPolicyRegistry(document routingPolicyDocument, source string) (RoutingPolicyRegistry, error) {
	if document.SchemaVersion == nil {
		return RoutingPolicyRegistry{}, routingPolicyFieldError(source, "schema_version")
	}
	if document.Registry == nil {
		return RoutingPolicyRegistry{}, routingPolicyFieldError(source, "registry")
	}
	if document.Version == nil {
		return RoutingPolicyRegistry{}, routingPolicyFieldError(source, "version")
	}
	if document.Policies == nil {
		return RoutingPolicyRegistry{}, routingPolicyFieldError(source, "policies")
	}
	if *document.SchemaVersion != "1.0" {
		return RoutingPolicyRegistry{}, routingPolicyValueError(source, "schema_version")
	}
	if *document.Registry != "routing_policy" {
		return RoutingPolicyRegistry{}, routingPolicyValueError(source, "registry")
	}
	if !routingPolicyVersionPattern.MatchString(*document.Version) {
		return RoutingPolicyRegistry{}, routingPolicyValueError(source, "version")
	}
	if len(*document.Policies) == 0 || len(*document.Policies) > 8 {
		return RoutingPolicyRegistry{}, routingPolicyValueError(source, "policies")
	}

	entries := make(map[string]RoutingPolicyDefinition, len(*document.Policies))
	for index, entry := range *document.Policies {
		field := func(name string) error {
			return routingPolicyFieldError(source, fmt.Sprintf("policies[%d].%s", index, name))
		}
		if entry.CapabilityClass == nil {
			return RoutingPolicyRegistry{}, field("capability_class")
		}
		if entry.PreferredModel == nil {
			return RoutingPolicyRegistry{}, field("preferred_model")
		}
		if entry.ResolutionSet == nil {
			return RoutingPolicyRegistry{}, field("resolution_set")
		}
		policy := RoutingPolicyDefinition{CapabilityClass: *entry.CapabilityClass, PreferredModel: *entry.PreferredModel, ResolutionSet: append([]string(nil), (*entry.ResolutionSet)...)}
		if err := ValidateRoutingPolicyDefinition(policy); err != nil {
			return RoutingPolicyRegistry{}, wrapFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("%s policy %s is invalid", source, policy.CapabilityClass), false, "repair the host routing-policy JSON", err)
		}
		if _, exists := entries[policy.CapabilityClass]; exists {
			return RoutingPolicyRegistry{}, routingPolicyClassError(source, policy.CapabilityClass, "appears more than once")
		}
		entries[policy.CapabilityClass] = policy
	}

	laneClasses := make(map[string]struct{})
	for _, lane := range generatedLaneDefinitions {
		laneClasses[lane.CapabilityClass] = struct{}{}
	}
	for capabilityClass := range entries {
		if _, known := laneClasses[capabilityClass]; !known {
			return RoutingPolicyRegistry{}, routingPolicyClassError(source, capabilityClass, "is not present in the lane registry")
		}
	}
	for capabilityClass := range laneClasses {
		if _, covered := entries[capabilityClass]; !covered {
			return RoutingPolicyRegistry{}, routingPolicyClassError(source, capabilityClass, "is missing from the policy")
		}
	}

	digest, err := routingPolicyDigest(document)
	if err != nil {
		return RoutingPolicyRegistry{}, wrapFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("cannot digest routing policy %s", source), false, "repair the host routing-policy JSON", err)
	}
	registry := RoutingPolicyRegistry{entries: make(map[string]RoutingPolicyDefinition, len(entries)), version: *document.Version, digest: digest, source: source}
	for capabilityClass, policy := range entries {
		registry.entries[routingPolicyKey(capabilityClass, registry.version)] = cloneRoutingPolicyDefinition(policy)
	}
	return registry, nil
}

func routingPolicyDigest(document routingPolicyDocument) (string, error) {
	policies := make([]any, 0, len(*document.Policies))
	for _, entry := range *document.Policies {
		policies = append(policies, map[string]any{"capability_class": *entry.CapabilityClass, "preferred_model": *entry.PreferredModel, "resolution_set": *entry.ResolutionSet})
	}
	body := map[string]any{"schema_version": *document.SchemaVersion, "registry": *document.Registry, "version": *document.Version, "policies": policies}
	var encodedBuffer bytes.Buffer
	encoder := json.NewEncoder(&encodedBuffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return "", err
	}
	encoded := bytes.TrimSpace(encodedBuffer.Bytes())
	if len(encoded) == 0 {
		return "", fmt.Errorf("empty canonical policy")
	}
	return digestBytes(encoded), nil
}

func digestBytes(value []byte) string { return "sha256:" + fmt.Sprintf("%x", sha256Sum(value)) }

func sha256Sum(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func routingPolicyFieldError(source, field string) error {
	return newFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("%s is missing required field %s", source, field), false, "repair the host routing-policy JSON")
}

func routingPolicyValueError(source, field string) error {
	return newFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("%s has an invalid %s field", source, field), false, "repair the host routing-policy JSON")
}

func routingPolicyClassError(source, capabilityClass, detail string) error {
	return newFailure(KindRoutingPolicyInvalid, "routing_policy_load", fmt.Sprintf("%s capability class %q %s", source, capabilityClass, detail), false, "cover each lane capability class exactly once")
}

func setActiveRoutingPolicyRegistry(registry RoutingPolicyRegistry) {
	activeRoutingPolicyMu.Lock()
	activeRoutingPolicyRegistry = registry
	activeRoutingPolicyMu.Unlock()
}

func LoadedRoutingPolicyManifestDigest() string {
	activeRoutingPolicyMu.RLock()
	defer activeRoutingPolicyMu.RUnlock()
	return activeRoutingPolicyRegistry.digest
}

func BuiltinRoutingPolicies() []RoutingPolicyDefinition {
	result := make([]RoutingPolicyDefinition, 0, len(generatedRoutingPolicies))
	for _, policy := range generatedRoutingPolicies {
		result = append(result, cloneRoutingPolicyDefinition(policy))
	}
	return result
}

func (r RoutingPolicyRegistry) Lookup(capabilityClass, version, digest string) (RoutingPolicyDefinition, error) {
	policy, ok := r.entries[routingPolicyKey(capabilityClass, version)]
	if !ok {
		return RoutingPolicyDefinition{}, newFailure(KindRoutingPolicyNotRegistered, "routing_policy", "capability class or policy version is not registered", false, "select a registered routing policy")
	}
	if digest != r.digest || !laneDigestPattern.MatchString(digest) {
		return RoutingPolicyDefinition{}, newFailure(KindRoutingPolicyDigestMismatch, "routing_policy", "routing policy digest does not match the registered definition", false, "reread the routing policy registry and retry with its digest")
	}
	return cloneRoutingPolicyDefinition(policy), nil
}

func LookupRoutingPolicy(capabilityClass, version, digest string) (RoutingPolicyDefinition, error) {
	activeRoutingPolicyMu.RLock()
	defer activeRoutingPolicyMu.RUnlock()
	return activeRoutingPolicyRegistry.Lookup(capabilityClass, version, digest)
}

func ValidateRoutingPolicyDefinition(policy RoutingPolicyDefinition) error {
	if !laneRefPattern.MatchString(policy.CapabilityClass) || !routingPolicyModelPattern.MatchString(policy.PreferredModel) || len(policy.ResolutionSet) < 1 || len(policy.ResolutionSet) > 8 || !uniqueBoundedRoutingModels(policy.ResolutionSet) || policy.ResolutionSet[0] != policy.PreferredModel {
		return newFailure(KindRoutingPolicyInvalid, "routing_policy", "routing policy definition is invalid", false, "repair the generated routing policy manifest")
	}
	return nil
}

func cloneRoutingPolicyDefinition(policy RoutingPolicyDefinition) RoutingPolicyDefinition {
	policy.ResolutionSet = append([]string(nil), policy.ResolutionSet...)
	return policy
}

func uniqueBoundedRoutingModels(models []string) bool {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if !routingPolicyModelPattern.MatchString(model) || len(model) > 128 {
			return false
		}
		if _, exists := seen[model]; exists {
			return false
		}
		seen[model] = struct{}{}
	}
	return true
}
