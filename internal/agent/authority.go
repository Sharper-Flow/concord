package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// mainCheckoutAllowedCapabilities names the closed set of capabilities that
// resolve from a registered Project's default checkout. product_read is
// trivially read-only. work_define joins it because its operations write
// Product state in the store and never touch a checkout path (CD-0092 D2).
// The boundary is declared once here so future additions require a decision
// naming the new capability's write surface (CD-0092 D3).
var mainCheckoutAllowedCapabilities = map[agentcontract.Capability]struct{}{
	agentcontract.Capability("product_read"): {},
	agentcontract.Capability("work_define"):  {},
}

// mainCheckoutAllowedOperations names the operation-scoped extension to the
// capability allowlist. lifecycle writes one Product-state work.transitioned
// event and never touches a checkout path or claims a worktree (CD-0094 D1/D2).
// The boundary remains declared once so each new operation requires a decision
// naming its write surface (CD-0094 D3).
var mainCheckoutAllowedOperations = map[agentcontract.Capability]map[string]struct{}{
	agentcontract.Capability("work_transition"): {
		"lifecycle": {},
		// CD-0094 D5 admits remove conditionally: replay or the planner's
		// exact-item shelving proof, revalidated in the transaction.
		"remove": {},
	},
}

type Clock func() time.Time
type Service struct {
	Store          *store.Store
	Now            Clock
	NonceRetention time.Duration
	// ProjectHostProber probes the invoking directory/worktree git identity
	// with no transaction open. ProjectHostMatcher matches the probed host
	// against Project locators with SQL only, inside the caller's
	// transaction. The CLI boundary installs both. Keeping them injectable
	// gives tests a deterministic git runner while ensuring routine model
	// input cannot supply a Project or Product authority, and the split
	// keeps git work outside every mutation transaction (CD-0195 D2).
	ProjectHostProber  func(context.Context, string, string) (store.ResolvedProjectHost, error)
	ProjectHostMatcher func(context.Context, *store.Transaction, store.ResolvedProjectHost) (store.ProjectResolution, error)
	// publicationObserver, when set, is called with each publication phase as
	// that phase completes, and may return an error to interrupt the sequence.
	// It is an unexported white-box surface for conformance tests that must
	// observe real cross-authority ordering or inject a fault between two
	// phases; production construction leaves it nil. It mirrors the unexported
	// observer surface internal/store uses for the same purpose.
	publicationObserver func(phase string) error
	// ExternalEvidenceCommand runs one authenticated forge command outside
	// every write transaction. argv carries each value separately; no shell
	// string is accepted. Production construction installs the gh executor;
	// tests install a deterministic fake, so no test touches the forge. The
	// outside-repair boundary owns the only call site (CD-0122 D2, CD-0210).
	ExternalEvidenceCommand ExternalEvidenceCommandRunner
}

// ExternalEvidenceCommandRunner runs one external command by exact argv.
// The implementation owns the bounded execution policy; a nil return value
// from stdout is a command failure the caller reports as missing evidence.
type ExternalEvidenceCommandRunner func(ctx context.Context, command string, args ...string) (stdout string, err error)

func NewService(authority *store.Store) *Service {
	var now Clock
	if authority != nil {
		now = authority.Clock
	}
	return &Service{Store: authority, Now: now, NonceRetention: 24 * time.Hour, ExternalEvidenceCommand: DefaultExternalEvidenceCommand}
}

func authorityUnavailable(op string) error {
	return &store.Failure{Kind: store.KindUnavailable, Op: op, Detail: "authority store is not open", RecoveryAction: "open the authority database"}
}

func transactionInvalid(op string) error {
	return &store.Failure{Kind: store.KindInvalidOperation, Op: op, Detail: "transaction is not open", RecoveryAction: "supply an active store transaction"}
}

func (s *Service) authorityReady(op string) error {
	if s == nil || s.Store == nil {
		return authorityUnavailable(op)
	}
	return nil
}
func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

type ClientRegistration struct {
	ClientRef string
	KeyID     string
	PublicKey ed25519.PublicKey
	Policy    TrustedClientPolicy
}

type TrustedClientPolicy struct {
	PrincipalRef string
	Capabilities []agentcontract.Capability
	ProductScope []string
	ProjectScope []string
	AgentScope   []string
}

