// Lane completion is the second adapter entry point CD-0102 D5 names. The host
// runs the worker between dispatch and completion, so the only place the
// adapter sees the finished worker is the `tool.execute.after` hook for the
// Task call the dispatch window bound. This module takes that hook's input,
// drains the in-flight attempt, and admits the result through
// completeWorkerAttempt, which exports the worker session, reads the executing
// model and agent, resolves the report, signs, and records the attempt.
//
// The hook contract offers no return channel and a thrown error fails the tool
// call in place of its result. A completion refusal is therefore appended to
// the tool output as a typed element the coordinator reads, and the hook never
// throws: the worker's own result stays visible either way.
import { completeWorkerAttempt, type AgentResultEnvelope, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { dispatchWindows, DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import { agentLanes } from "./generated-agent-lanes"

export interface LaneCompletionInput {
  tool: string
  sessionID: string
  callID: string
  args: unknown
}

export interface LaneCompletionOutput {
  title: string
  output: string
  metadata: unknown
}

export interface LaneCompletionDeps {
  windows?: DispatchWindows
  credentials?: CredentialStore
  runner?: DispatchRunner
  concordBinary?: string
  signal?: AbortSignal
}

// The element the coordinator reads after the host's task wrapper. It carries
// the completion envelope so a refusal names its kind and recovery action.
const ATTEMPT_ELEMENT = "concord_attempt"

function renderAttempt(envelope: AgentResultEnvelope): string {
  const summary: Record<string, unknown> = {
    outcome: envelope.outcome,
    lane: envelope.lane,
    readback_model: envelope.readback_model,
    session_id: envelope.session_id,
  }
  if (envelope.error) summary.error = envelope.error
  return `\n<${ATTEMPT_ELEMENT}>\n${JSON.stringify(summary)}\n</${ATTEMPT_ELEMENT}>`
}

export async function completeDispatchedWorker(input: LaneCompletionInput, output: LaneCompletionOutput, deps: LaneCompletionDeps = {}): Promise<void> {
  if (input.tool !== TASK_TOOL_ID) return
  const windows = deps.windows ?? dispatchWindows()
  const record = windows.takeInFlight(input.sessionID)
  if (!record) return
  // The packet pins the lane's version and digest, so completion binds to the
  // definition the dispatch authorized rather than to whatever the registry
  // carries now. A registry that drifted between dispatch and completion — an
  // upgrade mid-flight — would otherwise sign its own version and digest onto
  // the attempt, recording the worker as having run a contract it never
  // received. The attempt row is written from those same values, so nothing
  // downstream can catch the substitution.
  const lane = agentLanes.find((candidate) => candidate.id === record.packet.lane_id && candidate.version === record.packet.lane_version && candidate.digest === record.packet.lane_digest)
  if (!lane) {
    output.output += renderAttempt({ schema_version: "1.0", outcome: "error", lane: { id: record.packet.lane_id, version: record.packet.lane_version, digest: record.packet.lane_digest }, agent: `concord-${record.packet.lane_id}`, readback_model: null, session_id: null, error: { kind: "invalid_input", retry_safe: false, recovery_action: "contact_operator", message: "in-flight attempt names a lane at a version and digest the registry does not carry" } })
    return
  }
  const signal = deps.signal ?? new AbortController().signal
  let envelope: AgentResultEnvelope
  try {
    envelope = await completeWorkerAttempt(lane, record.packet, output.output, { credentials: deps.credentials, runner: deps.runner, concordBinary: deps.concordBinary, packetDigest: record.packetDigest }, signal)
  } catch (error) {
    envelope = { schema_version: "1.0", outcome: "error", lane: { id: lane.id, version: lane.version, digest: lane.digest }, agent: `concord-${lane.id}`, readback_model: null, session_id: null, error: { kind: "error", retry_safe: false, recovery_action: "reconcile_operation", message: String(error).slice(0, 2048) } }
  }
  output.output += renderAttempt(envelope)
}
