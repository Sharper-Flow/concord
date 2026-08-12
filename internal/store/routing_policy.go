package store

import "regexp"

// RoutingPolicyDefinition is the generated, immutable resolution policy for a
// capability class. ResolutionSet is ordered: the preferred model is first.
type RoutingPolicyDefinition struct {
	CapabilityClass string
	PreferredModel  string
	ResolutionSet   []string
}

type RoutingPolicyRegistry struct {
	entries map[string]RoutingPolicyDefinition
}

var routingPolicyVersionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var routingPolicyModelPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*/[^/ ]+$`)

func routingPolicyKey(capabilityClass, version string) string { return capabilityClass + ":" + version }

func NewBuiltinRoutingPolicyRegistry() RoutingPolicyRegistry {
	entries := make(map[string]RoutingPolicyDefinition, len(generatedRoutingPolicies))
	for _, policy := range generatedRoutingPolicies {
		if err := ValidateRoutingPolicyDefinition(policy); err != nil {
			panic(err)
		}
		entries[routingPolicyKey(policy.CapabilityClass, RoutingPolicyVersion)] = cloneRoutingPolicyDefinition(policy)
	}
	return RoutingPolicyRegistry{entries: entries}
}

var builtinRoutingPolicyRegistry = NewBuiltinRoutingPolicyRegistry()

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
	if digest != RoutingPolicyManifestDigest || !laneDigestPattern.MatchString(digest) {
		return RoutingPolicyDefinition{}, newFailure(KindRoutingPolicyDigestMismatch, "routing_policy", "routing policy digest does not match the registered definition", false, "reread the routing policy registry and retry with its digest")
	}
	return cloneRoutingPolicyDefinition(policy), nil
}

func LookupRoutingPolicy(capabilityClass, version, digest string) (RoutingPolicyDefinition, error) {
	return builtinRoutingPolicyRegistry.Lookup(capabilityClass, version, digest)
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