func (s *Service) RegisterTrustedClient(ctx context.Context, registration ClientRegistration) error {
	if err := s.authorityReady("agent_register_client"); err != nil {
		return err
	}
	if registration.ClientRef == "" || registration.KeyID == "" || len(registration.PublicKey) != ed25519.PublicKeySize || !validTrustedPolicy(registration.Policy) {
		return errors.New("invalid client registration")
	}
	now := s.now().Format(time.RFC3339Nano)
	policy := canonicalPolicy(registration.Policy)
	return s.Store.RegisterTrustedClient(ctx, store.TrustedClientRecord{ClientRef: registration.ClientRef, Status: "active", PrincipalRef: registration.Policy.PrincipalRef, CapabilitiesJSON: policy.capabilities, ProductScopeJSON: policy.products, ProjectScopeJSON: policy.projects, AgentScopeJSON: policy.agents}, store.TrustedClientKeyRecord{ClientRef: registration.ClientRef, KeyID: registration.KeyID, PublicKey: []byte(registration.PublicKey), Status: "active"}, now)
}

type canonicalPolicyJSON struct{ capabilities, products, projects, agents string }

func canonicalPolicy(policy TrustedClientPolicy) canonicalPolicyJSON {
	caps := normalizeStrings(capabilityStrings(policy.Capabilities))
	products := normalizeStrings(policy.ProductScope)
	projects := normalizeStrings(policy.ProjectScope)
	agents := normalizeStrings(policy.AgentScope)
	a, _ := json.Marshal(caps)
	b, _ := json.Marshal(products)
	c, _ := json.Marshal(projects)
	d, _ := json.Marshal(agents)
	return canonicalPolicyJSON{string(a), string(b), string(c), string(d)}
}
func validTrustedPolicy(policy TrustedClientPolicy) bool {
	if !agentcontract.BoundedBytes(policy.PrincipalRef, 1, 128) {
		return false
	}
	return validPolicyGrants(policy)
}

// validPolicyGrants holds the policy dimensions an expansion shares with a
// full statement: capability vocabulary, per-dimension bounds, uniqueness,
// and scope-string bounds. The principal is deliberately absent because an
// expansion cannot change it.
func validPolicyGrants(policy TrustedClientPolicy) bool {
	if len(policy.Capabilities) > 32 || len(policy.ProductScope) > 100 || len(policy.ProjectScope) > 100 || len(policy.AgentScope) > 100 || !agentcontract.UniqueNonEmpty(capabilityStrings(policy.Capabilities)) || !agentcontract.UniqueNonEmpty(policy.ProductScope) || !agentcontract.UniqueNonEmpty(policy.ProjectScope) || !agentcontract.UniqueNonEmpty(policy.AgentScope) {
		return false
	}
	for _, capability := range policy.Capabilities {
		if !slices.Contains([]string{"product_read", "work_define", "work_transition", "work_relate", "work_compact", "cross_scope", "research"}, string(capability)) {
			return false
		}
	}
	for _, value := range append(append(append([]string{}, policy.ProductScope...), policy.ProjectScope...), policy.AgentScope...) {
		if !agentcontract.BoundedBytes(value, 1, 128) {
			return false
		}
	}
	return true
}

func (s *Service) UpdateTrustedClientPolicy(ctx context.Context, clientRef string, policy TrustedClientPolicy) error {
	if err := s.authorityReady("agent_update_policy"); err != nil {
		return err
	}
	if clientRef == "" || !validTrustedPolicy(policy) {
		return errors.New("invalid trusted client policy")
	}
	p := canonicalPolicy(policy)
	return s.Store.UpdateTrustedClientPolicy(ctx, clientRef, store.TrustedClientRecord{PrincipalRef: policy.PrincipalRef, CapabilitiesJSON: p.capabilities, ProductScopeJSON: p.products, ProjectScopeJSON: p.projects, AgentScopeJSON: p.agents}, s.now().Format(time.RFC3339Nano))
}

// ExpandTrustedClientPolicy widens a trusted client policy additively
// (TS5). The additions union with the stored capabilities, Product
// scope, Project scope, and agent scope; every grant the client already holds survives
// unchanged, and the stored principal is never touched. Restating the full
// policy with a new principal stays on UpdateTrustedClientPolicy.
func (s *Service) ExpandTrustedClientPolicy(ctx context.Context, clientRef string, additions TrustedClientPolicy) error {
	if err := s.authorityReady("agent_expand_policy"); err != nil {
		return err
	}
	if clientRef == "" || !validPolicyGrants(additions) {
		return errors.New("invalid trusted client policy additions")
	}
	add := canonicalPolicy(additions)
	return s.Store.MutateTrustedClientPolicy(ctx, clientRef, unionPolicyExpansion("agent_expand_policy", add))
}

