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
	"strconv"
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
	return &Service{Store: authority, Now: func() time.Time { return time.Now().UTC() }, MaxClockSkew: defaultClockSkew, NonceRetention: 24 * time.Hour}
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

// RegisterClient is retained only as an internal compatibility name; trusted
// client registration always requires an explicit authority policy.
func (s *Service) RegisterClient(ctx context.Context, registration ClientRegistration) error {
	return s.RegisterTrustedClient(ctx, registration)
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
		// worker_evidence is registrable in a client policy but deliberately
		// absent from the grant-request vocabulary below: it authorizes signed
		// worker-evidence writes only, and no bearer grant can carry it.
		if !oneOf(string(capability), "product_read", "work_define", "work_transition", "work_relate", "work_compact", "work_initiative", "cross_scope", "research", string(CapabilityWorkerEvidence)) {
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

// CanonicalAssertion is the byte format signed by a trusted client. It is
// intentionally not JSON: v1 is `v1\0`, followed by fixed, named fields encoded
// as `name=<UTF-8 byte length>:<UTF-8 value>|`. The field order is part of the
// contract and matches generated-contracts.ts. List values are pre-normalized
// by the caller and use comma-separated sorted members.
type SignedAssertion struct {
	ClientRef             string       `json:"client_ref"`
	SessionRef            string       `json:"session_ref"`
	AgentRef              string       `json:"agent_ref"`
	Directory             string       `json:"directory"`
	Worktree              string       `json:"worktree"`
	RequestedProductID    string       `json:"requested_product_id"`
	RequestedProjectIDs   []string     `json:"requested_project_ids"`
	RequestedCapabilities []Capability `json:"requested_capabilities"`
	IssuedAt              time.Time    `json:"issued_at"`
	Nonce                 string       `json:"nonce"`
	ManifestDigest        string       `json:"manifest_digest"`
	Signature             []byte       `json:"signature"`
}

func CanonicalAssertion(a SignedAssertion) []byte {
	values := []string{a.ClientRef, a.SessionRef, a.AgentRef, a.Directory, a.Worktree, a.RequestedProductID, strings.Join(normalizeStrings(a.RequestedProjectIDs), ","), strings.Join(normalizeStrings(capabilityStrings(a.RequestedCapabilities)), ","), a.IssuedAt.UTC().Format(time.RFC3339Nano), a.Nonce, a.ManifestDigest}
	names := []string{"client_ref", "session_ref", "agent_ref", "directory", "worktree", "requested_product_id", "requested_project_ids", "requested_capabilities", "issued_at", "nonce", "manifest_digest"}
	var b strings.Builder
	b.WriteString("v1\x00")
	for i, name := range names {
		v := values[i]
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len([]byte(v))))
		b.WriteByte(':')
		b.WriteString(v)
		b.WriteByte('|')
	}
	return []byte(b.String())
}
func validSignedRequests(a SignedAssertion) bool {
	if a.RequestedProductID != "" && !bounded(a.RequestedProductID, 1, 128) {
		return false
	}
	if len(a.RequestedProjectIDs) > 100 || len(a.RequestedCapabilities) > 32 || !unique(a.RequestedProjectIDs) || !unique(capabilityStrings(a.RequestedCapabilities)) {
		return false
	}
	for _, value := range a.RequestedProjectIDs {
		if !bounded(value, 1, 128) {
			return false
		}
	}
	for _, capability := range a.RequestedCapabilities {
		if !oneOf(string(capability), "product_read", "work_define", "work_transition", "work_relate", "work_compact", "work_initiative", "cross_scope", "research") {
			return false
		}
	}
	return true
}

type GrantRequest struct {
	Assertion SignedAssertion
	ExpiresAt time.Time
	MaxUses   int
}
type Grant struct {
	RecordID          string `json:"grant_ref"`
	Token             string `json:"-"`
	PrincipalRef      string
	ClientRef         string
	SessionRef        string
	AgentRef          string
	ClientKeyID       string
	Directory         string
	Worktree          string
	ManifestDigest    string
	Capabilities      []Capability
	ProductScope      []string
	ProjectScope      []string
	IssuedAt          time.Time
	ExpiresAt         time.Time
	ScopeVersion      string
	CandidateProducts []string
	ScopeSnapshot     map[string]any
}

