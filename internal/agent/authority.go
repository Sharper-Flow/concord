package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
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
	DB             *sql.DB
	Now            Clock
	MaxClockSkew   time.Duration
	NonceRetention time.Duration
	// ProjectResolver is installed by the CLI boundary. Keeping it injectable
	// gives tests a deterministic git runner while ensuring routine model input
	// cannot supply a Project or Product authority.
	ProjectResolver func(context.Context, string, string) (store.ProjectResolution, error)
}

func NewService(db *sql.DB) *Service {
	return &Service{DB: db, Now: func() time.Time { return time.Now().UTC() }, MaxClockSkew: defaultClockSkew, NonceRetention: 24 * time.Hour}
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
	if s.DB == nil || registration.ClientRef == "" || registration.KeyID == "" || len(registration.PublicKey) != ed25519.PublicKeySize || !validTrustedPolicy(registration.Policy) {
		return errors.New("invalid client registration")
	}
	now := s.now().Format(time.RFC3339Nano)
	policy := canonicalPolicy(registration.Policy)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`, registration.ClientRef, "active", registration.Policy.PrincipalRef, policy.capabilities, policy.products, policy.projects, now); err != nil {
		return fmt.Errorf("register client: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`, registration.ClientRef, registration.KeyID, []byte(registration.PublicKey), "active", now); err != nil {
		return fmt.Errorf("register client key: %w", err)
	}
	return tx.Commit()
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
		if !oneOf(string(capability), "product_read", "work_define", "work_transition", "work_relate", "work_compact", "cross_scope") {
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
	if clientRef == "" || !validTrustedPolicy(policy) {
		return errors.New("invalid trusted client policy")
	}
	p := canonicalPolicy(policy)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_clients SET principal_ref=?,capabilities_json=?,product_scope_json=?,project_scope_json=? WHERE client_ref=? AND status='active'`, policy.PrincipalRef, p.capabilities, p.products, p.projects, clientRef)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("trusted client not found or revoked")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE client_ref=? AND revoked_at IS NULL`, s.now().Format(time.RFC3339Nano), clientRef); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) RotateClientKey(ctx context.Context, registration ClientRegistration) error {
	if s.DB == nil || registration.ClientRef == "" || registration.KeyID == "" || len(registration.PublicKey) != ed25519.PublicKeySize {
		return errors.New("invalid client rotation")
	}
	now := s.now().Format(time.RFC3339Nano)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM agent_clients WHERE client_ref=?`, registration.ClientRef).Scan(&status); err != nil {
		return fmt.Errorf("rotate client: %w", err)
	}
	if status != "active" {
		return errors.New("client is revoked")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_client_keys SET status='revoked',revoked_at=? WHERE client_ref=? AND status='active'`, now, registration.ClientRef); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE client_ref=? AND revoked_at IS NULL`, now, registration.ClientRef); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`, registration.ClientRef, registration.KeyID, []byte(registration.PublicKey), "active", now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_clients SET rotated_at=? WHERE client_ref=?`, now, registration.ClientRef); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) RevokeClient(ctx context.Context, clientRef string) error {
	if clientRef == "" {
		return errors.New("empty client reference")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_clients SET status='revoked',revoked_at=? WHERE client_ref=? AND status='active'`, now, clientRef); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_client_keys SET status='revoked',revoked_at=? WHERE client_ref=? AND status='active'`, now, clientRef); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE client_ref=? AND revoked_at IS NULL`, now, clientRef); err != nil {
		return err
	}
	return tx.Commit()
}

// CanonicalAssertion is the byte format signed by a trusted client. It is
// intentionally not JSON: v1 is `v1\0`, followed by fixed, named fields encoded
// as `name=<UTF-8 byte length>:<UTF-8 value>|`. The field order is part of the
// contract and matches generated-contracts.ts. List values are pre-normalized
// by the caller and use comma-separated sorted members.
type SignedAssertion struct {
	ClientRef             string       `json:"client_ref"`
	ClientVersion         string       `json:"client_version"`
	SessionRef            string       `json:"session_ref"`
	AgentRef              string       `json:"agent_ref"`
	Directory             string       `json:"directory"`
	Worktree              string       `json:"worktree"`
	RequestedProductID    string       `json:"requested_product_id"`
	RequestedProjectIDs   []string     `json:"requested_project_ids"`
	RequestedCapabilities []Capability `json:"requested_capabilities"`
	IssuedAt              time.Time    `json:"issued_at"`
	Nonce                 string       `json:"nonce"`
	SurfaceRange          string       `json:"surface_range"`
	EnvelopeVersions      string       `json:"envelope_versions"`
	ManifestDigest        string       `json:"manifest_digest"`
	Signature             []byte       `json:"signature"`
}

