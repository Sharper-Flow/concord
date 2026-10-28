package agent

import (
	"context"
	"crypto/ed25519"
	"reflect"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// WorkerControlAssertion is signed by the host, never authored by the worker.
// AgentRef and SessionRef identify the actual child making the call. Native
// execution uses that authenticated identity, never its coordinator's actor.
type WorkerControlAssertion struct {
	ClientRef        string `json:"client_ref"`
	AgentRef         string `json:"agent_ref"`
	SessionRef       string `json:"session_ref"`
	Directory        string `json:"directory"`
	Worktree         string `json:"worktree"`
	ManifestDigest   string `json:"manifest_digest"`
	WorkID           string `json:"work_id"`
	ProductID        string `json:"product_id"`
	ProjectID        string `json:"project_id"`
	AttemptID        string `json:"attempt_id"`
	PacketDigest     string `json:"packet_digest"`
	WorktreeIdentity string `json:"worktree_identity"`
	ControlID        string `json:"control_id"`
	LeaseID          string `json:"lease_id"`
	RequestID        string `json:"request_id"`
	IssuedAt         string `json:"issued_at"`
	Nonce            string `json:"nonce"`
	Signature        []byte `json:"signature"`
}

// CanonicalWorkerControlAssertion uses the existing length-framed client
// assertion format with a distinct domain. An evidence signature cannot be
// reused as execution authority, even when its attempt and client match.
func CanonicalWorkerControlAssertion(a WorkerControlAssertion) []byte {
	names := []string{"client_ref", "agent_ref", "session_ref", "directory", "worktree", "manifest_digest", "work_id", "product_id", "project_id", "attempt_id", "packet_digest", "worktree_identity", "control_id", "lease_id", "request_id", "issued_at", "nonce"}
	values := []string{a.ClientRef, a.AgentRef, a.SessionRef, a.Directory, a.Worktree, a.ManifestDigest, a.WorkID, a.ProductID, a.ProjectID, a.AttemptID, a.PacketDigest, a.WorktreeIdentity, a.ControlID, a.LeaseID, a.RequestID, a.IssuedAt, a.Nonce}
	return canonicalClientAssertion("worker-control-v1\x00", names, values)
}

func (s *Service) verifyWorkerControlAssertionTx(ctx context.Context, tx *store.Transaction, host store.ResolvedProjectHost, a WorkerControlAssertion) (Authority, error) {
	if err := s.authorityReady("worker_control"); err != nil {
		return Authority{}, err
	}
	if tx == nil {
		return Authority{}, transactionInvalid("worker_control")
	}
	for _, value := range []string{a.ClientRef, a.AgentRef, a.SessionRef, a.WorkID, a.ProductID, a.ProjectID, a.AttemptID, a.ControlID, a.LeaseID, a.RequestID} {
		if !agentcontract.BoundedBytes(value, 1, 128) {
			return Authority{}, authorityRefusal("worker control assertion has an invalid identity")
		}
	}
	if !agentcontract.BoundedBytes(a.Directory, 1, 4096) || !agentcontract.BoundedBytes(a.Worktree, 1, 4096) || !validWorkerEvidenceObservationDigest(a.PacketDigest) || !validWorkerEvidenceObservationDigest(a.WorktreeIdentity) || !validWorkerNonce(a.Nonce) || len(a.Signature) != ed25519.SignatureSize {
		return Authority{}, authorityRefusal("worker control assertion is malformed")
	}
	issued, err := time.Parse(time.RFC3339Nano, a.IssuedAt)
	if err != nil || issued.Before(s.now().Add(-s.skew())) || issued.After(s.now().Add(s.skew())) {
		return Authority{}, authorityRefusal("worker control assertion timestamp invalid")
	}
	client, key, err := store.TrustedClientWithKeyTx(ctx, tx, a.ClientRef)
	if err != nil {
		return Authority{}, trustedClientRefusalFor(err, authorityRefusal("worker control assertion signature or client invalid"))
	}
	if client.Status != "active" || key.Status != "active" || key.KeyID == "" || !ed25519.Verify(ed25519.PublicKey(key.PublicKey), CanonicalWorkerControlAssertion(a), a.Signature) {
		return Authority{}, authorityRefusal("worker control assertion signature or client invalid")
	}
	// Reuse the execution policy owner and the actual presented agent. The
	// evidence-only capability never satisfies this execution check.
	return s.AuthorizeTx(ctx, tx, host, Invocation{ClientRef: a.ClientRef, AgentRef: a.AgentRef, SessionRef: a.SessionRef, Directory: a.Directory, Worktree: a.Worktree, ManifestDigest: a.ManifestDigest, RequiredCapability: "work_transition", RequiredOperation: "worktree_verify", ProductID: a.ProductID, ProjectID: a.ProjectID})
}

func (s *Service) resolveAuthorizedWorkerControlTx(ctx context.Context, tx *store.Transaction, host store.ResolvedProjectHost, req store.WorkerControlRequest, a WorkerControlAssertion) (store.WorkerControlPlan, Authority, error) {
	authority, err := s.verifyWorkerControlAssertionTx(ctx, tx, host, a)
	if err != nil {
		return store.WorkerControlPlan{}, Authority{}, err
	}
	if req.WorkID != a.WorkID || req.ProjectID != a.ProjectID || req.AttemptID != a.AttemptID || req.WorktreeIdentity != a.WorktreeIdentity || req.ControlID != a.ControlID {
		return store.WorkerControlPlan{}, Authority{}, authorityRefusal("worker control assertion does not bind this request")
	}
	scopes, err := store.ProductsForWorkIDsTx(ctx, tx, []string{req.WorkID})
	if err != nil {
		return store.WorkerControlPlan{}, Authority{}, err
	}
	if err := validateWorkReferenceScope(CallEnvelope{SelectedProductID: a.ProductID}, authority, agentcontract.OperationMutation, []string{req.WorkID}, scopes); err != nil {
		return store.WorkerControlPlan{}, Authority{}, err
	}
	plan, err := store.ResolveWorkerControlTx(ctx, tx, req)
	if err != nil {
		return store.WorkerControlPlan{}, Authority{}, err
	}
	if plan.Oracle.WorkerPacketDigest != a.PacketDigest {
		return store.WorkerControlPlan{}, Authority{}, authorityRefusal("worker control assertion does not bind the recorded packet")
	}
	if host.Host.WorktreePath == "" || host.Host.WorktreePath != plan.WorktreePath {
		return store.WorkerControlPlan{}, Authority{}, authorityRefusal("worker control caller is outside the dispatched worktree")
	}
	return plan, authority, nil
}

// ExecuteWorkerControl reaches only the existing native producer. The first
// authorization closes before Git planning. Admission repeats authorization
// and binding checks and consumes the nonce in the acquiring lease transaction.
// A retained replay authenticates with a fresh assertion nonce before returning
// its producer result; the retained lease and request identifiers stay unchanged.
func (s *Service) ExecuteWorkerControl(ctx context.Context, req store.WorkerControlRequest, a WorkerControlAssertion) (store.WorktreeVerifyResult, error) {
	if err := s.authorityReady("worker_control"); err != nil {
		return store.WorktreeVerifyResult{}, err
	}
	operation, ok := agentcontract.ValidateContractOperation("concord_work_transition", "worktree_verify")
	if !ok || operation.SupportedBudgetSeconds < 1 {
		return store.WorktreeVerifyResult{}, authorityRefusal("native execution operation or budget is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(operation.SupportedBudgetSeconds)*time.Second)
	defer cancel()
	host, err := s.ProbeProjectHost(ctx, a.Directory, a.Worktree)
	if err != nil {
		return store.WorktreeVerifyResult{}, err
	}
	var plan store.WorkerControlPlan
	var authority Authority
	if err := s.Store.Transact(ctx, func(tx *store.Transaction) error {
		var err error
		plan, authority, err = s.resolveAuthorizedWorkerControlTx(ctx, tx, host, req, a)
		return err
	}); err != nil {
		return store.WorktreeVerifyResult{}, err
	}
	return s.Store.VerifyWorktree(ctx, store.WorktreeVerifyRequest{
		WorkID: req.WorkID, ProjectID: req.ProjectID, Oracle: &plan.Oracle,
		LeaseID: a.LeaseID, RequestID: a.RequestID, PrincipalRef: authority.PrincipalRef,
		SupportedBudgetSeconds: operation.SupportedBudgetSeconds, CallerBudgetApplied: true,
		Owner: store.SessionWorktreeOwner{ClientRef: authority.ClientRef, AgentRef: authority.AgentRef, SessionRef: authority.SessionRef},
		NativeAuthorization: func(ctx context.Context, tx *store.Transaction) error {
			current, currentAuthority, err := s.resolveAuthorizedWorkerControlTx(ctx, tx, host, req, a)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(current, plan) || currentAuthority.PrincipalRef != authority.PrincipalRef || currentAuthority.ClientRef != authority.ClientRef || currentAuthority.AgentRef != authority.AgentRef || currentAuthority.SessionRef != authority.SessionRef {
				return authorityRefusal("worker control binding changed before native admission")
			}
			now := s.now()
			return store.PruneAndRecordNonceTx(ctx, tx, a.ClientRef, a.Nonce, now.Format(time.RFC3339Nano), now.Add(s.skew()).Format(time.RFC3339Nano), now.Add(-s.skew()).Format(time.RFC3339Nano))
		},
	})
}
