package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
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
	AgentScopeJSON   string
	// PolicyRevision increments on every persisted policy write, including a
	// write that restores the exact prior content. The agent layer folds it
	// into the derived policy version so an approval can never match a policy
	// state that was replaced and re-created while the approval was valid.
	PolicyRevision int64
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
	return s.TransactDurable(ctx, func(transaction *Transaction) error {
		return registerTrustedClientTx(ctx, transaction, client, key, now)
	})
}

func registerTrustedClientTx(ctx context.Context, transaction *Transaction, client TrustedClientRecord, key TrustedClientKeyRecord, now string) error {
	tx, err := transactionSQL(transaction, "agent_register_client")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,agent_scope_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, client.ClientRef, client.Status, client.PrincipalRef, client.CapabilitiesJSON, client.ProductScopeJSON, client.ProjectScopeJSON, client.AgentScopeJSON, now); err != nil {
		return wrapFailure(KindProjectionConflict, "agent_register_client", "cannot persist trusted client", false, "choose an unused client reference", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES(?,?,?,?,?)`, key.ClientRef, key.KeyID, key.PublicKey, key.Status, now); err != nil {
		return wrapFailure(KindProjectionConflict, "agent_register_client", "cannot persist trusted client key", false, "choose an unused key identifier", err)
	}
	return nil
}

func (s *Store) UpdateTrustedClientPolicy(ctx context.Context, clientRef string, policy TrustedClientRecord, now string) error {
	return s.TransactDurable(ctx, func(transaction *Transaction) error {
		return updateTrustedClientPolicyTx(ctx, transaction, clientRef, policy)
	})
}

func updateTrustedClientPolicyTx(ctx context.Context, transaction *Transaction, clientRef string, policy TrustedClientRecord) error {
	tx, err := transactionSQL(transaction, "agent_update_policy")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_clients SET principal_ref=?,capabilities_json=?,product_scope_json=?,project_scope_json=?,agent_scope_json=?,policy_revision=policy_revision+1 WHERE client_ref=? AND status='active'`, policy.PrincipalRef, policy.CapabilitiesJSON, policy.ProductScopeJSON, policy.ProjectScopeJSON, policy.AgentScopeJSON, clientRef)
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
	return s.TransactDurable(ctx, func(transaction *Transaction) error {
		return mutateTrustedClientPolicyTx(ctx, transaction, clientRef, mutate)
	})
}