func (g Grant) String() string {
	return fmt.Sprintf("Grant{client_ref:%s, session_ref:%s, expires_at:%s}", g.ClientRef, g.SessionRef, g.ExpiresAt.Format(time.RFC3339Nano))
}

func (s *Service) IssueGrant(ctx context.Context, req GrantRequest) (Grant, error) {
	var out Grant
	if err := s.authorityReady("agent_issue_grant"); err != nil {
		return out, err
	}
	a := req.Assertion
	if a.ClientRef == "" || a.SessionRef == "" || a.AgentRef == "" || a.Directory == "" || a.Worktree == "" || a.Nonce == "" || !validSignedRequests(a) {
		return out, errors.New("invalid grant request")
	}
	if a.ManifestDigest != ManifestDigest {
		return out, errors.New("manifest digest mismatch")
	}
	now := s.now()
	if a.IssuedAt.Before(now.Add(-s.skew())) || a.IssuedAt.After(now.Add(s.skew())) {
		return out, errors.New("assertion timestamp outside clock skew")
	}
	if req.ExpiresAt.IsZero() || !req.ExpiresAt.After(now) || req.ExpiresAt.Sub(now) > 24*time.Hour {
		return out, errors.New("grant expiry outside bound")
	}
	if len(a.Nonce) < 16 || len(a.Nonce) > 256 {
		return out, errors.New("nonce outside bound")
	}
	if len(a.Signature) != ed25519.SignatureSize {
		return out, errors.New("invalid assertion signature")
	}
	var scopeVersion string
	candidateProducts := append([]string(nil), func() []string {
		if a.RequestedProductID != "" {
			return []string{a.RequestedProductID}
		}
		return nil
	}()...)
	scopeSnapshot := map[string]any{}
	var resolvedProject store.ProjectResolution
	var resolvedProducts []string
	if s.ProjectResolver != nil {
		var resolveErr error
		resolvedProject, resolveErr = s.ProjectResolver(ctx, a.Directory, a.Worktree)
		if resolveErr != nil {
			return out, resolveErr
		}
		var scopeErr error
		scopeVersion, resolvedProducts, scopeErr = s.Store.ScopeVersion(ctx, resolvedProject.ProjectID)
		if scopeErr != nil {
			return out, scopeErr
		}
	}
	requestedCaps := normalizeStrings(capabilityStrings(a.RequestedCapabilities))
	requestedProducts := []string{}
	if a.RequestedProductID != "" {
		requestedProducts = []string{a.RequestedProductID}
	}
	requestedProjects := normalizeStrings(a.RequestedProjectIDs)
	if req.MaxUses < 0 || req.MaxUses > 1000000 {
		return out, errors.New("grant uses outside bound")
	}
	var err error
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return out, err
	}
	token := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	recordBytes := make([]byte, 32)
	if _, err := rand.Read(recordBytes); err != nil {
		return out, err
	}
	recordID := hex.EncodeToString(recordBytes)
	var client store.TrustedClientRecord
	var key store.TrustedClientKeyRecord
	err = s.Store.Transact(ctx, func(tx *store.Transaction) error {
		var lookupErr error
		client, key, lookupErr = store.TrustedClientForGrantTx(ctx, tx, a.ClientRef)
		if lookupErr != nil {
			return errors.New("unknown or keyless client")
		}
		if client.Status != "active" || key.Status != "active" {
			return errors.New("client is revoked")
		}
		if !ed25519.Verify(ed25519.PublicKey(key.PublicKey), CanonicalAssertion(a), a.Signature) {
			return errors.New("invalid client assertion signature")
		}
		var policyCaps, policyProducts, policyProjects []string
		if json.Unmarshal([]byte(client.CapabilitiesJSON), &policyCaps) != nil || json.Unmarshal([]byte(client.ProductScopeJSON), &policyProducts) != nil || json.Unmarshal([]byte(client.ProjectScopeJSON), &policyProjects) != nil {
			return errors.New("invalid client authority policy")
		}
		if !subset(requestedCaps, policyCaps) || !subset(requestedProducts, policyProducts) || !subset(requestedProjects, policyProjects) {
			return errors.New("requested authority exceeds trusted client policy")
		}
		if s.ProjectResolver != nil {
			candidateProducts = intersect(resolvedProducts, policyProducts)
			if len(requestedProducts) > 0 && !subset(requestedProducts, candidateProducts) {
				return errors.New("requested Product is not in the resolved host scope")
			}
			if len(candidateProducts) == 0 {
				return errors.New("resolved Project is outside trusted client Product policy")
			}
			requestedProjects = []string{resolvedProject.ProjectID}
			if !subset(requestedProjects, policyProjects) {
				return errors.New("resolved Project is outside trusted client policy")
			}
			// CD-0008 D1: the main checkout stays on the default branch, so it
			// never carries mutation authority. Reads from trunk remain valid.
			if resolvedProject.MainWorktree {
				for _, capability := range requestedCaps {
					if capability != "product_read" {
						return errors.New("mutating authority requires a linked worktree; the main checkout is read-only")
					}
				}
			}
			scopeSnapshot = map[string]any{"project_id": resolvedProject.ProjectID, "product_ids": candidateProducts, "scope_version": scopeVersion}
		}
		caps := requestedCaps
		products := candidateProducts
		projects := requestedProjects
		derivedCapsJSON, _ := json.Marshal(caps)
		derivedProductsJSON, _ := json.Marshal(products)
		derivedProjectsJSON, _ := json.Marshal(projects)
		snapshotJSON, _ := json.Marshal(scopeSnapshot)
		candidatesJSON, _ := json.Marshal(candidateProducts)
		return store.PersistGrantTx(ctx, tx, store.GrantInsert{RecordID: recordID, TokenHash: hash[:], PrincipalRef: client.PrincipalRef, ClientRef: a.ClientRef, SessionRef: a.SessionRef, AgentRef: a.AgentRef, Directory: a.Directory, Worktree: a.Worktree, ClientKeyID: key.KeyID, ManifestDigest: ManifestDigest, CapabilitiesJSON: string(derivedCapsJSON), ProductScopeJSON: string(derivedProductsJSON), ProjectScopeJSON: string(derivedProjectsJSON), IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: req.ExpiresAt.UTC().Format(time.RFC3339Nano), MaxUses: req.MaxUses, ScopeVersion: scopeVersion, ScopeSnapshotJSON: string(snapshotJSON), CandidateProductsJSON: string(candidatesJSON), Nonce: a.Nonce, NonceObservedAt: now.Format(time.RFC3339Nano), NonceExpiresAt: now.Add(s.skew()).Format(time.RFC3339Nano), NoncePruneBefore: now.Add(-s.skew()).Format(time.RFC3339Nano)})
	})
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Op == "agent_nonce" {
			return out, errors.New("assertion nonce replayed")
		}
		return out, err
	}
	return Grant{RecordID: recordID, Token: token, PrincipalRef: client.PrincipalRef, ClientRef: a.ClientRef, SessionRef: a.SessionRef, AgentRef: a.AgentRef, Directory: a.Directory, Worktree: a.Worktree, ClientKeyID: key.KeyID, ManifestDigest: ManifestDigest, Capabilities: capabilityValues(requestedCaps), ProductScope: candidateProducts, ProjectScope: requestedProjects, IssuedAt: now, ExpiresAt: req.ExpiresAt.UTC(), ScopeVersion: scopeVersion, CandidateProducts: candidateProducts, ScopeSnapshot: scopeSnapshot}, nil
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

