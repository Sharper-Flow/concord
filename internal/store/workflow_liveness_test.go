package store

func livenessActor() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/liveness", AgentRef: "agent/owner", SessionRef: "session/liveness", ActorClass: ActorAgent}
}