func CanonicalAssertion(a SignedAssertion) []byte {
	values := []string{a.ClientRef, a.ClientVersion, a.SessionRef, a.AgentRef, a.Directory, a.Worktree, a.RequestedProductID, strings.Join(normalizeStrings(a.RequestedProjectIDs), ","), strings.Join(normalizeStrings(capabilityStrings(a.RequestedCapabilities)), ","), a.IssuedAt.UTC().Format(time.RFC3339Nano), a.Nonce, a.SurfaceRange, a.EnvelopeVersions, a.ManifestDigest}
	names := []string{"client_ref", "client_version", "session_ref", "agent_ref", "directory", "worktree", "requested_product_id", "requested_project_ids", "requested_capabilities", "issued_at", "nonce", "surface_range", "envelope_versions", "manifest_digest"}
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
		if !oneOf(string(capability), "product_read", "work_define", "work_transition", "work_relate", "work_compact", "cross_scope") {
			return false
		}
	}
	return true
}

type GrantRequest struct {
	Assertion       SignedAssertion
	SurfaceVersion  string
	EnvelopeVersion string
	ExpiresAt       time.Time
	MaxUses         int
}
type Grant struct {
	RecordID          string `json:"grant_ref"`
	Token             string `json:"-"`
	PrincipalRef      string
	ClientRef         string
	SessionRef        string
	AgentRef          string
	ClientVersion     string
	ClientKeyID       string
	Directory         string
	Worktree          string
	SurfaceVersion    string
	EnvelopeVersion   string
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
	if s.DB == nil {
		return out, errors.New("nil authority database")
	}
	a := req.Assertion
	if a.ClientRef == "" || a.SessionRef == "" || a.AgentRef == "" || a.Directory == "" || a.Worktree == "" || a.Nonce == "" || a.ManifestDigest != ManifestDigest || !validSignedRequests(a) {
		return out, errors.New("invalid grant request")
	}
	selectedSurface, negotiationErr := NegotiateSurfaceVersion(a.SurfaceRange)
	selectedEnvelope := "1.0"
	if a.SurfaceRange == "" || a.EnvelopeVersions == "" || negotiationErr != nil || !containsVersion(a.EnvelopeVersions, selectedEnvelope) {
		return out, errors.New("unsupported contract version")
	}
	req.SurfaceVersion = selectedSurface
	req.EnvelopeVersion = selectedEnvelope
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
		scopeVersion, resolvedProducts, scopeErr = store.ScopeVersionForDB(ctx, s.DB, resolvedProject.ProjectID)
		if scopeErr != nil {
			return out, scopeErr
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var status, keyStatus, keyID, principal, capsJSON, productsJSON, projectsJSON string
	var publicKey []byte
	if err = tx.QueryRowContext(ctx, `SELECT c.status,k.status,k.key_id,k.public_key,c.principal_ref,c.capabilities_json,c.product_scope_json,c.project_scope_json FROM agent_clients c JOIN agent_client_keys k ON k.client_ref=c.client_ref AND k.status='active' WHERE c.client_ref=?`, a.ClientRef).Scan(&status, &keyStatus, &keyID, &publicKey, &principal, &capsJSON, &productsJSON, &projectsJSON); err != nil {
		return out, errors.New("unknown or keyless client")
	}
	if status != "active" || keyStatus != "active" {
		return out, errors.New("client is revoked")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), CanonicalAssertion(a), a.Signature) {
		return out, errors.New("invalid client assertion signature")
	}
	var policyCaps, policyProducts, policyProjects []string
	if json.Unmarshal([]byte(capsJSON), &policyCaps) != nil || json.Unmarshal([]byte(productsJSON), &policyProducts) != nil || json.Unmarshal([]byte(projectsJSON), &policyProjects) != nil {
		return out, errors.New("invalid client authority policy")
	}
	requestedCaps := normalizeStrings(capabilityStrings(a.RequestedCapabilities))
	requestedProducts := []string{}
	if a.RequestedProductID != "" {
		requestedProducts = []string{a.RequestedProductID}
	}
	requestedProjects := normalizeStrings(a.RequestedProjectIDs)
	if !subset(requestedCaps, policyCaps) || !subset(requestedProducts, policyProducts) || !subset(requestedProjects, policyProjects) {
		return out, errors.New("requested authority exceeds trusted client policy")
	}
	if s.ProjectResolver != nil {
		candidateProducts = resolvedProducts
		candidateProducts = intersect(candidateProducts, policyProducts)
		if len(requestedProducts) > 0 && !subset(requestedProducts, candidateProducts) {
			return out, errors.New("requested Product is not in the resolved host scope")
		}
		if len(candidateProducts) == 0 {
			return out, errors.New("resolved Project is outside trusted client Product policy")
		}
		requestedProjects = []string{resolvedProject.ProjectID}
		if !subset(requestedProjects, policyProjects) {
			return out, errors.New("resolved Project is outside trusted client policy")
		}
		scopeSnapshot = map[string]any{"project_id": resolvedProject.ProjectID, "product_ids": candidateProducts, "scope_version": scopeVersion}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_nonce_replay WHERE expires_at < ?`, now.Add(-s.skew()).Format(time.RFC3339Nano)); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_nonce_replay(client_ref,nonce,observed_at,expires_at) VALUES(?,?,?,?)`, a.ClientRef, a.Nonce, now.Format(time.RFC3339Nano), now.Add(s.skew()).Format(time.RFC3339Nano)); err != nil {
		return out, errors.New("assertion nonce replayed")
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return out, err
	}
	token := hex.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	recordBytes := make([]byte, 32)
	if _, err = rand.Read(recordBytes); err != nil {
		return out, err
	}
	recordID := hex.EncodeToString(recordBytes)
	caps := requestedCaps
	products := candidateProducts
	projects := requestedProjects
	derivedCapsJSON, _ := json.Marshal(caps)
	derivedProductsJSON, _ := json.Marshal(products)
	derivedProjectsJSON, _ := json.Marshal(projects)
	if req.MaxUses < 0 || req.MaxUses > 1000000 {
		return out, errors.New("grant uses outside bound")
	}
	snapshotJSON, _ := json.Marshal(scopeSnapshot)
	candidatesJSON, _ := json.Marshal(candidateProducts)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_grants(grant_ref,grant_hash,principal_ref,client_ref,session_ref,agent_ref,directory,worktree,client_version,client_key_id,surface_version,envelope_version,manifest_digest,capabilities_json,product_scope_json,project_scope_json,issued_at,expires_at,max_uses,scope_version,scope_snapshot_json,candidate_products_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, recordID, hash[:], principal, a.ClientRef, a.SessionRef, a.AgentRef, a.Directory, a.Worktree, a.ClientVersion, keyID, req.SurfaceVersion, req.EnvelopeVersion, ManifestDigest, string(derivedCapsJSON), string(derivedProductsJSON), string(derivedProjectsJSON), now.Format(time.RFC3339Nano), req.ExpiresAt.UTC().Format(time.RFC3339Nano), req.MaxUses, scopeVersion, string(snapshotJSON), string(candidatesJSON))
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return Grant{RecordID: recordID, Token: token, PrincipalRef: principal, ClientRef: a.ClientRef, SessionRef: a.SessionRef, AgentRef: a.AgentRef, Directory: a.Directory, Worktree: a.Worktree, ClientVersion: a.ClientVersion, ClientKeyID: keyID, SurfaceVersion: req.SurfaceVersion, EnvelopeVersion: req.EnvelopeVersion, ManifestDigest: ManifestDigest, Capabilities: capabilityValues(caps), ProductScope: products, ProjectScope: projects, IssuedAt: now, ExpiresAt: req.ExpiresAt.UTC(), ScopeVersion: scopeVersion, CandidateProducts: candidateProducts, ScopeSnapshot: scopeSnapshot}, nil
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
	GrantToken                                                                                                                         string
	ClientRef, ClientVersion, PrincipalRef, SessionRef, AgentRef, Directory, Worktree, SurfaceVersion, EnvelopeVersion, ManifestDigest string
	HostAssertionDigest                                                                                                                string
	RequiredCapability                                                                                                                 Capability
	ProductID, ProjectID                                                                                                               string
}

