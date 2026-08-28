package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

const defaultClockSkew = 2 * time.Minute

type Clock func() time.Time
type Service struct {
	Store          *store.Store
	Now            Clock
	MaxClockSkew   time.Duration
	NonceRetention time.Duration
	// ProjectResolver is installed by the CLI boundary. Keeping it injectable
	// gives tests a deterministic git runner while ensuring routine model input
	// cannot supply a Project or Product authority.
	ProjectResolver func(context.Context, string, string) (store.ProjectResolution, error)
	// publicationObserver, when set, is called with each publication phase as
	// that phase completes, and may return an error to interrupt the sequence.
	// It is an unexported white-box surface for conformance tests that must
	// observe real cross-authority ordering or inject a fault between two
	// phases; production construction leaves it nil. It mirrors the unexported
	// observer surface internal/store uses for the same purpose.
	publicationObserver func(phase string) error
}

func NewService(authority *store.Store) *Service {
	var now Clock
	if authority != nil {
		now = authority.Clock
	}
	return &Service{Store: authority, Now: now, MaxClockSkew: defaultClockSkew, NonceRetention: 24 * time.Hour}
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
func (s *Service) skew() time.Duration {
	if s.MaxClockSkew <= 0 {
		return defaultClockSkew
	}
	return s.MaxClockSkew
}

type ClientRegistration struct {
	ClientRef string
	KeyID     string
	PublicKey ed25519.PublicKey
	Policy    TrustedClientPolicy
}

type TrustedClientPolicy struct {
	PrincipalRef string
	Capabilities []Capability
	ProductScope []string
	ProjectScope []string
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
	return s.Store.RegisterTrustedClient(ctx, store.TrustedClientRecord{ClientRef: registration.ClientRef, Status: "active", PrincipalRef: registration.Policy.PrincipalRef, CapabilitiesJSON: policy.capabilities, ProductScopeJSON: policy.products, ProjectScopeJSON: policy.projects}, store.TrustedClientKeyRecord{ClientRef: registration.ClientRef, KeyID: registration.KeyID, PublicKey: []byte(registration.PublicKey), Status: "active"}, now)
}

type canonicalPolicyJSON struct{ capabilities, products, projects string }

func canonicalPolicy(policy TrustedClientPolicy) canonicalPolicyJSON {
	caps := normalizeStrings(capabilityStrings(policy.Capabilities))
	products := normalizeStrings(policy.ProductScope)
	projects := normalizeStrings(policy.ProjectScope)
	a, _ := json.Marshal(caps)
	b, _ := json.Marshal(products)
	c, _ := json.Marshal(projects)
	return canonicalPolicyJSON{string(a), string(b), string(c)}
}
func validTrustedPolicy(policy TrustedClientPolicy) bool {
	if !bounded(policy.PrincipalRef, 1, 128) || len(policy.Capabilities) > 32 || len(policy.ProductScope) > 100 || len(policy.ProjectScope) > 100 || !unique(capabilityStrings(policy.Capabilities)) || !unique(policy.ProductScope) || !unique(policy.ProjectScope) {
		return false
	}
	for _, capability := range policy.Capabilities {
		// worker_evidence and worker_dispatch are registrable in a client
		// policy but deliberately absent from the grant-request vocabulary
		// below: both authorize client-only signed writes, and no bearer
		// grant can carry either. worker_dispatch is policy-bound because
		// CD-0059 D3 makes the nested-worker prohibition structural by
		// denying workers the capability at the policy layer.
		if !oneOf(string(capability), "product_read", "work_define", "work_transition", "work_relate", "work_compact", "work_initiative", "cross_scope", "research", string(CapabilityWorkerEvidence), string(CapabilityWorkerDispatch)) {
			return false
		}
	}
	for _, value := range append(append([]string{}, policy.ProductScope...), policy.ProjectScope...) {
		if !bounded(value, 1, 128) {
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
	return s.Store.UpdateTrustedClientPolicy(ctx, clientRef, store.TrustedClientRecord{PrincipalRef: policy.PrincipalRef, CapabilitiesJSON: p.capabilities, ProductScopeJSON: p.products, ProjectScopeJSON: p.projects}, s.now().Format(time.RFC3339Nano))
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
	Capabilities      []Capability
	ProductScope      []string
	ProjectScope      []string
	ScopeVersion      string
	CandidateProducts []string
	ScopeSnapshot     map[string]any
}

type Grant = Authority

type Invocation struct {
	ClientRef, PrincipalRef, SessionRef, AgentRef, Directory, Worktree, ManifestDigest string
	HostAssertionDigest                                                                string
	RequiredCapability                                                                 Capability
	ProductID, ProjectID                                                               string
}

func (s *Service) Authorize(ctx context.Context, in Invocation) (Authority, error) {
	if err := s.authorityReady("agent_authorize"); err != nil {
		return Authority{}, err
	}
	if s.ProjectResolver == nil {
		return Authority{}, authorityRefusal("project resolver is required")
	}
	client, key, err := s.Store.TrustedClientWithKey(ctx, in.ClientRef)
	if err != nil || client.Status != "active" || key.Status != "active" {
		return Authority{}, authorityRefusal("client or key is not active")
	}
	return s.authorizeResolved(ctx, nil, in, client)
}

func (s *Service) AuthorizeTx(ctx context.Context, tx *store.Transaction, in Invocation) (Authority, error) {
	if err := s.authorityReady("agent_authorize"); err != nil {
		return Authority{}, err
	}
	if tx == nil {
		return Authority{}, transactionInvalid("agent_authorize")
	}
	if s.ProjectResolver == nil {
		return Authority{}, authorityRefusal("project resolver is required")
	}
	client, key, err := store.TrustedClientWithKeyTx(ctx, tx, in.ClientRef)
	if err != nil || client.Status != "active" || key.Status != "active" {
		return Authority{}, authorityRefusal("client or key is not active")
	}
	return s.authorizeResolved(ctx, tx, in, client)
}

func (s *Service) authorizeResolved(ctx context.Context, tx *store.Transaction, in Invocation, client store.TrustedClientRecord) (Authority, error) {
	if in.PrincipalRef != "" && in.PrincipalRef != client.PrincipalRef {
		return Authority{}, authorityRefusal("principal does not match trusted client")
	}
	if in.ManifestDigest != ManifestDigest {
		return Authority{}, authorityRefusal("manifest digest mismatch")
	}
	var policyCaps, policyProducts, policyProjects []string
	if json.Unmarshal([]byte(client.CapabilitiesJSON), &policyCaps) != nil || json.Unmarshal([]byte(client.ProductScopeJSON), &policyProducts) != nil || json.Unmarshal([]byte(client.ProjectScopeJSON), &policyProjects) != nil {
		return Authority{}, authorityRefusal("invalid client authority policy")
	}
	resolved, err := s.ProjectResolver(ctx, in.Directory, in.Worktree)
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
	if !contains(policyProjects, resolved.ProjectID) {
		return Authority{}, authorityRefusal("resolved Project is outside trusted client policy")
	}
	if resolved.MainWorktree && in.RequiredCapability != "product_read" {
		return Authority{}, authorityRefusal("mutating authority requires a linked worktree; the main checkout is read-only")
	}
	if !containsCapability(capabilityValues(policyCaps), in.RequiredCapability) {
		return Authority{}, authorityRefusal("capability outside trusted client policy")
	}
	if in.ProductID != "" && !contains(candidateProducts, in.ProductID) {
		return Authority{}, authorityRefusal("product outside trusted client policy")
	}
	if in.ProjectID != "" && in.ProjectID != resolved.ProjectID {
		return Authority{}, authorityRefusal("project outside resolved scope")
	}
	projects := []string{resolved.ProjectID}
	snapshot := map[string]any{"project_id": resolved.ProjectID, "product_ids": candidateProducts, "scope_version": scopeVersion}
	return Authority{PrincipalRef: client.PrincipalRef, ClientRef: client.ClientRef, SessionRef: in.SessionRef, AgentRef: in.AgentRef, Directory: in.Directory, Worktree: in.Worktree, ManifestDigest: ManifestDigest, Capabilities: capabilityValues(normalizeStrings(policyCaps)), ProductScope: candidateProducts, ProjectScope: projects, ScopeVersion: scopeVersion, CandidateProducts: candidateProducts, ScopeSnapshot: snapshot}, nil
}

// authorityRefusal marks the authorization boundary as a typed refusal.
func authorityRefusal(detail string) error {
	return newRuntimeFailure("unauthorized", detail, "contact_operator", false)
}

func intersect(left, right []string) []string {
	var out []string
	for _, value := range normalizeStrings(left) {
		if contains(right, value) {
			out = append(out, value)
		}
	}
	return out
}

func expiryPassed(stored string, now time.Time) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, stored)
	if err != nil {
		return time.Time{}, true
	}
	return parsed, !parsed.After(now)
}

func sha256Bytes(value []byte) []byte { sum := sha256.Sum256(value); return sum[:] }
func capabilityStrings(values []Capability) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
func capabilityValues(values []string) []Capability {
	out := make([]Capability, len(values))
	for i, value := range values {
		out[i] = Capability(value)
	}
	return out
}
func subset(requested, allowed []string) bool {
	for _, value := range requested {
		if !contains(allowed, value) {
			return false
		}
	}
	return true
}
func normalizeStrings(values []string) []string {
	out := append([]string(nil), values...)
	for i := range out {
		if out[i] == "" {
			continue
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func containsCapability(values []Capability, value Capability) bool {
	for _, v := range values {
		if subtle.ConstantTimeCompare([]byte(v), []byte(value)) == 1 {
			return true
		}
	}
	return false
}

type ApprovalChallengeSpec struct {
	OperationDigest     string
	Scope               map[string]any
	Versions            map[string]any
	Consequence         string
	HostAssertionDigest string
	ExpiresAt           time.Time
}
type ApprovalCheck struct {
	ApprovalRef             string
	OperationDigest         string
	Scope                   map[string]any
	Versions                map[string]any
	Consequence             string
	ClientRef               string
	SessionRef              string
	RequireOperatorIdentity bool
}

type HostApprovalAssertion struct {
	ChallengeRef  string   `json:"challenge_ref"`
	RequestDigest string   `json:"request_digest"`
	Scope         []string `json:"scope"`
	Versions      []string `json:"versions"`
	SessionRef    string   `json:"session_ref"`
	AgentRef      string   `json:"agent_ref"`
	Worktree      string   `json:"worktree"`
	IssuedAt      string   `json:"issued_at"`
}

func approvalScopeBindings(scope map[string]any) []string {
	keys := make([]string, 0, len(scope))
	for key := range scope {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bindings := make([]string, 0, len(scope))
	for _, key := range keys {
		switch value := scope[key].(type) {
		case string:
			if value != "" {
				bindings = append(bindings, key+":"+value)
			}
		case []string:
			for _, item := range value {
				if item != "" {
					bindings = append(bindings, key+":"+item)
				}
			}
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok && text != "" {
					bindings = append(bindings, key+":"+text)
				}
			}
		}
	}
	sort.Strings(bindings)
	return bindings
}

func approvalVersionBindings(versions map[string]any) []string {
	keys := make([]string, 0, len(versions))
	for key := range versions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bindings := make([]string, 0, len(versions))
	for _, key := range keys {
		bindings = append(bindings, key+":"+fmt.Sprint(versions[key]))
	}
	sort.Strings(bindings)
	return bindings
}

// ValidateHostApprovalAssertionTx proves that the trusted client signed the
// exact approved intent. It also consumes the nonce in the same transaction as
// the resulting approval/domain effect.
func (s *Service) ValidateHostApprovalAssertionTx(ctx context.Context, tx *store.Transaction, in Invocation, assertion HostApprovalAssertion, check ApprovalCheck) (bool, error) {
	if err := s.authorityReady("agent_validate_host_approval"); err != nil {
		return false, err
	}
	isChallenge, err := s.validateHostApprovalAssertionIdentityTx(ctx, tx, in, assertion, check)
	if err != nil {
		return false, err
	}
	return isChallenge, nil
}

// validateHostApprovalAssertionIdentityTx is the shared signed-approval path.
// The assertion authenticates the invoking trusted client and exact challenge;
// it carries no human identity. Operator attribution is derived later from
// the durable consumed approval record.
func (s *Service) validateHostApprovalAssertionIdentityTx(ctx context.Context, tx *store.Transaction, in Invocation, assertion HostApprovalAssertion, check ApprovalCheck) (bool, error) {
	if err := s.authorityReady("agent_validate_host_approval"); err != nil {
		return false, err
	}
	if tx == nil || assertion.ChallengeRef != check.ApprovalRef || assertion.RequestDigest != check.OperationDigest || assertion.SessionRef != in.SessionRef || assertion.AgentRef != in.AgentRef || assertion.Worktree != in.Worktree {
		return false, transactionInvalid("agent_validate_host_approval")
	}
	issued, err := time.Parse(time.RFC3339Nano, assertion.IssuedAt)
	if err != nil || issued.Before(s.now().Add(-s.skew())) || issued.After(s.now().Add(s.skew())) {
		return false, errors.New("host approval assertion timestamp invalid")
	}
	wantScope, _ := json.Marshal(approvalScopeBindings(check.Scope))
	wantVersions, _ := json.Marshal(approvalVersionBindings(check.Versions))
	assertedScope, _ := json.Marshal(assertion.Scope)
	assertedVersions, _ := json.Marshal(assertion.Versions)
	if string(assertedScope) != string(wantScope) || string(assertedVersions) != string(wantVersions) {
		return false, errors.New("host approval assertion scope or versions invalid")
	}
	isChallenge := true
	challenge, challengeErr := store.ReadApprovalChallengeRefTx(ctx, tx, assertion.ChallengeRef)
	if challengeErr == nil {
		storedScope, _ := json.Marshal(check.Scope)
		storedVersions, _ := json.Marshal(check.Versions)
		_, challengeExpired := expiryPassed(challenge.ExpiresAt, s.now())
		if challenge.Status != "active" || challengeExpired || challenge.OperationDigest != check.OperationDigest || challenge.ScopeJSON != string(storedScope) || challenge.VersionJSON != string(storedVersions) || challenge.Consequence != check.Consequence || challenge.HostAssertionDigest != in.HostAssertionDigest {
			return false, errors.New("approval challenge binding invalid")
		}
	} else if challengeErr != nil {
		var failure *store.Failure
		if !errors.As(challengeErr, &failure) || failure.Kind != store.KindProjectionNotFound {
			return false, challengeErr
		}
		isChallenge = false
	}
	return isChallenge, nil
}

// CreateApprovalChallengeTx is the only challenge creation path. Identity and
// scope come from the authorization boundary; callers provide operation intent.
func (s *Service) CreateApprovalChallengeTx(ctx context.Context, tx *store.Transaction, in Invocation, spec ApprovalChallengeSpec) (string, error) {
	if err := s.authorityReady("agent_challenge_create"); err != nil {
		return "", err
	}
	if tx == nil || spec.OperationDigest == "" || spec.Scope == nil || spec.Versions == nil || spec.Consequence == "" || spec.HostAssertionDigest == "" || in.HostAssertionDigest != spec.HostAssertionDigest || spec.ExpiresAt.IsZero() {
		return "", transactionInvalid("agent_challenge_create")
	}
	authority, err := s.AuthorizeTx(ctx, tx, in)
	if err != nil {
		return "", err
	}
	if !validChallengeScope(spec.Scope) || !validChallengeVersions(spec.Versions) || !scopeWithinAuthority(spec.Scope, authority) {
		return "", errors.New("approval scope exceeds authorized scope")
	}
	now := s.now()
	if !spec.ExpiresAt.After(now) || spec.ExpiresAt.Sub(now) > 24*time.Hour {
		return "", errors.New("challenge expiry outside bound")
	}
	scope, _ := json.Marshal(spec.Scope)
	versions, _ := json.Marshal(spec.Versions)
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(raw)
	productScope, _ := json.Marshal(authority.ProductScope)
	err = store.InsertApprovalChallengeTx(ctx, tx, store.ApprovalChallengeRecord{ChallengeRef: ref, ClientRef: authority.ClientRef, PrincipalRef: authority.PrincipalRef, SessionRef: authority.SessionRef, AgentRef: authority.AgentRef, Directory: authority.Directory, Worktree: authority.Worktree, ProductScopeJSON: string(productScope), OperationDigest: spec.OperationDigest, ScopeJSON: string(scope), VersionJSON: string(versions), Consequence: spec.Consequence, HostAssertionDigest: spec.HostAssertionDigest, IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: spec.ExpiresAt.UTC().Format(time.RFC3339Nano), Status: "active", MaxUses: 1})
	if err != nil {
		return "", err
	}
	return ref, nil
}

// CreateApprovalFromChallengeTx derives identity fields from authorization and
// atomically consumes the core-issued challenge before recording approval.
func (s *Service) CreateApprovalFromChallengeTx(ctx context.Context, tx *store.Transaction, in Invocation, challengeRef string) (string, error) {
	if err := s.authorityReady("agent_approval_create"); err != nil {
		return "", err
	}
	if tx == nil || len(challengeRef) != 64 {
		return "", transactionInvalid("agent_approval_create")
	}
	authority, err := s.AuthorizeTx(ctx, tx, in)
	if err != nil {
		return "", err
	}
	identity := store.ChallengeIdentity{ClientRef: in.ClientRef, SessionRef: in.SessionRef, AgentRef: in.AgentRef, Worktree: in.Worktree}
	challenge, err := store.ReadApprovalChallengeTx(ctx, tx, challengeRef, identity)
	if err != nil {
		return "", errors.New("approval challenge not found")
	}
	_, challengeExpired := expiryPassed(challenge.ExpiresAt, s.now())
	if challenge.Status != "active" || challenge.UsedCount >= challenge.MaxUses || challenge.HostAssertionDigest != in.HostAssertionDigest || challengeExpired {
		return "", errors.New("approval challenge invalid")
	}
	if err := store.ConsumeApprovalChallengeTx(ctx, tx, challengeRef, identity, s.now().Format(time.RFC3339Nano)); err != nil {
		return "", errors.New("approval challenge consumption lost race")
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(raw)
	evidenceRef := "approval-challenge:" + challengeRef
	evidenceDigest := sha256Hex([]byte(challenge.HostAssertionDigest + "|" + challenge.OperationDigest + "|" + challenge.ScopeJSON + "|" + challenge.VersionJSON))
	err = store.InsertApprovalTx(ctx, tx, store.ApprovalInsert{ApprovalRef: ref, OperationDigest: challenge.OperationDigest, ScopeJSON: challenge.ScopeJSON, VersionJSON: challenge.VersionJSON, Consequence: challenge.Consequence, HumanPrincipalRef: authority.PrincipalRef, ClientRef: authority.ClientRef, SessionRef: authority.SessionRef, IssuedAt: s.now().Format(time.RFC3339Nano), ExpiresAt: challenge.ExpiresAt, MaxUses: 1, ProtectedEvidenceRef: evidenceRef, ProtectedEvidenceDigest: evidenceDigest})
	if err != nil {
		return "", err
	}
	return ref, nil
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

func scopeWithinAuthority(scope map[string]any, authority Authority) bool {
	if product, ok := scope["product_id"].(string); ok && !contains(authority.ProductScope, product) {
		return false
	}
	switch products := scope["product_ids"].(type) {
	case []any:
		for _, raw := range products {
			product, ok := raw.(string)
			if !ok || !contains(authority.ProductScope, product) {
				return false
			}
		}
	case []string:
		for _, product := range products {
			if !contains(authority.ProductScope, product) {
				return false
			}
		}
	}
	switch projects := scope["project_ids"].(type) {
	case []any:
		for _, raw := range projects {
			project, ok := raw.(string)
			if !ok || !contains(authority.ProjectScope, project) {
				return false
			}
		}
	case []string:
		for _, project := range projects {
			if !contains(authority.ProjectScope, project) {
				return false
			}
		}
	}
	return true
}
func validChallengeScope(scope map[string]any) bool {
	allowed := map[string]bool{"product_id": true, "product_ids": true, "project_ids": true, "work_ids": true, "scope_version": true}
	for key, value := range scope {
		if !allowed[key] {
			return false
		}
		switch key {
		case "product_id", "scope_version":
			if text, ok := value.(string); !ok || !bounded(text, 1, 128) {
				return false
			}
		case "product_ids", "project_ids", "work_ids":
			switch ids := value.(type) {
			case []any:
				if len(ids) > 100 {
					return false
				}
				for _, raw := range ids {
					text, ok := raw.(string)
					if !ok || !bounded(text, 1, 128) {
						return false
					}
				}
			case []string:
				if len(ids) > 100 {
					return false
				}
				for _, text := range ids {
					if !bounded(text, 1, 128) {
						return false
					}
				}
			default:
				return false
			}
		}
	}
	return true
}
func validChallengeVersions(versions map[string]any) bool {
	allowed := map[string]bool{"work": true, "contract": true, "operation": true, "terminal_work": true, "predecessor": true, "successor": true, "from": true, "to": true, "from_contract": true, "to_contract": true}
	for key, value := range versions {
		if !allowed[key] {
			return false
		}
		switch typed := value.(type) {
		case int:
			if typed < 1 {
				return false
			}
		case int64:
			if typed < 1 {
				return false
			}
		case float64:
			if typed < 1 || typed != float64(int64(typed)) {
				return false
			}
		case json.Number:
			if n, err := typed.Int64(); err != nil || n < 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func (s *Service) ValidateAndConsumeApprovalTx(ctx context.Context, tx *store.Transaction, ref string, check ApprovalCheck) error {
	if err := s.authorityReady("agent_approval_consume"); err != nil {
		return err
	}
	if tx == nil || len(ref) != 64 {
		return transactionInvalid("agent_approval_consume")
	}
	approval, err := store.ReadApprovalTx(ctx, tx, ref)
	if err != nil {
		return errors.New("approval not found")
	}
	_, approvalExpired := expiryPassed(approval.ExpiresAt, s.now())
	if approval.ClientStatus != "active" || approval.RevokedAt != "" || approval.UsedCount >= approval.MaxUses || approvalExpired || approval.OperationDigest != check.OperationDigest || approval.Consequence != check.Consequence || approval.ClientRef != check.ClientRef || approval.SessionRef != check.SessionRef {
		return errors.New("approval binding invalid")
	}
	wantScope, _ := json.Marshal(check.Scope)
	wantVersions, _ := json.Marshal(check.Versions)
	if string(wantScope) != approval.ScopeJSON || string(wantVersions) != approval.VersionJSON {
		return errors.New("approval scope or version invalid")
	}
	if err := store.ConsumeApprovalTx(ctx, tx, ref, s.now().Format(time.RFC3339Nano)); err != nil {
		return errors.New("approval was already consumed")
	}
	return nil
}

// ApprovalAuthorityActorTx derives the operator actor from durable core-owned
// authority only. The trusted-client policy, consumed challenge, and consumed
// approval are the identity inputs; no host assertion or model payload can name
// a human.
func (s *Service) ApprovalAuthorityActorTx(ctx context.Context, tx *store.Transaction, in Invocation, approvalRef string) (store.WorkflowActor, error) {
	var actor store.WorkflowActor
	if err := s.authorityReady("agent_approval_authority"); err != nil {
		return actor, err
	}
	if tx == nil || len(approvalRef) != 64 {
		return actor, transactionInvalid("agent_approval_authority")
	}
	authority, err := store.ReadApprovalAuthorityTx(ctx, tx, approvalRef)
	if err != nil {
		return actor, errors.New("consumed approval authority is unavailable")
	}
	if authority.ClientRef != in.ClientRef || authority.UsedCount < authority.MaxUses || authority.MaxUses != 1 || !strings.HasPrefix(authority.ProtectedEvidenceRef, "approval-challenge:") {
		return actor, errors.New("approval authority was not exactly consumed")
	}
	challengeRef := strings.TrimPrefix(authority.ProtectedEvidenceRef, "approval-challenge:")
	if authority.ChallengeStatus != "consumed" {
		return actor, errors.New("approval challenge was not exactly consumed")
	}
	identity := store.ChallengeIdentity{ClientRef: in.ClientRef, SessionRef: in.SessionRef, AgentRef: in.AgentRef, Worktree: in.Worktree}
	if _, err := store.ReadApprovalChallengeTx(ctx, tx, challengeRef, identity); err != nil {
		return actor, errors.New("approval challenge is not bound to the invoking session")
	}
	policyDigest := sha256Hex([]byte("approval-policy-v1\x00" + authority.ClientRef + "|" + authority.PrincipalRef + "|" + authority.CapabilitiesJSON + "|" + authority.ProductScopeJSON + "|" + authority.ProjectScopeJSON))
	actor = store.WorkflowActor{
		PrincipalRef: "approval-authority:" + strings.TrimPrefix(policyDigest, "sha256:"),
		ClientRef:    authority.ClientRef,
		AgentRef:     "approval:" + approvalRef,
		SessionRef:   "challenge:" + challengeRef,
		ActorClass:   store.ActorOperator,
	}
	if err := store.ValidateWorkflowActor(actor); err != nil {
		return store.WorkflowActor{}, err
	}
	actorRef, err := store.WorkflowActorRef(actor)
	if err != nil {
		return store.WorkflowActor{}, err
	}
	executing := store.WorkflowActor{PrincipalRef: in.PrincipalRef, ClientRef: in.ClientRef, AgentRef: in.AgentRef, SessionRef: in.SessionRef, ActorClass: store.ActorAgent}
	executingRef, err := store.WorkflowActorRef(executing)
	if err != nil || actorRef == executingRef {
		return store.WorkflowActor{}, errors.New("approval authority actor relabels the invoking agent")
	}
	return actor, nil
}

// consequenceSummaryFor derives the CD-0037 typed approval prompt from the
// exact spec the challenge was minted with. It runs at mint time only: the
// summary is never stored, so nothing it describes can drift without
// invalidating the challenge that carries the same facts. Tool and operation
// come from the validated invocation, never from caller input.
func consequenceSummaryFor(tool, operation string, spec ApprovalChallengeSpec) *ConsequenceSummary {
	return &ConsequenceSummary{
		Tool:            tool,
		Operation:       operation,
		Consequence:     spec.Consequence,
		OperationDigest: spec.OperationDigest,
		Scope:           approvalScopeBindings(spec.Scope),
		Versions:        approvalVersionBindings(spec.Versions),
		ExpiresAt:       spec.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}
