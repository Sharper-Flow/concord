package store

import (
	"context"
	"database/sql"
	"errors"
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
	// write that restores the exact prior content.
	PolicyRevision int64
}

type TrustedClientKeyRecord struct {
	ClientRef string
	KeyID     string
	PublicKey []byte
	Status    string
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
