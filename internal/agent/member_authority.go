package agent

import (
	"encoding/json"

	"github.com/sharper-flow/concord/internal/store"
)

func memberOperationScope(raw []byte, env CallEnvelope, op ContractOperation) store.MemberOperationScope {
	var fields struct {
		WorkID    string `json:"work_id"`
		ProjectID string `json:"project_id"`
		ActionID  string `json:"action_id"`
		AttemptID string `json:"attempt_id"`
	}
	_ = json.Unmarshal(raw, &fields)
	if fields.ProjectID == "" {
		fields.ProjectID = env.AmbientProjectID
	}
	return store.MemberOperationScope{WorkID: fields.WorkID, ProjectID: fields.ProjectID, Operation: op.ID, ActionID: fields.ActionID, AttemptID: fields.AttemptID, Read: op.Kind == OperationRead}
}

func memberWorkflowActor(grant Authority) store.WorkflowActor {
	return store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
}