func (s *Service) ValidateInvocation(ctx context.Context, in Invocation) (Grant, error) {
	var g Grant
	if len(in.GrantToken) != 64 {
		return g, errors.New("invalid grant token")
	}
	hash := sha256.Sum256([]byte(in.GrantToken))
	var capsJSON, productsJSON, projectsJSON string
	var issued, expires, scopeVersion, scopeSnapshotJSON, candidateProductsJSON string
	var revoked sql.NullString
	var maxUses, used int
	var clientStatus, activeKeyID string
	err := s.DB.QueryRowContext(ctx, `SELECT g.grant_ref,g.principal_ref,g.client_ref,g.session_ref,g.agent_ref,g.directory,g.worktree,g.client_version,g.client_key_id,g.surface_version,g.envelope_version,g.manifest_digest,g.capabilities_json,g.product_scope_json,g.project_scope_json,g.issued_at,g.expires_at,g.revoked_at,g.max_uses,g.used_count,g.scope_version,g.scope_snapshot_json,g.candidate_products_json,c.status,k.key_id FROM agent_grants g JOIN agent_clients c ON c.client_ref=g.client_ref JOIN agent_client_keys k ON k.client_ref=g.client_ref AND k.status='active' WHERE g.grant_hash=?`, hash[:]).Scan(&g.RecordID, &g.PrincipalRef, &g.ClientRef, &g.SessionRef, &g.AgentRef, &g.Directory, &g.Worktree, &g.ClientVersion, &g.ClientKeyID, &g.SurfaceVersion, &g.EnvelopeVersion, &g.ManifestDigest, &capsJSON, &productsJSON, &projectsJSON, &issued, &expires, &revoked, &maxUses, &used, &scopeVersion, &scopeSnapshotJSON, &candidateProductsJSON, &clientStatus, &activeKeyID)
	if err != nil {
		return g, errors.New("unknown grant")
	}
	now := s.now()
	if clientStatus != "active" || g.ClientKeyID != activeKeyID || revoked.Valid || g.ManifestDigest != ManifestDigest || expires <= now.Format(time.RFC3339Nano) {
		return g, errors.New("grant expired or revoked")
	}
	if g.ClientRef != in.ClientRef || g.ClientVersion != in.ClientVersion || g.PrincipalRef != in.PrincipalRef || g.SessionRef != in.SessionRef || g.AgentRef != in.AgentRef || g.Directory != in.Directory || g.Worktree != in.Worktree || g.SurfaceVersion != in.SurfaceVersion || g.EnvelopeVersion != in.EnvelopeVersion || in.ManifestDigest != g.ManifestDigest {
		return g, errors.New("invocation binding mismatch")
	}
	if maxUses > 0 && used >= maxUses {
		return g, errors.New("grant use limit reached")
	}
	if err = json.Unmarshal([]byte(capsJSON), &g.Capabilities); err != nil {
		return g, err
	}
	if !containsCapability(g.Capabilities, in.RequiredCapability) {
		return g, errors.New("grant capability missing")
	}
	if err = json.Unmarshal([]byte(productsJSON), &g.ProductScope); err != nil {
		return g, err
	}
	if err = json.Unmarshal([]byte(projectsJSON), &g.ProjectScope); err != nil {
		return g, err
	}
	g.ScopeVersion = scopeVersion
	_ = json.Unmarshal([]byte(scopeSnapshotJSON), &g.ScopeSnapshot)
	_ = json.Unmarshal([]byte(candidateProductsJSON), &g.CandidateProducts)
	g.Token = in.GrantToken
	g.IssuedAt, _ = time.Parse(time.RFC3339Nano, issued)
	g.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if in.ProductID != "" && !contains(g.ProductScope, in.ProductID) {
		return g, errors.New("product outside grant scope")
	}
	if in.ProjectID != "" && !contains(g.ProjectScope, in.ProjectID) {
		return g, errors.New("project outside grant scope")
	}
	return g, nil
}
func (s *Service) ConsumeGrant(ctx context.Context, in Invocation) error {
	_, err := s.consumeGrant(ctx, nil, in, true)
	return err
}