// unionPolicyExpansion returns the mutate closure that unions the canonical
// additions into every policy dimension under the dimension bounds.
func unionPolicyExpansion(op string, add canonicalPolicyJSON) func(store.TrustedClientRecord) (store.TrustedClientRecord, error) {
	return func(current store.TrustedClientRecord) (store.TrustedClientRecord, error) {
		next := current
		var err error
		if next.CapabilitiesJSON, err = unionPolicyJSON(op, "capabilities", current.CapabilitiesJSON, add.capabilities, 32); err != nil {
			return current, err
		}
		if next.ProductScopeJSON, err = unionPolicyJSON(op, "product scope", current.ProductScopeJSON, add.products, 100); err != nil {
			return current, err
		}
		if next.ProjectScopeJSON, err = unionPolicyJSON(op, "project scope", current.ProjectScopeJSON, add.projects, 100); err != nil {
			return current, err
		}
		// The agent scope is a policy dimension like the others (CD-0049 D5).
		// An expansion that skipped it would leave a client unable to present
		// an agent it was just granted.
		if next.AgentScopeJSON, err = unionPolicyJSON(op, "agent scope", current.AgentScopeJSON, add.agents, 100); err != nil {
			return current, err
		}
		return next, nil
	}
}

// unionPolicyJSON decodes both canonical JSON arrays, unions them without
// duplicates in sorted order, and refuses when the union exceeds the
// dimension's bound. A stored array that does not decode refuses typed rather
// than being widened by guesswork.
func unionPolicyJSON(op, dimension, currentJSON, additionsJSON string, bound int) (string, error) {
	var current, additions []string
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		return "", &store.Failure{Kind: store.KindInvalidOperation, Op: op, Detail: "stored " + dimension + " policy is unreadable", RecoveryAction: "restate the full policy with client-policy-update"}
	}
	if err := json.Unmarshal([]byte(additionsJSON), &additions); err != nil {
		return "", &store.Failure{Kind: store.KindInvalidOperation, Op: op, Detail: dimension + " additions are unreadable", RecoveryAction: "restate the additions as a JSON array of strings"}
	}
	merged := normalizeStrings(append(append([]string(nil), current...), additions...))
	deduped := merged[:0]
	for i, value := range merged {
		if i == 0 || value != merged[i-1] {
			deduped = append(deduped, value)
		}
	}
	if len(deduped) > bound {
		return "", &store.Failure{Kind: store.KindInvalidOperation, Op: op, Detail: dimension + " union exceeds the policy bound of " + fmt.Sprint(bound), RecoveryAction: "split the expansion or restate the full policy with client-policy-update"}
	}
	out, err := json.Marshal(deduped)
	if err != nil {
		return "", &store.Failure{Kind: store.KindUnavailable, Op: op, Detail: "cannot encode the merged " + dimension + " policy", RecoveryAction: "retry the policy expansion"}
	}
	return string(out), nil
}
func (s *Service) RotateClientKey(ctx context.Context, registration ClientRegistration) error {
	if err := s.authorityReady("agent_rotate_key"); err != nil {
		return err
	}
	if registration.ClientRef == "" || registration.KeyID == "" || len(registration.PublicKey) != ed25519.PublicKeySize {
		return errors.New("invalid client rotation")
	}
	now := s.now().Format(time.RFC3339Nano)
	return s.Store.RotateTrustedClientKey(ctx, registration.ClientRef, store.TrustedClientKeyRecord{ClientRef: registration.ClientRef, KeyID: registration.KeyID, PublicKey: []byte(registration.PublicKey), Status: "active"}, now)
}
func (s *Service) RevokeClient(ctx context.Context, clientRef string) error {
	if err := s.authorityReady("agent_revoke_client"); err != nil {
		return err
	}
	if clientRef == "" {
		return errors.New("empty client reference")
	}
	return s.Store.RevokeTrustedClient(ctx, clientRef, s.now().Format(time.RFC3339Nano))
}