type Invocation struct {
	GrantToken                                                                         string
	ClientRef, PrincipalRef, SessionRef, AgentRef, Directory, Worktree, ManifestDigest string
	HostAssertionDigest                                                                string
	RequiredCapability                                                                 Capability
	ProductID, ProjectID                                                               string
}

func (s *Service) ValidateInvocation(ctx context.Context, in Invocation) (Grant, error) {
	if err := s.authorityReady("agent_validate_invocation"); err != nil {
		return Grant{}, err
	}
	if len(in.GrantToken) != 64 {
		return Grant{}, errors.New("invalid grant token")
	}
	record, err := s.Store.Grant(ctx, sha256Bytes([]byte(in.GrantToken)))
	if err != nil {
		return Grant{}, errors.New("unknown grant")
	}
	return s.validateGrantRecord(record, in, s.now())
}
func (s *Service) ConsumeGrant(ctx context.Context, in Invocation) error {
	if err := s.authorityReady("agent_consume_grant"); err != nil {
		return err
	}
	_, err := s.consumeGrant(ctx, nil, in, true)
	return err
}

// ValidateAndConsumeGrantTx is the mutation authorization boundary. The caller
// owns tx and must commit it together with the authorized domain effect.
func (s *Service) ValidateAndConsumeGrantTx(ctx context.Context, tx *store.Transaction, in Invocation) (Grant, error) {
	if err := s.authorityReady("agent_validate_grant"); err != nil {
		return Grant{}, err
	}
	if tx == nil {
		return Grant{}, transactionInvalid("agent_validate_grant")
	}
	return s.consumeGrant(ctx, tx, in, true)
}

