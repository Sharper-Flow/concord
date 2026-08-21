package store

import (
	"context"
	"database/sql"
)

// TrustedClientRecord is the durable trusted-client projection used by the
// agent authority. JSON policy values remain opaque to store callers.
type TrustedClientRecord struct {
	ClientRef        string
	Status           string
	PrincipalRef     string
	CapabilitiesJSON string
	ProductScopeJSON string
	ProjectScopeJSON string
}

type TrustedClientKeyRecord struct {
	ClientRef string
	KeyID     string
	PublicKey []byte
	Status    string
}

// GrantRecord is the physical agent_grants row. Timestamps and policy JSON are
// kept as their stored representations so the domain layer owns interpretation.
type GrantRecord struct {
	RecordID, PrincipalRef, ClientRef, SessionRef, AgentRef string
	Directory, Worktree, ClientKeyID, ManifestDigest        string
	CapabilitiesJSON, ProductScopeJSON, ProjectScopeJSON    string
	IssuedAt, ExpiresAt                                     string
	RevokedAt                                               string
	MaxUses, UsedCount                                      int
	ScopeVersion, ScopeSnapshotJSON, CandidateProductsJSON  string
	ClientStatus, ActiveKeyID                               string
}

type GrantInsert struct {
	RecordID                                                 string
	TokenHash                                                []byte
	PrincipalRef, ClientRef, SessionRef, AgentRef            string
	Directory, Worktree, ClientKeyID, ManifestDigest         string
	CapabilitiesJSON, ProductScopeJSON, ProjectScopeJSON     string
	IssuedAt, ExpiresAt                                      string
	MaxUses                                                  int
	ScopeVersion, ScopeSnapshotJSON, CandidateProductsJSON   string
	Nonce, NonceObservedAt, NonceExpiresAt, NoncePruneBefore string
}

type ApprovalChallengeRecord struct {
	ChallengeRef, GrantRef, OperationDigest                  string
	ScopeJSON, VersionJSON, Consequence, HostAssertionDigest string
	IssuedAt, ExpiresAt, Status                              string
	MaxUses, UsedCount                                       int
}

type ApprovalInsert struct {
	ApprovalRef, OperationDigest, ScopeJSON, VersionJSON, Consequence string
	HumanPrincipalRef, ClientRef, SessionRef                          string
	IssuedAt, ExpiresAt                                               string
	MaxUses                                                           int
	ProtectedEvidenceRef, ProtectedEvidenceDigest                     string
}

type ApprovalRecord struct {
	ApprovalRef, OperationDigest, ScopeJSON, VersionJSON, Consequence string
	HumanPrincipalRef, ClientRef, SessionRef                          string
	IssuedAt, ExpiresAt, RevokedAt, ProtectedEvidenceRef              string
	MaxUses, UsedCount                                                int
	ClientStatus                                                      string
}

type ApprovalAuthorityRecord struct {
	ClientRef, ProtectedEvidenceRef, PrincipalRef        string
	CapabilitiesJSON, ProductScopeJSON, ProjectScopeJSON string
	UsedCount, MaxUses                                   int
	ChallengeGrantRef, ChallengeStatus, CurrentGrantRef  string
}