type Authority struct {
	PrincipalRef      string
	ClientRef         string
	SessionRef        string
	AgentRef          string
	Directory         string
	Worktree          string
	ManifestDigest    string
	Capabilities      []agentcontract.Capability
	ProductScope      []string
	ProjectScope      []string
	ScopeVersion      string
	CandidateProducts []string
	ScopeSnapshot     map[string]any
	// MainWorktree records that the grant resolved from the registered
	// Project's default checkout rather than a linked worktree, so
	// conditional main-checkout operations can enforce their conditions at
	// planning time.
	MainWorktree bool
}

type Invocation struct {
	ClientRef, PrincipalRef, SessionRef, AgentRef, Directory, Worktree, ManifestDigest string
	HostAssertionDigest                                                                string
	RequiredCapability                                                                 agentcontract.Capability
	RequiredOperation                                                                  string
	ProductID, ProjectID                                                               string
}

// envelopeInvocation asks to authorize one operation for the whole call
// envelope: the caller identity and its host binding, scoped to the selected
// Product.
func envelopeInvocation(env CallEnvelope, capability agentcontract.Capability, operation string) Invocation {
	return Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: capability, RequiredOperation: operation, ProductID: env.SelectedProductID}
}

func (s *Service) Authorize(ctx context.Context, in Invocation) (Authority, error) {
	if err := s.authorityReady("agent_authorize"); err != nil {
		return Authority{}, err
	}
	if s.ProjectHostProber == nil || s.ProjectHostMatcher == nil {
		return Authority{}, authorityRefusal("project host prober and matcher are required")
	}
	client, key, err := s.Store.TrustedClientWithKey(ctx, in.ClientRef)
	if err != nil {
		return Authority{}, trustedClientRefusalFor(err, authorityRefusal("client or key is not active"))
	}
	if client.Status != "active" || key.Status != "active" {
		return Authority{}, authorityRefusal("client or key is not active")
	}
	host, err := s.ProbeProjectHost(ctx, in.Directory, in.Worktree)
	if err != nil {
		return Authority{}, err
	}
	return s.authorizeResolved(ctx, nil, host, in, client)
}

// ProbeProjectHost resolves the invoking git identity with no transaction
// open (CD-0195 D2). Callers run it before opening the mutation transaction
// whose authorization matches the probed host against Project locators.
func (s *Service) ProbeProjectHost(ctx context.Context, directory, worktree string) (store.ResolvedProjectHost, error) {
	if err := s.authorityReady("agent_authorize"); err != nil {
		return store.ResolvedProjectHost{}, err
	}
	if s.ProjectHostProber == nil {
		return store.ResolvedProjectHost{}, authorityRefusal("project host prober is required")
	}
	return s.ProjectHostProber(ctx, directory, worktree)
}

func (s *Service) AuthorizeTx(ctx context.Context, tx *store.Transaction, host store.ResolvedProjectHost, in Invocation) (Authority, error) {
	if err := s.authorityReady("agent_authorize"); err != nil {
		return Authority{}, err
	}
	if tx == nil {
		return Authority{}, transactionInvalid("agent_authorize")
	}
	if host.Canonical == "" && host.Host.CanonicalPath == "" {
		return Authority{}, authorityRefusal("project host was not probed; probe it before the transaction opens")
	}
	if s.ProjectHostMatcher == nil {
		return Authority{}, authorityRefusal("project host matcher is required")
	}
	client, key, err := store.TrustedClientWithKeyTx(ctx, tx, in.ClientRef)
	if err != nil {
		return Authority{}, trustedClientRefusalFor(err, authorityRefusal("client or key is not active"))
	}
	if client.Status != "active" || key.Status != "active" {
		return Authority{}, authorityRefusal("client or key is not active")
	}
	grant, err := s.authorizeResolved(ctx, tx, host, in, client)
	if err != nil {
		return Authority{}, err
	}
	return grant, nil
}

// trustedClientRefusalFor maps an absent or inactive client to the caller's
// refusal. Other errors retain their own classification.
func trustedClientRefusalFor(err, refusal error) error {
	var failure *store.Failure
	if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
		return refusal
	}
	return err
}

