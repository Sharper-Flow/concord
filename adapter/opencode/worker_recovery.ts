import { completeWorkerAttempt, readWorkerSessionBody, readExportSession, readExportOpeningPacket, resolveWorkerReportFromText, validateAgentLanePacket, type AgentResultEnvelope, type WorkerRecoveryContext } from "./dispatch"
import { canonicalDirectory, serializeLanePacket } from "./dispatch-window"
import { laneForIdentity } from "./generated-agent-lanes"
import { readTaskResult } from "./task-result"

const object = (value: unknown): value is Record<string, any> => value !== null && typeof value === "object" && !Array.isArray(value)

// All content comes from the original parent's completed host Task. Tool input
// selects a part; it supplies neither a packet, report, model nor provenance.
export async function reconcileRetainedWorker(
  recovery: WorkerRecoveryContext,
  workID: string,
  attemptID: string,
  taskPartID: string,
  options: Parameters<typeof completeWorkerAttempt>[3],
  signal: AbortSignal,
): Promise<AgentResultEnvelope> {
  const lane = laneForIdentity(recovery.dispatch.lane_id, recovery.dispatch.lane_version, recovery.dispatch.lane_digest)
  if (!lane) throw new Error("the original dispatched lane definition is unavailable")
  const reader = options.sessionReader
  if (!reader) throw new Error("the host session reader is unavailable")
  const parentRead = await readWorkerSessionBody(reader, recovery.coordinator_session, signal)
  if (!parentRead.ok) throw new Error(parentRead.message)
  const parent = JSON.parse(parentRead.body)
  if (!object(parent.info) || parent.info.id !== recovery.coordinator_session || !Array.isArray(parent.messages)) throw new Error("original coordinator host identity is missing")
  const parts: any[] = []
  for (const message of parent.messages) {
    if (!object(message) || !object(message.info) || message.info.sessionID !== recovery.coordinator_session || !Array.isArray(message.parts)) throw new Error("parent transcript has a mismatched message identity")
    for (const part of message.parts) if (object(part) && part.id === taskPartID) parts.push(part)
  }
  if (parts.length !== 1) throw new Error("the original host Task part is missing or ambiguous")
  const part = parts[0]
  if (part.type !== "tool" || part.tool !== "task" || part.sessionID !== recovery.coordinator_session || !object(part.state) || part.state.status !== "completed" || typeof part.state.output !== "string" || !object(part.state.input) || typeof part.state.input.prompt !== "string") throw new Error("the selected original host Task is not completed")
  const task = readTaskResult(part.state.output)
  if (!task || task.state !== "completed") throw new Error("retained host Task has no completed child result")
  let packet: unknown
  try { packet = JSON.parse(part.state.input.prompt) } catch { throw new Error("retained host Task has no original packet") }
  if (!validateAgentLanePacket(packet) || packet.work_id !== workID || packet.attempt_id !== attemptID || recovery.dispatch.attempt_id !== attemptID || packet.lane_id !== lane.id || packet.lane_version !== lane.version || packet.lane_digest !== recovery.dispatch.lane_digest || serializeLanePacket(packet) !== part.state.input.prompt || part.state.input.subagent_type !== `concord-${lane.id}`) throw new Error("retained host Task packet or executing lane differs from the original attempt")
  const childRead = await readWorkerSessionBody(reader, task.sessionID, signal)
  if (!childRead.ok) throw new Error(childRead.message)
  const child = JSON.parse(childRead.body)
  const originalDirectory = canonicalDirectory(recovery.worker_worktree)
  if (originalDirectory === null || !object(child.info) || child.info.parentID !== recovery.coordinator_session || canonicalDirectory(child.info.directory) !== originalDirectory) throw new Error("retained child session has a different parent or worktree")
  const opening = readExportOpeningPacket(childRead.body, task.sessionID, packet)
  const readback = readExportSession(childRead.body, task.sessionID)
  if (!opening.ok || !readback.ok || readback.metadata.readback_agent !== `concord-${lane.id}`) throw new Error("retained child opening or agent differs from its original dispatch")
  const report = resolveWorkerReportFromText(task.text, packet)
  if ("detail" in report || report.report.status !== "completed" || report.report.readback_model !== readback.metadata.readback_model) throw new Error("retained report is invalid or has a different executing model")
  // The existing completion path rechecks child opening, agent and model. Its
  // signed dispatch append also sends the packet to the Go canonical-digest
  // check before replay, so a Task with changed packet content writes nothing.
  return completeWorkerAttempt(lane, packet, part.state.output, { ...options, workerDirectory: recovery.worker_worktree, packetDigest: recovery.packet_digest, recovery }, signal)
}