// ValidateGrantTx validates a grant inside the caller-owned transaction without
// consuming a use. Mutation idempotency is checked after this identity lookup
// and before grant consumption, so replay never burns another grant use.
func (s *Service) ValidateGrantTx(ctx context.Context, tx *store.Transaction, in Invocation) (Grant, error) {
	if err := s.authorityReady("agent_validate_grant"); err != nil {
		return Grant{}, err
	}
	if tx == nil {
		return Grant{}, transactionInvalid("agent_validate_grant")
	}
	return s.validateGrantTx(ctx, tx, in)
}

func (s *Service) validateGrantTx(ctx context.Context, tx *store.Transaction, in Invocation) (Grant, error) {
	if err := s.authorityReady("agent_validate_grant"); err != nil {
		return Grant{}, err
	}
	if tx == nil {
		return Grant{}, transactionInvalid("agent_validate_grant")
	}
	return s.consumeGrant(ctx, tx, in, false)
}

func (s *Service) consumeGrant(ctx context.Context, tx *store.Transaction, in Invocation, consume bool) (Grant, error) {
	if err := s.authorityReady("agent_consume_grant"); err != nil {
		return Grant{}, err
	}
	if len(in.GrantToken) != 64 {
		return Grant{}, errors.New("invalid grant token")
	}
	hash := sha256Bytes([]byte(in.GrantToken))
	var record store.GrantRecord
	var err error
	if tx != nil {
		record, err = store.GrantTx(ctx, tx, hash)
	} else {
		record, err = s.Store.Grant(ctx, hash)
	}
	if err != nil {
		return Grant{}, errors.New("unknown grant")
	}
	g, err := s.validateGrantRecord(record, in, s.now())
	if err != nil {
		return Grant{}, err
	}
	if !consume {
		return g, nil
	}
	if tx != nil {
		err = store.ConsumeGrantTx(ctx, tx, hash, in.ClientRef, s.now().Format(time.RFC3339Nano))
	} else {
		err = s.Store.ConsumeGrant(ctx, hash, in.ClientRef, s.now().Format(time.RFC3339Nano))
	}
	if err != nil {
		return Grant{}, errors.New("grant consumption lost authorization race")
	}
	return g, nil
}

// expiryPassed reports whether a stored RFC3339Nano expiry is at or before now,
// comparing parsed instants rather than their encodings. RFC3339Nano omits
// trailing zeros, so a value carrying a fractional second and one without it do
// not sort chronologically: '.' (0x2E) precedes 'Z' (0x5A). A stored value that
// does not parse cannot be compared and counts as expired, so corruption at the
// authorization boundary fails closed.
func expiryPassed(stored string, now time.Time) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, stored)
	if err != nil {
		return time.Time{}, true
	}
	return parsed, !parsed.After(now)
}