func (s *Service) authorizeResolved(ctx context.Context, tx *store.Transaction, host store.ResolvedProjectHost, in Invocation, client store.TrustedClientRecord) (Authority, error) {
	if in.PrincipalRef != "" && in.PrincipalRef != client.PrincipalRef {
		return Authority{}, authorityRefusal("principal does not match trusted client")
	}
	if in.ManifestDigest != agentcontract.ManifestDigest {
		return Authority{}, authorityRefusal("manifest digest mismatch")
	}
	var policyCaps, policyProducts, policyProjects, policyAgents []string
	if json.Unmarshal([]byte(client.CapabilitiesJSON), &policyCaps) != nil || json.Unmarshal([]byte(client.ProductScopeJSON), &policyProducts) != nil || json.Unmarshal([]byte(client.ProjectScopeJSON), &policyProjects) != nil || json.Unmarshal([]byte(client.AgentScopeJSON), &policyAgents) != nil {
		return Authority{}, authorityRefusal("invalid client authority policy")
	}
	// The agent reference arrives from the caller and is written into
	// attribution, so the trusted client must name the agents it may present.
	// An empty scope authorizes none, which is the state every client
	// registered before the scope existed holds. TS5 additive expansion
	// is the remedy, so the refusal names it: re-registration would discard
	// grants the client already holds.
	//
	// The two conditions refuse separately because they are different
	// situations. A client with no scope was never told which agents it
	// presents. A client whose scope omits this agent was told about others.
	// Naming the client in both keeps the operator pointed at the policy, not
	// at the agent that merely surfaced it.
	if len(policyAgents) == 0 {
		return Authority{}, authorityRefusal(fmt.Sprintf("trusted client %q holds an empty agent scope, which authorizes no agent; name the agents it presents with concord client-policy-expand", in.ClientRef))
	}
	if !slices.Contains(policyAgents, in.AgentRef) {
		return Authority{}, authorityRefusal(fmt.Sprintf("agent %q is outside the agent scope of trusted client %q; add it with concord client-policy-expand", in.AgentRef, in.ClientRef))
	}
	resolved, err := s.ProjectHostMatcher(ctx, tx, host)
	if err != nil {
		return Authority{}, err
	}
	var scopeVersion string
	var resolvedProducts []string
	if tx == nil {
		scopeVersion, resolvedProducts, err = s.Store.ScopeVersion(ctx, resolved.ProjectID)
	} else {
		scopeVersion, resolvedProducts, err = store.ScopeVersionTx(ctx, tx, resolved.ProjectID)
	}
	if err != nil {
		return Authority{}, err
	}
	candidateProducts := intersect(resolvedProducts, policyProducts)
	if len(candidateProducts) == 0 {
		return Authority{}, authorityRefusal("resolved Project is outside trusted client Product policy")
	}
	if !slices.Contains(policyProjects, resolved.ProjectID) {
		return Authority{}, authorityRefusal("resolved Project is outside trusted client policy")
	}
	if resolved.MainWorktree {
		_, capabilityAllowed := mainCheckoutAllowedCapabilities[in.RequiredCapability]
		_, operationAllowed := mainCheckoutAllowedOperations[in.RequiredCapability][in.RequiredOperation]
		if !capabilityAllowed && !operationAllowed {
			return Authority{}, authorityRefusal("implementation-bearing authority requires a linked worktree; the main checkout refuses it (CD-0092 D2)")
		}
	}
	if !containsCapability(capabilityValues(policyCaps), in.RequiredCapability) {
		return Authority{}, authorityRefusal("capability outside trusted client policy")
	}
	if in.ProductID != "" && !slices.Contains(candidateProducts, in.ProductID) {
		return Authority{}, authorityRefusal("product outside trusted client policy")
	}
	if in.ProjectID != "" && in.ProjectID != resolved.ProjectID {
		return Authority{}, authorityRefusal("project outside resolved scope")
	}
	// Project authority follows current Product membership (CD-0152 D1). The
	// explicit policy is the authority source for Products, so every Product
	// it authorizes contributes its current Projects: the authority spans
	// every Project of every authorized Product, and the ambient locator
	// neither limits it nor selects a narrower set. A Project a Product no
	// longer owns drops out on the next invocation.
	authorizedProducts := normalizeStrings(policyProducts)
	projects, err := s.authorizedProductProjects(ctx, tx, resolved.ProjectID, authorizedProducts)
	if err != nil {
		return Authority{}, err
	}
	snapshot := map[string]any{"project_id": resolved.ProjectID, "product_ids": candidateProducts, "scope_version": scopeVersion}
	return Authority{PrincipalRef: client.PrincipalRef, ClientRef: client.ClientRef, SessionRef: in.SessionRef, AgentRef: in.AgentRef, Directory: in.Directory, Worktree: in.Worktree, ManifestDigest: agentcontract.ManifestDigest, Capabilities: capabilityValues(normalizeStrings(policyCaps)), ProductScope: authorizedProducts, ProjectScope: normalizeStrings(projects), ScopeVersion: scopeVersion, CandidateProducts: candidateProducts, ScopeSnapshot: snapshot, MainWorktree: resolved.MainWorktree}, nil
}