func (s *Store) RegisterTrustedClient(ctx context.Context, client TrustedClientRecord, key TrustedClientKeyRecord, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_register_client")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES(?,?,?,?,?,?,?)`, client.ClientRef, client.Status, client.PrincipalRef, client.CapabilitiesJSON, client.ProductScopeJSON, client.ProjectScopeJSON, now); err != nil {
			return wrapFailure(KindProjectionConflict, "agent_register_client", "cannot persist trusted client", false, "choose an unused client reference", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`, key.ClientRef, key.KeyID, key.PublicKey, key.Status, now); err != nil {
			return wrapFailure(KindProjectionConflict, "agent_register_client", "cannot persist trusted client key", false, "choose an unused key identifier", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func (s *Store) UpdateTrustedClientPolicy(ctx context.Context, clientRef string, policy TrustedClientRecord, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_update_policy")
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_clients SET principal_ref=?,capabilities_json=?,product_scope_json=?,project_scope_json=? WHERE client_ref=? AND status='active'`, policy.PrincipalRef, policy.CapabilitiesJSON, policy.ProductScopeJSON, policy.ProjectScopeJSON, clientRef)
		if err != nil {
			return wrapFailure(KindUnavailable, "agent_update_policy", "cannot update trusted client policy", true, "retry the policy update", err)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return newFailure(KindProjectionNotFound, "agent_update_policy", "trusted client not found or revoked", false, "reread the trusted client")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE client_ref=? AND revoked_at IS NULL`, now, clientRef); err != nil {
			return wrapFailure(KindUnavailable, "agent_update_policy", "cannot revoke prior grants", true, "retry the policy update", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func (s *Store) RotateTrustedClientKey(ctx context.Context, clientRef string, key TrustedClientKeyRecord, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_rotate_key")
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_clients WHERE client_ref=?`, clientRef).Scan(&status); err != nil {
			return wrapFailure(KindProjectionNotFound, "agent_rotate_key", "client is not recorded", false, "reread the trusted client", err)
		}
		if status != "active" {
			return newFailure(KindInvalidOperation, "agent_rotate_key", "client is revoked", false, "restore the client before rotating its key")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_client_keys SET status='revoked',revoked_at=? WHERE client_ref=? AND status='active'`, now, clientRef); err != nil {
			return wrapFailure(KindUnavailable, "agent_rotate_key", "cannot revoke the prior client key", true, "retry key rotation", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE client_ref=? AND revoked_at IS NULL`, now, clientRef); err != nil {
			return wrapFailure(KindUnavailable, "agent_rotate_key", "cannot revoke prior grants", true, "retry key rotation", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`, clientRef, key.KeyID, key.PublicKey, key.Status, now); err != nil {
			return wrapFailure(KindProjectionConflict, "agent_rotate_key", "cannot persist the new client key", false, "choose an unused key identifier", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_clients SET rotated_at=? WHERE client_ref=?`, now, clientRef); err != nil {
			return wrapFailure(KindUnavailable, "agent_rotate_key", "cannot record key rotation", true, "retry key rotation", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func (s *Store) RevokeTrustedClient(ctx context.Context, clientRef, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_revoke_client")
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_clients SET status='revoked',revoked_at=? WHERE client_ref=? AND status='active'`, now, clientRef)
		if err != nil {
			return wrapFailure(KindUnavailable, "agent_revoke_client", "cannot revoke client", true, "retry client revocation", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return wrapFailure(KindUnavailable, "agent_revoke_client", "cannot verify client revocation", true, "retry client revocation", err)
		}
		if n != 1 {
			return newFailure(KindProjectionNotFound, "agent_revoke_client", "trusted client not found or already revoked", false, "reread the trusted client")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_client_keys SET status='revoked',revoked_at=? WHERE client_ref=? AND status='active'`, now, clientRef); err != nil {
			return wrapFailure(KindUnavailable, "agent_revoke_client", "cannot revoke client keys", true, "retry client revocation", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE client_ref=? AND revoked_at IS NULL`, now, clientRef); err != nil {
			return wrapFailure(KindUnavailable, "agent_revoke_client", "cannot revoke client grants", true, "retry client revocation", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func (s *Store) TrustedClientForGrant(ctx context.Context, clientRef string) (TrustedClientRecord, TrustedClientKeyRecord, error) {
	if s == nil || s.db == nil {
		return TrustedClientRecord{}, TrustedClientKeyRecord{}, newFailure(KindUnavailable, "agent_client_read", "database is not open", true, "open the authority database")
	}
	return trustedClientForGrant(ctx, s.db, clientRef)
}

func TrustedClientForGrantTx(ctx context.Context, transaction *Transaction, clientRef string) (TrustedClientRecord, TrustedClientKeyRecord, error) {
	tx, err := transactionSQL(transaction, "agent_client_read")
	if err != nil {
		return TrustedClientRecord{}, TrustedClientKeyRecord{}, err
	}
	return trustedClientForGrant(ctx, tx, clientRef)
}

func trustedClientForGrant(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, clientRef string) (TrustedClientRecord, TrustedClientKeyRecord, error) {
	var client TrustedClientRecord
	var key TrustedClientKeyRecord
	if err := q.QueryRowContext(ctx, `SELECT c.client_ref,c.status,c.principal_ref,c.capabilities_json,c.product_scope_json,c.project_scope_json,k.key_id,k.public_key,k.status FROM agent_clients c JOIN agent_client_keys k ON k.client_ref=c.client_ref AND k.status='active' WHERE c.client_ref=?`, clientRef).Scan(&client.ClientRef, &client.Status, &client.PrincipalRef, &client.CapabilitiesJSON, &client.ProductScopeJSON, &client.ProjectScopeJSON, &key.KeyID, &key.PublicKey, &key.Status); err != nil {
		return client, key, wrapFailure(KindProjectionNotFound, "agent_client_read", "unknown or keyless client", false, "register an active trusted client", err)
	}
	key.ClientRef = clientRef
	return client, key, nil
}

func (s *Store) PersistGrant(ctx context.Context, input GrantInsert) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "agent_issue_grant", "database is not open", true, "open the authority database")
	}
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_issue_grant")
		if err != nil {
			return err
		}
		return persistNonceAndGrant(ctx, tx, input)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func PersistGrantTx(ctx context.Context, transaction *Transaction, input GrantInsert) error {
	tx, err := transactionSQL(transaction, "agent_issue_grant")
	if err != nil {
		return err
	}
	return persistNonceAndGrant(ctx, tx, input)
}

func persistNonceAndGrant(ctx context.Context, tx *sql.Tx, input GrantInsert) error {
	if input.NoncePruneBefore == "" {
		return newFailure(KindInvalidOperation, "agent_nonce", "nonce prune cutoff is required", false, "supply an explicit nonce prune cutoff")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_nonce_replay WHERE expires_at < ?`, input.NoncePruneBefore); err != nil {
		return wrapFailure(KindUnavailable, "agent_nonce", "cannot prune assertion nonces", true, "retry grant issuance", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_nonce_replay(client_ref,nonce,observed_at,expires_at) VALUES(?,?,?,?)`, input.ClientRef, input.Nonce, input.NonceObservedAt, input.NonceExpiresAt); err != nil {
		return newFailure(KindProjectionConflict, "agent_nonce", "assertion nonce replayed", false, "issue a new assertion nonce")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_grants(grant_ref,grant_hash,principal_ref,client_ref,session_ref,agent_ref,directory,worktree,client_key_id,manifest_digest,capabilities_json,product_scope_json,project_scope_json,issued_at,expires_at,max_uses,scope_version,scope_snapshot_json,candidate_products_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.RecordID, input.TokenHash, input.PrincipalRef, input.ClientRef, input.SessionRef, input.AgentRef, input.Directory, input.Worktree, input.ClientKeyID, input.ManifestDigest, input.CapabilitiesJSON, input.ProductScopeJSON, input.ProjectScopeJSON, input.IssuedAt, input.ExpiresAt, input.MaxUses, input.ScopeVersion, input.ScopeSnapshotJSON, input.CandidateProductsJSON)
	if err != nil {
		return wrapFailure(KindProjectionConflict, "agent_issue_grant", "cannot persist grant", false, "retry with a fresh grant identity", err)
	}
	return nil
}

func (s *Store) Grant(ctx context.Context, tokenHash []byte) (GrantRecord, error) {
	if s == nil || s.db == nil {
		return GrantRecord{}, newFailure(KindUnavailable, "agent_grant_read", "database is not open", true, "open the authority database")
	}
	return grant(ctx, s.db, tokenHash)
}
func GrantTx(ctx context.Context, transaction *Transaction, tokenHash []byte) (GrantRecord, error) {
	tx, err := transactionSQL(transaction, "agent_grant_read")
	if err != nil {
		return GrantRecord{}, err
	}
	return grant(ctx, tx, tokenHash)
}

func grant(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tokenHash []byte) (GrantRecord, error) {
	var g GrantRecord
	if err := q.QueryRowContext(ctx, `SELECT g.grant_ref,g.principal_ref,g.client_ref,g.session_ref,g.agent_ref,g.directory,g.worktree,g.client_key_id,g.manifest_digest,g.capabilities_json,g.product_scope_json,g.project_scope_json,g.issued_at,g.expires_at,COALESCE(g.revoked_at,''),g.max_uses,g.used_count,g.scope_version,g.scope_snapshot_json,g.candidate_products_json,c.status,k.key_id FROM agent_grants g JOIN agent_clients c ON c.client_ref=g.client_ref JOIN agent_client_keys k ON k.client_ref=g.client_ref AND k.status='active' WHERE g.grant_hash=?`, tokenHash).Scan(&g.RecordID, &g.PrincipalRef, &g.ClientRef, &g.SessionRef, &g.AgentRef, &g.Directory, &g.Worktree, &g.ClientKeyID, &g.ManifestDigest, &g.CapabilitiesJSON, &g.ProductScopeJSON, &g.ProjectScopeJSON, &g.IssuedAt, &g.ExpiresAt, &g.RevokedAt, &g.MaxUses, &g.UsedCount, &g.ScopeVersion, &g.ScopeSnapshotJSON, &g.CandidateProductsJSON, &g.ClientStatus, &g.ActiveKeyID); err != nil {
		return g, wrapFailure(KindProjectionNotFound, "agent_grant_read", "unknown grant", false, "issue a valid grant", err)
	}
	return g, nil
}

func (s *Store) ConsumeGrant(ctx context.Context, tokenHash []byte, clientRef, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return ConsumeGrantTx(ctx, transaction, tokenHash, clientRef, now)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func ConsumeGrantTx(ctx context.Context, transaction *Transaction, tokenHash []byte, clientRef, now string) error {
	tx, err := transactionSQL(transaction, "agent_grant_consume")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_grants SET used_count=used_count+1 WHERE grant_hash=? AND client_ref=? AND revoked_at IS NULL AND expires_at>? AND (max_uses=0 OR used_count<max_uses)`, tokenHash, clientRef, now)
	if err != nil {
		return wrapFailure(KindUnavailable, "agent_grant_consume", "cannot consume grant", true, "retry invocation", err)
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return newFailure(KindProjectionConflict, "agent_grant_consume", "grant consumption lost authorization race", false, "reread the grant and retry")
	}
	return nil
}

func ReadTrustedClientKeyTx(ctx context.Context, transaction *Transaction, clientRef, principalRef string) (TrustedClientKeyRecord, error) {
	var key TrustedClientKeyRecord
	tx, err := transactionSQL(transaction, "agent_client_key")
	if err != nil {
		return key, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT k.key_id,k.public_key,k.status FROM agent_clients c JOIN agent_client_keys k ON k.client_ref=c.client_ref AND k.status='active' WHERE c.client_ref=? AND c.principal_ref=? AND c.status='active'`, clientRef, principalRef).Scan(&key.KeyID, &key.PublicKey, &key.Status); err != nil {
		return key, newFailure(KindProjectionNotFound, "agent_client_key", "trusted client key unavailable", false, "reread the active trusted client")
	}
	key.ClientRef = clientRef
	return key, nil
}

func ReadApprovalChallengeTx(ctx context.Context, transaction *Transaction, ref, grantRef string) (ApprovalChallengeRecord, error) {
	var c ApprovalChallengeRecord
	tx, err := transactionSQL(transaction, "agent_challenge_read")
	if err != nil {
		return c, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=? AND grant_ref=?`, ref, grantRef).Scan(&c.ChallengeRef, &c.GrantRef, &c.OperationDigest, &c.ScopeJSON, &c.VersionJSON, &c.Consequence, &c.HostAssertionDigest, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.MaxUses, &c.UsedCount); err != nil {
		return c, newFailure(KindProjectionNotFound, "agent_challenge_read", "approval challenge not found", false, "reread the active approval challenge")
	}
	return c, nil
}

func ReadApprovalChallengeRefTx(ctx context.Context, transaction *Transaction, ref string) (ApprovalChallengeRecord, error) {
	var c ApprovalChallengeRecord
	tx, err := transactionSQL(transaction, "agent_challenge_read")
	if err != nil {
		return c, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=?`, ref).Scan(&c.ChallengeRef, &c.GrantRef, &c.OperationDigest, &c.ScopeJSON, &c.VersionJSON, &c.Consequence, &c.HostAssertionDigest, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.MaxUses, &c.UsedCount); err != nil {
		return c, newFailure(KindProjectionNotFound, "agent_challenge_read", "approval challenge not found", false, "reread the approval challenge")
	}
	return c, nil
}

func InsertApprovalChallengeTx(ctx context.Context, transaction *Transaction, c ApprovalChallengeRecord) error {
	tx, err := transactionSQL(transaction, "agent_challenge_create")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_approval_challenges(challenge_ref,grant_ref,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, c.ChallengeRef, c.GrantRef, c.OperationDigest, c.ScopeJSON, c.VersionJSON, c.Consequence, c.HostAssertionDigest, c.IssuedAt, c.ExpiresAt, c.Status, c.MaxUses, c.UsedCount); err != nil {
		return wrapFailure(KindProjectionConflict, "agent_challenge_create", "cannot persist approval challenge", false, "retry with a fresh challenge reference", err)
	}
	return nil
}

func ConsumeApprovalChallengeTx(ctx context.Context, transaction *Transaction, ref, grantRef, now string) error {
	tx, err := transactionSQL(transaction, "agent_challenge_consume")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_approval_challenges SET status='consumed',consumed_at=?,used_count=used_count+1 WHERE challenge_ref=? AND grant_ref=? AND status='active' AND expires_at>? AND used_count<max_uses`, now, ref, grantRef, now)
	if err != nil {
		return wrapFailure(KindUnavailable, "agent_challenge_consume", "cannot consume approval challenge", true, "retry approval creation", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionConflict, "agent_challenge_consume", "approval challenge consumption lost race", false, "reread the approval challenge")
	}
	return nil
}

func InsertApprovalTx(ctx context.Context, transaction *Transaction, a ApprovalInsert) error {
	tx, err := transactionSQL(transaction, "agent_approval_create")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ApprovalRef, a.OperationDigest, a.ScopeJSON, a.VersionJSON, a.Consequence, a.HumanPrincipalRef, a.ClientRef, a.SessionRef, a.IssuedAt, a.ExpiresAt, a.MaxUses, a.ProtectedEvidenceRef, a.ProtectedEvidenceDigest); err != nil {
		return wrapFailure(KindProjectionConflict, "agent_approval_create", "cannot persist approval", false, "retry with a fresh approval reference", err)
	}
	return nil
}

func ReadApprovalTx(ctx context.Context, transaction *Transaction, ref string) (ApprovalRecord, error) {
	var a ApprovalRecord
	tx, err := transactionSQL(transaction, "agent_approval_read")
	if err != nil {
		return a, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT a.approval_ref,a.operation_digest,a.scope_json,a.version_json,a.consequence,a.client_ref,a.session_ref,a.expires_at,a.max_uses,a.used_count,COALESCE(a.revoked_at,''),c.status FROM agent_approvals a JOIN agent_clients c ON c.client_ref=a.client_ref WHERE a.approval_ref=?`, ref).Scan(&a.ApprovalRef, &a.OperationDigest, &a.ScopeJSON, &a.VersionJSON, &a.Consequence, &a.ClientRef, &a.SessionRef, &a.ExpiresAt, &a.MaxUses, &a.UsedCount, &a.RevokedAt, &a.ClientStatus); err != nil {
		return a, newFailure(KindProjectionNotFound, "agent_approval_read", "approval not found", false, "reread the approval")
	}
	return a, nil
}

func ConsumeApprovalTx(ctx context.Context, transaction *Transaction, ref, now string) error {
	tx, err := transactionSQL(transaction, "agent_approval_consume")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_approvals SET used_count=used_count+1 WHERE approval_ref=? AND revoked_at IS NULL AND used_count<max_uses`, ref)
	if err != nil {
		return wrapFailure(KindUnavailable, "agent_approval_consume", "cannot consume approval", true, "retry the authorized operation", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionConflict, "agent_approval_consume", "approval was already consumed", false, "reread the approval")
	}
	return nil
}

func ReadApprovalAuthorityTx(ctx context.Context, transaction *Transaction, approvalRef string) (ApprovalAuthorityRecord, error) {
	var a ApprovalAuthorityRecord
	tx, err := transactionSQL(transaction, "agent_approval_authority")
	if err != nil {
		return a, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT a.client_ref,a.protected_evidence_ref,a.used_count,a.max_uses,c.principal_ref,c.capabilities_json,c.product_scope_json,c.project_scope_json FROM agent_approvals a JOIN agent_clients c ON c.client_ref=a.client_ref WHERE a.approval_ref=? AND c.status='active'`, approvalRef).Scan(&a.ClientRef, &a.ProtectedEvidenceRef, &a.UsedCount, &a.MaxUses, &a.PrincipalRef, &a.CapabilitiesJSON, &a.ProductScopeJSON, &a.ProjectScopeJSON); err != nil {
		return a, newFailure(KindProjectionNotFound, "agent_approval_authority", "consumed approval authority is unavailable", false, "reread the consumed approval")
	}
	challengeRef := ""
	if len(a.ProtectedEvidenceRef) > len("approval-challenge:") {
		challengeRef = a.ProtectedEvidenceRef[len("approval-challenge:"):]
	}
	if challengeRef == "" || tx.QueryRowContext(ctx, `SELECT grant_ref,status FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&a.ChallengeGrantRef, &a.ChallengeStatus) != nil {
		return a, newFailure(KindProjectionNotFound, "agent_approval_authority", "approval challenge was not exactly consumed", false, "reread the approval challenge")
	}
	return a, nil
}

func GrantRefTx(ctx context.Context, transaction *Transaction, tokenHash []byte, clientRef string) (string, error) {
	var grantRef string
	tx, err := transactionSQL(transaction, "agent_grant_read")
	if err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT grant_ref FROM agent_grants WHERE grant_hash=? AND client_ref=?`, tokenHash, clientRef).Scan(&grantRef); err != nil {
		return "", newFailure(KindProjectionNotFound, "agent_grant_read", "invoking grant is unavailable", false, "reread the invoking grant")
	}
	return grantRef, nil
}

func PruneAndRecordNonceTx(ctx context.Context, transaction *Transaction, clientRef, nonce, observedAt, expiresAt, pruneBefore string) error {
	tx, err := transactionSQL(transaction, "agent_nonce")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_nonce_replay WHERE expires_at < ?`, pruneBefore); err != nil {
		return wrapFailure(KindUnavailable, "agent_nonce", "cannot prune assertion nonces", true, "retry the assertion", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_nonce_replay(client_ref,nonce,observed_at,expires_at) VALUES(?,?,?,?)`, clientRef, nonce, observedAt, expiresAt); err != nil {
		return newFailure(KindProjectionConflict, "agent_nonce", "host approval assertion nonce replayed", false, "issue a new assertion nonce")
	}
	return nil
}

func (s *Store) RevokeGrant(ctx context.Context, tokenHash []byte, token, now string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "agent_revoke_grant", "database is not open", true, "open the authority database")
	}
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_revoke_grant")
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE grant_hash=? AND revoked_at IS NULL`, now, tokenHash)
		if err != nil {
			return wrapFailure(KindUnavailable, "agent_revoke_grant", "cannot revoke grant", true, "retry grant revocation", err)
		}
		if n, _ := result.RowsAffected(); n == 0 {
			result, err = tx.ExecContext(ctx, `UPDATE agent_grants SET revoked_at=? WHERE grant_ref=? AND revoked_at IS NULL`, now, token)
			if err != nil {
				return wrapFailure(KindUnavailable, "agent_revoke_grant", "cannot revoke grant", true, "retry grant revocation", err)
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return newFailure(KindProjectionNotFound, "agent_revoke_grant", "grant not found or already revoked", false, "reread the grant")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func (s *Store) RevokeApproval(ctx context.Context, ref, now string) error {
	return s.revoke(ctx, `UPDATE agent_approvals SET revoked_at=? WHERE approval_ref=? AND revoked_at IS NULL`, now, ref)
}
func (s *Store) RevokeApprovalChallenge(ctx context.Context, ref string) error {
	return s.revoke(ctx, `UPDATE agent_approval_challenges SET status='revoked' WHERE challenge_ref=? AND status='active'`, ref)
}
func (s *Store) revoke(ctx context.Context, query string, args ...string) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "agent_revoke", "database is not open", true, "open the authority database")
	}
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "agent_revoke")
		if err != nil {
			return err
		}
		values := make([]any, len(args))
		for i, value := range args {
			values[i] = value
		}
		result, err := tx.ExecContext(ctx, query, values...)
		if err != nil {
			return wrapFailure(KindUnavailable, "agent_revoke", "cannot persist revocation", true, "retry revocation", err)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return newFailure(KindProjectionNotFound, "agent_revoke", args[len(args)-1], false, "reread the authority record")
		}
		return nil
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}