// ValidateAndConsumeGrantTx is the mutation authorization boundary. The caller
// owns tx and must commit it together with the authorized domain effect.
func (s *Service) ValidateAndConsumeGrantTx(ctx context.Context, tx *sql.Tx, in Invocation) (Grant, error) {
	if tx == nil {
		return Grant{}, errors.New("nil grant transaction")
	}
	return s.consumeGrant(ctx, tx, in, true)
}

// ValidateGrantTx validates a grant inside the caller-owned transaction without
// consuming a use. Mutation idempotency is checked after this identity lookup
// and before grant consumption, so replay never burns another grant use.
func (s *Service) ValidateGrantTx(ctx context.Context, tx *sql.Tx, in Invocation) (Grant, error) {
	if tx == nil {
		return Grant{}, errors.New("nil grant transaction")
	}
	return s.validateGrantTx(ctx, tx, in)
}

func (s *Service) validateGrantTx(ctx context.Context, tx *sql.Tx, in Invocation) (Grant, error) {
	return s.consumeGrant(ctx, tx, in, false)
}

func (s *Service) consumeGrant(ctx context.Context, tx *sql.Tx, in Invocation, consume bool) (Grant, error) {
	if len(in.GrantToken) != 64 {
		return Grant{}, errors.New("invalid grant token")
	}
	hash := sha256Bytes([]byte(in.GrantToken))
	var q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}
	var exec interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}
	if tx != nil {
		q, exec = tx, tx
	} else {
		q, exec = s.DB, s.DB
	}
	var g Grant
	var capsJSON, productsJSON, projectsJSON, issued, expires, scopeVersion, scopeSnapshotJSON, candidateProductsJSON string
	var revoked sql.NullString
	var maxUses, used int
	var clientStatus, activeKeyID string
	err := q.QueryRowContext(ctx, `SELECT g.grant_ref,g.principal_ref,g.client_ref,g.session_ref,g.agent_ref,g.directory,g.worktree,g.client_version,g.client_key_id,g.surface_version,g.envelope_version,g.manifest_digest,g.capabilities_json,g.product_scope_json,g.project_scope_json,g.issued_at,g.expires_at,g.revoked_at,g.max_uses,g.used_count,g.scope_version,g.scope_snapshot_json,g.candidate_products_json,c.status,k.key_id FROM agent_grants g JOIN agent_clients c ON c.client_ref=g.client_ref JOIN agent_client_keys k ON k.client_ref=g.client_ref AND k.status='active' WHERE g.grant_hash=?`, hash).Scan(&g.RecordID, &g.PrincipalRef, &g.ClientRef, &g.SessionRef, &g.AgentRef, &g.Directory, &g.Worktree, &g.ClientVersion, &g.ClientKeyID, &g.SurfaceVersion, &g.EnvelopeVersion, &g.ManifestDigest, &capsJSON, &productsJSON, &projectsJSON, &issued, &expires, &revoked, &maxUses, &used, &scopeVersion, &scopeSnapshotJSON, &candidateProductsJSON, &clientStatus, &activeKeyID)
	if err != nil {
		return Grant{}, errors.New("unknown grant")
	}
	now := s.now()
	if clientStatus != "active" || g.ClientKeyID != activeKeyID || revoked.Valid || g.ManifestDigest != ManifestDigest || expires <= now.Format(time.RFC3339Nano) {
		return Grant{}, errors.New("grant expired or revoked")
	}
	if g.ClientRef != in.ClientRef || g.ClientVersion != in.ClientVersion || g.PrincipalRef != in.PrincipalRef || g.SessionRef != in.SessionRef || g.AgentRef != in.AgentRef || g.Directory != in.Directory || g.Worktree != in.Worktree || g.SurfaceVersion != in.SurfaceVersion || g.EnvelopeVersion != in.EnvelopeVersion || in.ManifestDigest != g.ManifestDigest {
		return Grant{}, errors.New("invocation binding mismatch")
	}
	if maxUses > 0 && used >= maxUses {
		return Grant{}, errors.New("grant use limit reached")
	}
	if err := json.Unmarshal([]byte(capsJSON), &g.Capabilities); err != nil || !containsCapability(g.Capabilities, in.RequiredCapability) {
		return Grant{}, errors.New("grant capability missing")
	}
	if err := json.Unmarshal([]byte(productsJSON), &g.ProductScope); err != nil {
		return Grant{}, err
	}
	if err := json.Unmarshal([]byte(projectsJSON), &g.ProjectScope); err != nil {
		return Grant{}, err
	}
	g.ScopeVersion = scopeVersion
	_ = json.Unmarshal([]byte(scopeSnapshotJSON), &g.ScopeSnapshot)
	_ = json.Unmarshal([]byte(candidateProductsJSON), &g.CandidateProducts)
	if in.ProductID != "" && !contains(g.ProductScope, in.ProductID) {
		return Grant{}, errors.New("product outside grant scope")
	}
	if in.ProjectID != "" && !contains(g.ProjectScope, in.ProjectID) {
		return Grant{}, errors.New("project outside grant scope")
	}
	g.Token = in.GrantToken
	g.IssuedAt, _ = time.Parse(time.RFC3339Nano, issued)
	g.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if !consume {
		return g, nil
	}
	result, err := exec.ExecContext(ctx, `UPDATE agent_grants SET used_count=used_count+1 WHERE grant_hash=? AND client_ref=? AND revoked_at IS NULL AND expires_at>? AND (max_uses=0 OR used_count<max_uses)`, hash, in.ClientRef, now.Format(time.RFC3339Nano))
	if err != nil {
		return Grant{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return Grant{}, errors.New("grant consumption lost authorization race")
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

func containsVersion(values, wanted string) bool {
	for _, value := range strings.Split(values, ",") {
		if strings.TrimSpace(value) == wanted {
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
	ApprovalRef     string
	OperationDigest string
	Scope           map[string]any
	Versions        map[string]any
	Consequence     string
	ClientRef       string
	SessionRef      string
}

type HostApprovalAssertion struct {
	ChallengeRef  string   `json:"challenge_ref"`
	RequestDigest string   `json:"request_digest"`
	Scope         []string `json:"scope"`
	Versions      []string `json:"versions"`
	SessionRef    string   `json:"session_ref"`
	AgentRef      string   `json:"agent_ref"`
	Worktree      string   `json:"worktree"`
	ClientVersion string   `json:"client_version"`
	IssuedAt      string   `json:"issued_at"`
	Nonce         string   `json:"nonce"`
	Signature     []byte   `json:"signature"`
}

func CanonicalHostApprovalAssertion(a HostApprovalAssertion) []byte {
	scope, _ := json.Marshal(a.Scope)
	versions, _ := json.Marshal(a.Versions)
	values := []string{a.ChallengeRef, a.RequestDigest, string(scope), string(versions), a.SessionRef, a.AgentRef, a.Worktree, a.ClientVersion, a.IssuedAt, a.Nonce}
	names := []string{"challenge_ref", "request_digest", "scope", "versions", "session_ref", "agent_ref", "worktree", "client_version", "issued_at", "nonce"}
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
func (s *Service) ValidateHostApprovalAssertionTx(ctx context.Context, tx *sql.Tx, in Invocation, assertion HostApprovalAssertion, check ApprovalCheck) (bool, error) {
	if tx == nil || len(assertion.Signature) != ed25519.SignatureSize || assertion.ChallengeRef != check.ApprovalRef || assertion.RequestDigest != check.OperationDigest || assertion.SessionRef != in.SessionRef || assertion.AgentRef != in.AgentRef || assertion.Worktree != in.Worktree || assertion.ClientVersion != in.ClientVersion || len(assertion.Nonce) < 16 || len(assertion.Nonce) > 256 {
		return false, errors.New("host approval assertion binding invalid")
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
	var status, keyStatus, keyID string
	var publicKey []byte
	if err := tx.QueryRowContext(ctx, `SELECT c.status,k.status,k.key_id,k.public_key FROM agent_clients c JOIN agent_client_keys k ON k.client_ref=c.client_ref AND k.status='active' WHERE c.client_ref=? AND c.principal_ref=?`, in.ClientRef, in.PrincipalRef).Scan(&status, &keyStatus, &keyID, &publicKey); err != nil || status != "active" || keyStatus != "active" || keyID == "" {
		return false, errors.New("trusted client key unavailable")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), CanonicalHostApprovalAssertion(assertion), assertion.Signature) {
		return false, errors.New("host approval assertion signature invalid")
	}
	var challengeDigest, challengeScope, challengeVersions, challengeConsequence, challengeHost, challengeExpires, challengeStatus string
	challengeErr := tx.QueryRowContext(ctx, `SELECT operation_digest,scope_json,version_json,consequence,host_assertion_digest,expires_at,status FROM agent_approval_challenges WHERE challenge_ref=?`, assertion.ChallengeRef).Scan(&challengeDigest, &challengeScope, &challengeVersions, &challengeConsequence, &challengeHost, &challengeExpires, &challengeStatus)
	isChallenge := challengeErr == nil
	if isChallenge {
		storedScope, _ := json.Marshal(check.Scope)
		storedVersions, _ := json.Marshal(check.Versions)
		if challengeStatus != "active" || challengeExpires <= s.now().Format(time.RFC3339Nano) || challengeDigest != check.OperationDigest || challengeScope != string(storedScope) || challengeVersions != string(storedVersions) || challengeConsequence != check.Consequence || challengeHost != in.HostAssertionDigest {
			return false, errors.New("approval challenge binding invalid")
		}
	} else if challengeErr != sql.ErrNoRows {
		return false, challengeErr
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_nonce_replay WHERE expires_at < ?`, s.now().Add(-s.skew()).Format(time.RFC3339Nano)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_nonce_replay(client_ref,nonce,observed_at,expires_at) VALUES(?,?,?,?)`, in.ClientRef, assertion.Nonce, s.now().Format(time.RFC3339Nano), s.now().Add(s.skew()).Format(time.RFC3339Nano)); err != nil {
		return false, errors.New("host approval assertion nonce replayed")
	}
	return isChallenge, nil
}

// CreateApprovalChallengeTx is the only challenge creation path. Principal,
// client, session, scope, and capabilities come from the active grant; callers
// provide only the operation intent and a host-resolution correlation digest.
func (s *Service) CreateApprovalChallengeTx(ctx context.Context, tx *sql.Tx, in Invocation, spec ApprovalChallengeSpec) (string, error) {
	if tx == nil || spec.OperationDigest == "" || spec.Scope == nil || spec.Versions == nil || spec.Consequence == "" || spec.HostAssertionDigest == "" || in.HostAssertionDigest != spec.HostAssertionDigest || spec.ExpiresAt.IsZero() {
		return "", errors.New("invalid approval challenge")
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
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_approval_challenges(challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, ref, g.RecordID, spec.OperationDigest, string(scope), string(versions), spec.Consequence, spec.HostAssertionDigest, now.Format(time.RFC3339Nano), spec.ExpiresAt.UTC().Format(time.RFC3339Nano), "active", 1, 0)
	if err != nil {
		return "", err
	}
	return ref, nil
}

// CreateApprovalFromChallengeTx derives all identity fields from the grant and
// atomically consumes the core-issued challenge before recording approval.
func (s *Service) CreateApprovalFromChallengeTx(ctx context.Context, tx *sql.Tx, in Invocation, challengeRef string) (string, error) {
	if tx == nil || len(challengeRef) != 64 {
		return "", errors.New("invalid approval challenge reference")
	}
	g, err := s.validateGrantTx(ctx, tx, in)
	if err != nil {
		return "", err
	}
	var digest, scopeJSON, versionJSON, consequence, hostDigest, expires, status string
	var maxUses, usedCount int
	err = tx.QueryRowContext(ctx, `SELECT operation_digest,scope_json,version_json,consequence,host_assertion_digest,expires_at,status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=? AND grant_ref=?`, challengeRef, g.RecordID).Scan(&digest, &scopeJSON, &versionJSON, &consequence, &hostDigest, &expires, &status, &maxUses, &usedCount)
	if err != nil {
		return "", errors.New("approval challenge not found")
	}
	if status != "active" || usedCount >= maxUses || hostDigest != in.HostAssertionDigest || expires <= s.now().Format(time.RFC3339Nano) {
		return "", errors.New("approval challenge invalid")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_approval_challenges SET status='consumed',consumed_at=?,used_count=used_count+1 WHERE challenge_ref=? AND grant_ref=? AND status='active' AND expires_at>? AND used_count<max_uses`, s.now().Format(time.RFC3339Nano), challengeRef, g.RecordID, s.now().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return "", errors.New("approval challenge consumption lost race")
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(raw)
	evidenceRef := "approval-challenge:" + challengeRef
	evidenceDigest := sha256Hex([]byte(hostDigest + "|" + digest + "|" + scopeJSON + "|" + versionJSON))
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, ref, digest, scopeJSON, versionJSON, consequence, g.PrincipalRef, g.ClientRef, g.SessionRef, s.now().Format(time.RFC3339Nano), expires, 1, evidenceRef, evidenceDigest)
	if err != nil {
		return "", err
	}
	return ref, nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
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
	allowed := map[string]bool{"work": true, "operation": true, "terminal_work": true, "predecessor": true, "successor": true, "from": true, "to": true}
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
func (s *Service) ValidateAndConsumeApprovalTx(ctx context.Context, tx *sql.Tx, ref string, check ApprovalCheck) error {
	if tx == nil || len(ref) != 64 {
		return errors.New("invalid approval reference")
	}
	var digest, scopeJSON, versionJSON, consequence, client, session, expires, clientStatus string
	var maxUses, used int
	var revoked sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT a.operation_digest,a.scope_json,a.version_json,a.consequence,a.client_ref,a.session_ref,a.expires_at,a.max_uses,a.used_count,a.revoked_at,c.status FROM agent_approvals a JOIN agent_clients c ON c.client_ref=a.client_ref WHERE a.approval_ref=?`, ref).Scan(&digest, &scopeJSON, &versionJSON, &consequence, &client, &session, &expires, &maxUses, &used, &revoked, &clientStatus)
	if err != nil {
		return errors.New("approval not found")
	}
	if clientStatus != "active" || revoked.Valid || used >= maxUses || expires <= s.now().Format(time.RFC3339Nano) || digest != check.OperationDigest || consequence != check.Consequence || client != check.ClientRef || session != check.SessionRef {
		return errors.New("approval binding invalid")
	}
	wantScope, _ := json.Marshal(check.Scope)
	wantVersions, _ := json.Marshal(check.Versions)
	if string(wantScope) != scopeJSON || string(wantVersions) != versionJSON {
		return errors.New("approval scope or version invalid")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_approvals SET used_count=used_count+1 WHERE approval_ref=? AND revoked_at IS NULL AND used_count<max_uses`, ref)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("approval was already consumed")
	}
	return nil
}

func (s *Service) RevokeApproval(ctx context.Context, ref string) error {
	if len(ref) != 64 {
		return errors.New("invalid approval reference")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE agent_approvals SET revoked_at=? WHERE approval_ref=? AND revoked_at IS NULL`, s.now().Format(time.RFC3339Nano), ref)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("approval not found or already revoked")
	}
	return nil
}

func (s *Service) RevokeApprovalChallenge(ctx context.Context, ref string) error {
	if len(ref) != 64 {
		return errors.New("invalid approval challenge reference")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE agent_approval_challenges SET status='revoked' WHERE challenge_ref=? AND status='active'`, ref)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("approval challenge not found or already closed")
	}
	return nil
}

func (s *Service) RevokeGrant(ctx context.Context, token string) error {
	if len(token) != 64 {
		return errors.New("invalid grant token")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE grant_hash=? AND revoked_at IS NULL`, s.now().Format(time.RFC3339Nano), sha256Bytes([]byte(token)))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		result, err = s.DB.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE grant_ref=? AND revoked_at IS NULL`, s.now().Format(time.RFC3339Nano), token)
		if err != nil {
			return err
		}
		n, _ = result.RowsAffected()
	}
	if n != 1 {
		return errors.New("grant not found or already revoked")
	}
	return nil
}