func (s *Service) authorizedProductProjects(ctx context.Context, tx *store.Transaction, projectID string, products []string) ([]string, error) {
	projects := []string{projectID}
	seenProjects := map[string]bool{projectID: true}
	for _, product := range products {
		var memberships []store.ProjectMembership
		var err error
		if tx == nil {
			memberships, err = s.Store.ProjectsForProduct(ctx, product)
		} else {
			memberships, err = store.ProjectsForProductTx(ctx, tx, product)
		}
		if err != nil {
			return nil, err
		}
		for _, membership := range memberships {
			if !seenProjects[membership.ID] {
				seenProjects[membership.ID] = true
				projects = append(projects, membership.ID)
			}
		}
	}
	return projects, nil
}

// authorityRefusal marks the authorization boundary as a typed refusal.
func authorityRefusal(detail string) error {
	return newRuntimeFailure("unauthorized", detail, "contact_operator", false)
}

func intersect(left, right []string) []string {
	var out []string
	for _, value := range normalizeStrings(left) {
		if slices.Contains(right, value) {
			out = append(out, value)
		}
	}
	return out
}

func capabilityStrings(values []agentcontract.Capability) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
func capabilityValues(values []string) []agentcontract.Capability {
	out := make([]agentcontract.Capability, len(values))
	for i, value := range values {
		out[i] = agentcontract.Capability(value)
	}
	return out
}

// normalizeStrings sorts a scope list and always returns a non-nil slice, so a
// scope with no members serializes as the empty JSON array the policy columns
// require rather than as null.
func normalizeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	out = append(out, values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
func containsCapability(values []agentcontract.Capability, value agentcontract.Capability) bool {
	for _, v := range values {
		if subtle.ConstantTimeCompare([]byte(v), []byte(value)) == 1 {
			return true
		}
	}
	return false
}

// normalizeScopeList copies unique scope identities in first-occurrence order.
// Preserve order and nilness for callers that retain the original scope JSON.
func normalizeScopeList(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// scopeListValues decodes one scope-map list value into its normalized
// string values: a []string a plan builder set, or a []any decoded from
// stored challenge, approval, or idempotency-snapshot JSON. Values that are
// not string lists name no scope identities and contribute nothing.
func scopeListValues(value any) []string {
	switch list := value.(type) {
	case []string:
		return normalizeScopeList(list)
	case []any:
		if list == nil {
			return nil
		}
		values := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return normalizeScopeList(values)
	default:
		return nil
	}
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// authorizedScopeFromSnapshot decodes a scope snapshot for a mutation check.
// An absent snapshot carries no scope constraint. A malformed snapshot returns
// an error so a parse failure cannot widen the authorized scope.
func authorizedScopeFromSnapshot(scopeJSON string) (map[string]any, error) {
	if scopeJSON == "" {
		return nil, nil
	}
	var scope map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		return nil, err
	}
	return scope, nil
}

// scopeWithinAuthority checks a challenge scope against the authorization the
// invoking session holds. product_id, product_ids, and project_ids are
// compared against the derived scopes.
func scopeWithinAuthority(scope map[string]any, authority Authority) bool {
	productScope, projectScope := authority.ProductScope, authority.ProjectScope
	if product, ok := scope["product_id"].(string); ok && !slices.Contains(productScope, product) {
		return false
	}
	return allScopeStrings(scope["product_ids"], func(product string) bool { return slices.Contains(productScope, product) }) &&
		allScopeStrings(scope["project_ids"], func(project string) bool { return slices.Contains(projectScope, project) })
}

// allScopeStrings reports whether every element of a challenge scope list,
// decoded as []any or built as []string, is a string that satisfies keep. A
// value of any other type holds no list elements and passes.
func allScopeStrings(value any, keep func(string) bool) bool {
	switch ids := value.(type) {
	case []any:
		for _, raw := range ids {
			text, ok := raw.(string)
			if !ok || !keep(text) {
				return false
			}
		}
	case []string:
		for _, text := range ids {
			if !keep(text) {
				return false
			}
		}
	}
	return true
}
