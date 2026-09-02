package store

import (
	"context"
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

type ApprovalChallengeRecord struct {
	ChallengeRef, ClientRef, PrincipalRef, SessionRef, AgentRef string
	Directory, Worktree, ProductScopeJSON                       string
	OperationDigest, ScopeJSON, VersionJSON                     string
	Consequence, HostAssertionDigest                            string
	IssuedAt, ExpiresAt, Status                                 string
	MaxUses, UsedCount                                          int
}

type ChallengeIdentity struct {
	ClientRef, SessionRef, AgentRef, Worktree string
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
	ChallengeStatus                                      string
}

func (s *Store) RegisterTrustedClient(ctx context.Context, client TrustedClientRecord, key TrustedClientKeyRecord, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return registerTrustedClientTx(ctx, transaction, client, key, now)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func registerTrustedClientTx(ctx context.Context, transaction *Transaction, client TrustedClientRecord, key TrustedClientKeyRecord, now string) error {
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
}

func (s *Store) UpdateTrustedClientPolicy(ctx context.Context, clientRef string, policy TrustedClientRecord, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return updateTrustedClientPolicyTx(ctx, transaction, clientRef, policy, now)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func updateTrustedClientPolicyTx(ctx context.Context, transaction *Transaction, clientRef string, policy TrustedClientRecord, now string) error {
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
	return nil
}

// MutateTrustedClientPolicy reads the active client named by clientRef, applies
// mutate to the current record, and persists the returned record in the same
// transaction. The read and the write are one step, so a concurrent policy
// change cannot be lost between them. Policy JSON stays opaque to this layer:
// mutate interprets it. An error from mutate aborts the transaction with no
// write, and the caller owns that error's typing.
func (s *Store) MutateTrustedClientPolicy(ctx context.Context, clientRef string, mutate func(current TrustedClientRecord) (TrustedClientRecord, error)) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return mutateTrustedClientPolicyTx(ctx, transaction, clientRef, mutate)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func mutateTrustedClientPolicyTx(ctx context.Context, transaction *Transaction, clientRef string, mutate func(TrustedClientRecord) (TrustedClientRecord, error)) error {
	tx, err := transactionSQL(transaction, "agent_mutate_policy")
	if err != nil {
		return err
	}
	var current TrustedClientRecord
	if err := tx.QueryRowContext(ctx, `SELECT client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json FROM agent_clients WHERE client_ref=? AND status='active'`, clientRef).Scan(&current.ClientRef, &current.Status, &current.PrincipalRef, &current.CapabilitiesJSON, &current.ProductScopeJSON, &current.ProjectScopeJSON); err != nil {
		return wrapFailure(KindProjectionNotFound, "agent_mutate_policy", "trusted client not found or revoked", false, "reread the trusted client", err)
	}
	next, err := mutate(current)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_clients SET principal_ref=?,capabilities_json=?,product_scope_json=?,project_scope_json=? WHERE client_ref=? AND status='active'`, next.PrincipalRef, next.CapabilitiesJSON, next.ProductScopeJSON, next.ProjectScopeJSON, clientRef)
	if err != nil {
		return wrapFailure(KindUnavailable, "agent_mutate_policy", "cannot update trusted client policy", true, "retry the policy update", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "agent_mutate_policy", "trusted client not found or revoked", false, "reread the trusted client")
	}
	return nil
}

func (s *Store) RotateTrustedClientKey(ctx context.Context, clientRef string, key TrustedClientKeyRecord, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return rotateTrustedClientKeyTx(ctx, transaction, clientRef, key, now)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func rotateTrustedClientKeyTx(ctx context.Context, transaction *Transaction, clientRef string, key TrustedClientKeyRecord, now string) error {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`, clientRef, key.KeyID, key.PublicKey, key.Status, now); err != nil {
		return wrapFailure(KindProjectionConflict, "agent_rotate_key", "cannot persist the new client key", false, "choose an unused key identifier", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_clients SET rotated_at=? WHERE client_ref=?`, now, clientRef); err != nil {
		return wrapFailure(KindUnavailable, "agent_rotate_key", "cannot record key rotation", true, "retry key rotation", err)
	}
	return nil
}

func (s *Store) RevokeTrustedClient(ctx context.Context, clientRef, now string) error {
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return revokeTrustedClientTx(ctx, transaction, clientRef, now)
	})
	if err != nil {
		return err
	}
	// committed; the durability barrier must hold before acknowledging
	return s.SyncDurable(ctx)
}

func revokeTrustedClientTx(ctx context.Context, transaction *Transaction, clientRef, now string) error {
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
	return nil
}

func (s *Store) TrustedClientWithKey(ctx context.Context, clientRef string) (TrustedClientRecord, TrustedClientKeyRecord, error) {
	if s == nil || s.db == nil {
		return TrustedClientRecord{}, TrustedClientKeyRecord{}, newFailure(KindUnavailable, "agent_client_read", "database is not open", true, "open the authority database")
	}
	return trustedClientWithKey(ctx, s.db, clientRef)
}

func TrustedClientWithKeyTx(ctx context.Context, transaction *Transaction, clientRef string) (TrustedClientRecord, TrustedClientKeyRecord, error) {
	tx, err := transactionSQL(transaction, "agent_client_read")
	if err != nil {
		return TrustedClientRecord{}, TrustedClientKeyRecord{}, err
	}
	return trustedClientWithKey(ctx, tx, clientRef)
}

func trustedClientWithKey(ctx context.Context, q queryer, clientRef string) (TrustedClientRecord, TrustedClientKeyRecord, error) {
	var client TrustedClientRecord
	var key TrustedClientKeyRecord
	if err := q.QueryRowContext(ctx, `SELECT c.client_ref,c.status,c.principal_ref,c.capabilities_json,c.product_scope_json,c.project_scope_json,k.key_id,k.public_key,k.status FROM agent_clients c JOIN agent_client_keys k ON k.client_ref=c.client_ref AND k.status='active' WHERE c.client_ref=?`, clientRef).Scan(&client.ClientRef, &client.Status, &client.PrincipalRef, &client.CapabilitiesJSON, &client.ProductScopeJSON, &client.ProjectScopeJSON, &key.KeyID, &key.PublicKey, &key.Status); err != nil {
		return client, key, wrapFailure(KindProjectionNotFound, "agent_client_read", "unknown or keyless client", false, "register an active trusted client", err)
	}
	key.ClientRef = clientRef
	return client, key, nil
}

func ReadApprovalChallengeTx(ctx context.Context, transaction *Transaction, ref string, identity ChallengeIdentity) (ApprovalChallengeRecord, error) {
	var c ApprovalChallengeRecord
	tx, err := transactionSQL(transaction, "agent_challenge_read")
	if err != nil {
		return c, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=? AND client_ref=? AND session_ref=? AND agent_ref=? AND worktree=?`, ref, identity.ClientRef, identity.SessionRef, identity.AgentRef, identity.Worktree).Scan(&c.ChallengeRef, &c.ClientRef, &c.PrincipalRef, &c.SessionRef, &c.AgentRef, &c.Directory, &c.Worktree, &c.ProductScopeJSON, &c.OperationDigest, &c.ScopeJSON, &c.VersionJSON, &c.Consequence, &c.HostAssertionDigest, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.MaxUses, &c.UsedCount); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=?`, ref).Scan(&c.ChallengeRef, &c.ClientRef, &c.PrincipalRef, &c.SessionRef, &c.AgentRef, &c.Directory, &c.Worktree, &c.ProductScopeJSON, &c.OperationDigest, &c.ScopeJSON, &c.VersionJSON, &c.Consequence, &c.HostAssertionDigest, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.MaxUses, &c.UsedCount); err != nil {
		return c, newFailure(KindProjectionNotFound, "agent_challenge_read", "approval challenge not found", false, "reread the approval challenge")
	}
	return c, nil
}

func InsertApprovalChallengeTx(ctx context.Context, transaction *Transaction, c ApprovalChallengeRecord) error {
	tx, err := transactionSQL(transaction, "agent_challenge_create")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_approval_challenges(challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ChallengeRef, c.ClientRef, c.PrincipalRef, c.SessionRef, c.AgentRef, c.Directory, c.Worktree, c.ProductScopeJSON, c.OperationDigest, c.ScopeJSON, c.VersionJSON, c.Consequence, c.HostAssertionDigest, c.IssuedAt, c.ExpiresAt, c.Status, c.MaxUses, c.UsedCount); err != nil {
		return wrapFailure(KindProjectionConflict, "agent_challenge_create", "cannot persist approval challenge", false, "retry with a fresh challenge reference", err)
	}
	return nil
}

func ConsumeApprovalChallengeTx(ctx context.Context, transaction *Transaction, ref string, identity ChallengeIdentity, now string) error {
	tx, err := transactionSQL(transaction, "agent_challenge_consume")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_approval_challenges SET status='consumed',consumed_at=?,used_count=used_count+1 WHERE challenge_ref=? AND client_ref=? AND session_ref=? AND agent_ref=? AND worktree=? AND status='active' AND expires_at>? AND used_count<max_uses`, now, ref, identity.ClientRef, identity.SessionRef, identity.AgentRef, identity.Worktree, now)
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
	if challengeRef == "" || tx.QueryRowContext(ctx, `SELECT status FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&a.ChallengeStatus) != nil {
		return a, newFailure(KindProjectionNotFound, "agent_approval_authority", "approval challenge was not exactly consumed", false, "reread the approval challenge")
	}
	return a, nil
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