func (s *Service) validateGrantRecord(record store.GrantRecord, in Invocation, now time.Time) (Grant, error) {
	var g Grant
	expiresAt, expired := expiryPassed(record.ExpiresAt, now)
	if record.ClientStatus != "active" || record.ClientKeyID != record.ActiveKeyID || record.RevokedAt != "" || record.ManifestDigest != ManifestDigest || expired {
		return g, errors.New("grant expired or revoked")
	}
	if record.ClientRef != in.ClientRef || record.PrincipalRef != in.PrincipalRef || record.SessionRef != in.SessionRef || record.AgentRef != in.AgentRef || record.Directory != in.Directory || record.Worktree != in.Worktree || in.ManifestDigest != record.ManifestDigest {
		return g, errors.New("invocation binding mismatch")
	}
	if record.MaxUses > 0 && record.UsedCount >= record.MaxUses {
		return g, errors.New("grant use limit reached")
	}
	if err := json.Unmarshal([]byte(record.CapabilitiesJSON), &g.Capabilities); err != nil || !containsCapability(g.Capabilities, in.RequiredCapability) {
		return g, errors.New("grant capability missing")
	}
	if err := json.Unmarshal([]byte(record.ProductScopeJSON), &g.ProductScope); err != nil {
		return g, err
	}
	if err := json.Unmarshal([]byte(record.ProjectScopeJSON), &g.ProjectScope); err != nil {
		return g, err
	}
	g.RecordID, g.PrincipalRef, g.ClientRef, g.SessionRef, g.AgentRef = record.RecordID, record.PrincipalRef, record.ClientRef, record.SessionRef, record.AgentRef
	g.Directory, g.Worktree, g.ClientKeyID = record.Directory, record.Worktree, record.ClientKeyID
	g.ManifestDigest = record.ManifestDigest
	g.ScopeVersion = record.ScopeVersion
	// A snapshot that fails to parse leaves the decoded scope nil, and a nil
	// scope satisfies containment by missing every lookup. Reject it rather
	// than let a corrupt record widen the grant.
	if err := json.Unmarshal([]byte(record.ScopeSnapshotJSON), &g.ScopeSnapshot); err != nil {
		return Grant{}, errors.New("grant scope snapshot is unreadable")
	}
	if err := json.Unmarshal([]byte(record.CandidateProductsJSON), &g.CandidateProducts); err != nil {
		return Grant{}, errors.New("grant candidate products are unreadable")
	}
	g.Token = in.GrantToken
	g.IssuedAt, _ = time.Parse(time.RFC3339Nano, record.IssuedAt)
	g.ExpiresAt = expiresAt
	if in.ProductID != "" && !contains(g.ProductScope, in.ProductID) {
		return g, errors.New("product outside grant scope")
	}
	if in.ProjectID != "" && !contains(g.ProjectScope, in.ProjectID) {
		return g, errors.New("project outside grant scope")
	}
	return g, nil
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
	Nonce         string   `json:"nonce"`
	// Deprecated compatibility fields are intentionally ignored and excluded
	// from signed bytes. Operator attribution comes from durable approval
	// authority, never from adapter-selected identity.
	OperatorPrincipalRef string `json:"-"`
	OperatorAgentRef     string `json:"-"`
	OperatorSessionRef   string `json:"-"`
	Signature            []byte `json:"signature"`
}