func mutateTrustedClientPolicyTx(ctx context.Context, transaction *Transaction, clientRef string, mutate func(TrustedClientRecord) (TrustedClientRecord, error)) error {
	tx, err := transactionSQL(transaction, "agent_mutate_policy")
	if err != nil {
		return err
	}
	var current TrustedClientRecord
	if err := tx.QueryRowContext(ctx, `SELECT client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,agent_scope_json,policy_revision FROM agent_clients WHERE client_ref=? AND status='active'`, clientRef).Scan(&current.ClientRef, &current.Status, &current.PrincipalRef, &current.CapabilitiesJSON, &current.ProductScopeJSON, &current.ProjectScopeJSON, &current.AgentScopeJSON, &current.PolicyRevision); err != nil {
		return wrapFailure(KindProjectionNotFound, "agent_mutate_policy", "trusted client not found or revoked", false, "reread the trusted client", err)
	}
	next, err := mutate(current)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_clients SET principal_ref=?,capabilities_json=?,product_scope_json=?,project_scope_json=?,agent_scope_json=?,policy_revision=policy_revision+1 WHERE client_ref=? AND status='active'`, next.PrincipalRef, next.CapabilitiesJSON, next.ProductScopeJSON, next.ProjectScopeJSON, next.AgentScopeJSON, clientRef)
	if err != nil {
		return wrapFailure(KindUnavailable, "agent_mutate_policy", "cannot update trusted client policy", true, "retry the policy update", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "agent_mutate_policy", "trusted client not found or revoked", false, "reread the trusted client")
	}
	return nil
}

// MutateTrustedClientPolicyTx is the transaction-scoped form of
// MutateTrustedClientPolicy. The agent grant-request route must apply the
// policy union in the same transaction that consumes the operator approval,
// so a committed policy with an unconsumed approval can never exist. Policy
// JSON stays opaque here: mutate interprets it, and a mutate error aborts the
// whole transaction with no write and no consumed approval.
func MutateTrustedClientPolicyTx(ctx context.Context, transaction *Transaction, clientRef string, mutate func(TrustedClientRecord) (TrustedClientRecord, error)) error {
	return mutateTrustedClientPolicyTx(ctx, transaction, clientRef, mutate)
}

func (s *Store) RotateTrustedClientKey(ctx context.Context, clientRef string, key TrustedClientKeyRecord, now string) error {
	return s.TransactDurable(ctx, func(transaction *Transaction) error {
		return rotateTrustedClientKeyTx(ctx, transaction, clientRef, key, now)
	})
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
	return s.TransactDurable(ctx, func(transaction *Transaction) error {
		return revokeTrustedClientTx(ctx, transaction, clientRef, now)
	})
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

// TrustedClient returns registered policy independently of key availability.
func (s *Store) TrustedClient(ctx context.Context, clientRef string) (TrustedClientRecord, error) {
	if s == nil || s.db == nil {
		return TrustedClientRecord{}, newFailure(KindUnavailable, "agent_client_read", "database is not open", true, "open the authority database")
	}
	client, _, err := readTrustedClient(ctx, s.db, clientRef)
	return client, err
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
	client, key, err := readTrustedClient(ctx, q, clientRef)
	if err != nil {
		return TrustedClientRecord{}, TrustedClientKeyRecord{}, err
	}
	if client.Status != "active" || key == nil {
		return TrustedClientRecord{}, TrustedClientKeyRecord{}, wrapFailure(KindProjectionNotFound, "agent_client_read", "unknown or keyless client", false, "register an active trusted client", sql.ErrNoRows)
	}
	return client, *key, nil
}

func readTrustedClient(ctx context.Context, q queryer, clientRef string) (TrustedClientRecord, *TrustedClientKeyRecord, error) {
	var client TrustedClientRecord
	var keyID, keyStatus sql.NullString
	var publicKey []byte
	if err := q.QueryRowContext(ctx, `SELECT c.client_ref,c.status,c.principal_ref,c.capabilities_json,c.product_scope_json,c.project_scope_json,c.agent_scope_json,c.policy_revision,k.key_id,k.public_key,k.status FROM agent_clients c LEFT JOIN agent_client_keys k ON k.client_ref=c.client_ref AND k.status='active' WHERE c.client_ref=?`, clientRef).Scan(&client.ClientRef, &client.Status, &client.PrincipalRef, &client.CapabilitiesJSON, &client.ProductScopeJSON, &client.ProjectScopeJSON, &client.AgentScopeJSON, &client.PolicyRevision, &keyID, &publicKey, &keyStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrustedClientRecord{}, nil, wrapFailure(KindProjectionNotFound, "agent_client_read", "unknown or keyless client", false, "register an active trusted client", err)
		}
		return TrustedClientRecord{}, nil, wrapFailure(KindUnavailable, "agent_client_read", "cannot read trusted client", true, "retry the trusted client read", err)
	}
	if !keyID.Valid {
		return client, nil, nil
	}
	return client, &TrustedClientKeyRecord{ClientRef: clientRef, KeyID: keyID.String, PublicKey: publicKey, Status: keyStatus.String}, nil
}

func ReadApprovalChallengeTx(ctx context.Context, transaction *Transaction, ref string, identity ChallengeIdentity) (ApprovalChallengeRecord, error) {
	var c ApprovalChallengeRecord
	tx, err := transactionSQL(transaction, "agent_challenge_read")
	if err != nil {
		return c, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,max_uses,used_count FROM agent_approval_challenges WHERE challenge_ref=? AND client_ref=? AND session_ref=? AND agent_ref=? AND worktree=?`, ref, identity.ClientRef, identity.SessionRef, identity.AgentRef, identity.Worktree).Scan(&c.ChallengeRef, &c.ClientRef, &c.PrincipalRef, &c.SessionRef, &c.AgentRef, &c.Directory, &c.Worktree, &c.ProductScopeJSON, &c.OperationDigest, &c.ScopeJSON, &c.VersionJSON, &c.Consequence, &c.HostAssertionDigest, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.MaxUses, &c.UsedCount); err != nil {
		return c, approvalReadFailure(err, "agent_challenge_read", "approval challenge not found", "reread the active approval challenge", "cannot read approval challenge")
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
		return c, approvalReadFailure(err, "agent_challenge_read", "approval challenge not found", "reread the approval challenge", "cannot read approval challenge")
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

// RearmApprovalChallengeTx moves one pending challenge's deadline inside the
// caller's transaction. Temporal order is decided on parsed RFC3339Nano
// values, never on stored TEXT order. Only an active, unused, single-use
// challenge whose persisted deadline still equals oldExpiresAt moves, and
// only when that deadline is expired (equality with now is expired) and
// newExpiresAt is strictly after now. The write changes nothing else and
// creates no consent; a guard mismatch or lost race leaves the row unchanged.
func RearmApprovalChallengeTx(ctx context.Context, transaction *Transaction, ref string, identity ChallengeIdentity, oldExpiresAt, newExpiresAt, now string) error {
	const op = "agent_challenge_rearm"
	oldDeadline, err := parseChallengeDeadline(op, oldExpiresAt)
	if err != nil {
		return err
	}
	newDeadline, err := parseChallengeDeadline(op, newExpiresAt)
	if err != nil {
		return err
	}
	atNow, err := parseChallengeDeadline(op, now)
	if err != nil {
		return err
	}
	if oldDeadline.After(atNow) {
		return newFailure(KindProjectionConflict, op, "approval challenge deadline is still live", false, "reread the approval challenge")
	}
	if !newDeadline.After(atNow) {
		return newFailure(KindProjectionConflict, op, "re-armed approval challenge deadline is not after now", false, "reread the approval challenge")
	}
	tx, err := transactionSQL(transaction, op)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_approval_challenges SET expires_at=? WHERE challenge_ref=? AND client_ref=? AND session_ref=? AND agent_ref=? AND worktree=? AND status='active' AND used_count=0 AND max_uses=1 AND expires_at=?`, newExpiresAt, ref, identity.ClientRef, identity.SessionRef, identity.AgentRef, identity.Worktree, oldExpiresAt)
	if err != nil {
		return wrapFailure(KindUnavailable, op, "cannot re-arm approval challenge", true, "retry the approval exchange", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionConflict, op, "approval challenge re-arm lost race", false, "reread the approval challenge")
	}
	return nil
}

func parseChallengeDeadline(op, value string) (time.Time, error) {
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, newFailure(KindInvalidOperation, op, "approval challenge deadline is not an RFC3339 timestamp", false, "supply canonical RFC3339 challenge deadlines")
	}
	return deadline, nil
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
		return a, approvalReadFailure(err, "agent_approval_read", "approval not found", "reread the approval", "cannot read approval")
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
		return a, approvalReadFailure(err, "agent_approval_authority", "consumed approval authority is unavailable", "reread the consumed approval", "cannot read approval authority")
	}
	challengeRef := ""
	if len(a.ProtectedEvidenceRef) > len("approval-challenge:") {
		challengeRef = a.ProtectedEvidenceRef[len("approval-challenge:"):]
	}
	if challengeRef == "" {
		return a, newFailure(KindProjectionNotFound, "agent_approval_authority", "approval challenge was not exactly consumed", false, "reread the approval challenge")
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&a.ChallengeStatus); err != nil {
		return a, approvalReadFailure(err, "agent_approval_authority", "approval challenge was not exactly consumed", "reread the approval challenge", "cannot read approval authority")
	}
	return a, nil
}

// approvalReadFailure classifies one approval-record read error: no row is
// the not-found refusal its caller names; any other error is a retryable
// store fault, never evidence that the record is absent.
func approvalReadFailure(err error, op, missing, missingRecovery, unreadable string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return newFailure(KindProjectionNotFound, op, missing, false, missingRecovery)
	}
	return wrapFailure(KindUnavailable, op, unreadable, true, "retry once the database is readable", err)
}