func CanonicalHostApprovalAssertion(a HostApprovalAssertion) []byte {
	scope, _ := json.Marshal(a.Scope)
	versions, _ := json.Marshal(a.Versions)
	values := []string{a.ChallengeRef, a.RequestDigest, string(scope), string(versions), a.SessionRef, a.AgentRef, a.Worktree, a.IssuedAt, a.Nonce}
	names := []string{"challenge_ref", "request_digest", "scope", "versions", "session_ref", "agent_ref", "worktree", "issued_at", "nonce"}
	var b strings.Builder
	b.WriteString("host-approval-v1\x00")
	for i, name := range names {
		value := values[i]
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len([]byte(value))))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte('|')
	}
	return []byte(b.String())
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
	if tx == nil || len(assertion.Signature) != ed25519.SignatureSize || assertion.ChallengeRef != check.ApprovalRef || assertion.RequestDigest != check.OperationDigest || assertion.SessionRef != in.SessionRef || assertion.AgentRef != in.AgentRef || assertion.Worktree != in.Worktree || len(assertion.Nonce) < 16 || len(assertion.Nonce) > 256 {
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
	key, err := store.ReadTrustedClientKeyTx(ctx, tx, in.ClientRef, in.PrincipalRef)
	if err != nil || key.Status != "active" || key.KeyID == "" {
		return false, errors.New("trusted client key unavailable")
	}
	if !ed25519.Verify(ed25519.PublicKey(key.PublicKey), CanonicalHostApprovalAssertion(assertion), assertion.Signature) {
		return false, errors.New("host approval assertion signature invalid")
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
	if err := store.PruneAndRecordNonceTx(ctx, tx, in.ClientRef, assertion.Nonce, s.now().Format(time.RFC3339Nano), s.now().Add(s.skew()).Format(time.RFC3339Nano), s.now().Add(-s.skew()).Format(time.RFC3339Nano)); err != nil {
		return false, errors.New("host approval assertion nonce replayed")
	}
	return isChallenge, nil
}

// CreateApprovalChallengeTx is the only challenge creation path. Principal,
// client, session, scope, and capabilities come from the active grant; callers
// provide only the operation intent and a host-resolution correlation digest.
func (s *Service) CreateApprovalChallengeTx(ctx context.Context, tx *store.Transaction, in Invocation, spec ApprovalChallengeSpec) (string, error) {
	if err := s.authorityReady("agent_challenge_create"); err != nil {
		return "", err
	}
	if tx == nil || spec.OperationDigest == "" || spec.Scope == nil || spec.Versions == nil || spec.Consequence == "" || spec.HostAssertionDigest == "" || in.HostAssertionDigest != spec.HostAssertionDigest || spec.ExpiresAt.IsZero() {
		return "", transactionInvalid("agent_challenge_create")
	}
	g, err := s.validateGrantTx(ctx, tx, in)
	if err != nil {
		return "", err
	}
	if !validChallengeScope(spec.Scope) || !validChallengeVersions(spec.Versions) || !scopeWithinGrant(spec.Scope, g) {
		return "", errors.New("approval scope exceeds grant scope")
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
	err = store.InsertApprovalChallengeTx(ctx, tx, store.ApprovalChallengeRecord{ChallengeRef: ref, GrantRef: g.RecordID, OperationDigest: spec.OperationDigest, ScopeJSON: string(scope), VersionJSON: string(versions), Consequence: spec.Consequence, HostAssertionDigest: spec.HostAssertionDigest, IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: spec.ExpiresAt.UTC().Format(time.RFC3339Nano), Status: "active", MaxUses: 1})
	if err != nil {
		return "", err
	}
	return ref, nil
}

// CreateApprovalFromChallengeTx derives all identity fields from the grant and
// atomically consumes the core-issued challenge before recording approval.
func (s *Service) CreateApprovalFromChallengeTx(ctx context.Context, tx *store.Transaction, in Invocation, challengeRef string) (string, error) {
	if err := s.authorityReady("agent_approval_create"); err != nil {
		return "", err
	}
	if tx == nil || len(challengeRef) != 64 {
		return "", transactionInvalid("agent_approval_create")
	}
	g, err := s.validateGrantTx(ctx, tx, in)
	if err != nil {
		return "", err
	}
	challenge, err := store.ReadApprovalChallengeTx(ctx, tx, challengeRef, g.RecordID)
	if err != nil {
		return "", errors.New("approval challenge not found")
	}
	_, challengeExpired := expiryPassed(challenge.ExpiresAt, s.now())
	if challenge.Status != "active" || challenge.UsedCount >= challenge.MaxUses || challenge.HostAssertionDigest != in.HostAssertionDigest || challengeExpired {
		return "", errors.New("approval challenge invalid")
	}
	if err := store.ConsumeApprovalChallengeTx(ctx, tx, challengeRef, g.RecordID, s.now().Format(time.RFC3339Nano)); err != nil {
		return "", errors.New("approval challenge consumption lost race")
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(raw)
	evidenceRef := "approval-challenge:" + challengeRef
	evidenceDigest := sha256Hex([]byte(challenge.HostAssertionDigest + "|" + challenge.OperationDigest + "|" + challenge.ScopeJSON + "|" + challenge.VersionJSON))
	err = store.InsertApprovalTx(ctx, tx, store.ApprovalInsert{ApprovalRef: ref, OperationDigest: challenge.OperationDigest, ScopeJSON: challenge.ScopeJSON, VersionJSON: challenge.VersionJSON, Consequence: challenge.Consequence, HumanPrincipalRef: g.PrincipalRef, ClientRef: g.ClientRef, SessionRef: g.SessionRef, IssuedAt: s.now().Format(time.RFC3339Nano), ExpiresAt: challenge.ExpiresAt, MaxUses: 1, ProtectedEvidenceRef: evidenceRef, ProtectedEvidenceDigest: evidenceDigest})
	if err != nil {
		return "", err
	}
	return ref, nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// authorizedScopeFromSnapshot decodes a stored scope snapshot for re-checking
// against the current grant. An absent snapshot carries no scope constraint and
// decodes to nil, but a present snapshot that fails to parse must surface the
// error: scopeWithinGrant reads a nil scope as satisfying every lookup, so a
// discarded parse failure would widen the grant instead of narrowing it.
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

func scopeWithinGrant(scope map[string]any, grant Grant) bool {
	if product, ok := scope["product_id"].(string); ok && !contains(grant.ProductScope, product) {
		return false
	}
	switch products := scope["product_ids"].(type) {
	case []any:
		for _, raw := range products {
			product, ok := raw.(string)
			if !ok || !contains(grant.ProductScope, product) {
				return false
			}
		}
	case []string:
		for _, product := range products {
			if !contains(grant.ProductScope, product) {
				return false
			}
		}
	}
	switch projects := scope["project_ids"].(type) {
	case []any:
		for _, raw := range projects {
			project, ok := raw.(string)
			if !ok || !contains(grant.ProjectScope, project) {
				return false
			}
		}
	case []string:
		for _, project := range projects {
			if !contains(grant.ProjectScope, project) {
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
	currentGrant, err := store.GrantRefTx(ctx, tx, sha256Bytes([]byte(in.GrantToken)), in.ClientRef)
	if err != nil || currentGrant != authority.ChallengeGrantRef {
		return actor, errors.New("approval challenge is not bound to the invoking grant")
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

func (s *Service) RevokeApproval(ctx context.Context, ref string) error {
	if err := s.authorityReady("agent_revoke_approval"); err != nil {
		return err
	}
	if len(ref) != 64 {
		return errors.New("invalid approval reference")
	}
	if err := s.Store.RevokeApproval(ctx, ref, s.now().Format(time.RFC3339Nano)); err != nil {
		return errors.New("approval not found or already revoked")
	}
	return nil
}

func (s *Service) RevokeApprovalChallenge(ctx context.Context, ref string) error {
	if err := s.authorityReady("agent_revoke_challenge"); err != nil {
		return err
	}
	if len(ref) != 64 {
		return errors.New("invalid approval challenge reference")
	}
	if s.Store.RevokeApprovalChallenge(ctx, ref) != nil {
		return errors.New("approval challenge not found or already closed")
	}
	return nil
}

func (s *Service) RevokeGrant(ctx context.Context, token string) error {
	if err := s.authorityReady("agent_revoke_grant"); err != nil {
		return err
	}
	if len(token) != 64 {
		return errors.New("invalid grant token")
	}
	if s.Store.RevokeGrant(ctx, sha256Bytes([]byte(token)), token, s.now().Format(time.RFC3339Nano)) != nil {
		return errors.New("grant not found or already revoked")
	}
	return nil
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
