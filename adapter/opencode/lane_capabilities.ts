import { agentLanes, laneForIdentity, laneToolRequirements, type AgentLane } from "./generated-agent-lanes"
import { dispatchDirectoryIdentityMismatch, dispatchWindows, type DispatchRecord, type DispatchWindows } from "./dispatch-window"
import { defaultRunner, readExportOpeningPacket, readWorkerSessionBody, type DispatchRunner } from "./dispatch"
import { repairingReview, reviewSubjectFailure } from "./worker_subject"
import { hostControlPlane, type HostControlPlane, type RouteResult, type SessionReader } from "./move-session"

export class WorkerCapabilityError extends Error {}

export const isLaneAgent = (agent: unknown): boolean => agentLanes.some(lane => agent === `concord-${lane.id}`)

export function toolAllowedByCapabilities(capabilities: readonly string[], toolID: string): boolean {
  const alternatives = Object.hasOwn(laneToolRequirements, toolID)
    ? (laneToolRequirements as Record<string, readonly (readonly string[])[]>)[toolID]
    : undefined
  return alternatives?.some((required) => required.every((capability) => capabilities.includes(capability))) ?? false
}

export type WorkerCapabilityContext = {
  dispatch: DispatchRecord
  parentSessionID: string
  sessionID: string
  lane: AgentLane
}

type Sessions = Pick<HostControlPlane, "sessionReader" | "taskScope" | "hasManagedParent">
type Options = { sessions?: Sessions; windows?: DispatchWindows; subjectRunner?: DispatchRunner }

export function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function checkedSessionInfo(data: unknown, ok: boolean, sessionID: string): Record<string, unknown> {
  if (!ok || !isRecord(data) || data.id !== sessionID) {
    throw new WorkerCapabilityError("cannot authenticate tool access: the host did not return the calling session")
  }
  return data
}

export function createWorkerCapabilityGuard(options: Options = {}) {
  const sessions = options.sessions ?? hostControlPlane()
  const windows = options.windows ?? dispatchWindows()
  // A weak key releases the child's binding with its settled dispatch packet.
  const children = new WeakMap<DispatchRecord, string>()
  const entered = new WeakSet<DispatchRecord>()
  // Membership can require authentication, never grant it. A settled child
  // stays fenced without making unrelated host work depend on Concord scope.
  const managedChildren = new Set<string>()
  // Parent identity is immutable; the selected agent is refreshed by chat.
  const roots = new Map<string, boolean>()
  const unresolved = new Set<string>()
  // Only immutable host provenance is cached, never tool grants or attempt state.
  const nativeChildren = new Map<string, string>()

  async function opening(sessionID: string, parentSessionID: string, result: RouteResult, reader: SessionReader, signal: AbortSignal): Promise<{ kind: "native" } | { kind: "lane"; agent: string; body: string }> {
    if (nativeChildren.get(sessionID) === parentSessionID) return { kind: "native" }
    const body = await readWorkerSessionBody({ ...reader, get: async () => result }, sessionID, signal)
    if (!body.ok) throw new WorkerCapabilityError(`cannot authenticate worker opening: ${body.message}`)
    const first = JSON.parse(body.body).messages[0]
    if (!isRecord(first?.info) || first.info.sessionID !== sessionID || first.info.role !== "user" || typeof first.info.agent !== "string" || !first.info.agent) {
      throw new WorkerCapabilityError("cannot authenticate the original host child agent")
    }
    if (isLaneAgent(first.info.agent)) return { kind: "lane", agent: first.info.agent, body: body.body }
    nativeChildren.set(sessionID, parentSessionID)
    return { kind: "native" }
  }

  async function isNativeChildOf(sessionID: string, parentSessionID: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<boolean> {
    const reader = sessions.sessionReader()
    if (!reader) throw new WorkerCapabilityError("cannot authenticate a resume target while the host session reader is unavailable")
    const result = await reader.get(sessionID, signal)
    const info = checkedSessionInfo(result.data, result.response.ok, sessionID)
    if (info.parentID !== parentSessionID) return false
    return (await opening(sessionID, parentSessionID, result, reader, signal)).kind === "native"
  }

  async function observe(sessionID: string, selectedAgent?: string): Promise<void> {
    const laneSelected = isLaneAgent(selectedAgent)
    if (roots.has(sessionID)) {
      if (selectedAgent !== undefined) roots.set(sessionID, laneSelected)
      return
    }
    const reader = sessions.sessionReader()
    if (!reader) return
    const signal = AbortSignal.timeout(5_000)
    try {
      const result = await reader.get(sessionID, signal)
      const info = checkedSessionInfo(result.data, result.response.ok, sessionID)
      if (info.parentID === undefined) {
        roots.set(sessionID, selectedAgent === undefined ? isLaneAgent(info.agent) : laneSelected)
      } else {
        if (typeof info.parentID !== "string" || !info.parentID) throw new WorkerCapabilityError("cannot authenticate tool access: the host parent identity is invalid")
        if (await sessions.hasManagedParent(sessionID, signal)) managedChildren.add(sessionID)
      }
      unresolved.delete(sessionID)
    } catch {
      // Chat can continue without authority. The next tool must authenticate
      // this caller even when the last local attempt has already retired.
      unresolved.add(sessionID)
    }
  }

  async function authorize(sessionID: string, signal: AbortSignal = AbortSignal.timeout(5_000)): Promise<WorkerCapabilityContext | null> {
    const reader = sessions.sessionReader()
    if (!reader) {
      if (windows.hasActiveAttempts() || managedChildren.has(sessionID) || unresolved.has(sessionID)) throw new WorkerCapabilityError("cannot authenticate a worker while the host session reader is unavailable")
      return null
    }
    const result = await reader.get(sessionID, signal)
    const info = checkedSessionInfo(result.data, result.response.ok, sessionID)
    if (info.parentID === undefined) {
      if (roots.get(sessionID) || isLaneAgent(info.agent)) {
        throw new WorkerCapabilityError("a Concord lane requires an active authorized attempt")
      }
      roots.set(sessionID, false)
      unresolved.delete(sessionID)
      return null
    }
    if (typeof info.parentID !== "string" || info.parentID.length === 0) {
      throw new WorkerCapabilityError("cannot authenticate tool access: the host parent identity is invalid")
    }
    const parentSessionID = info.parentID
    const record = windows.activeAttempt(parentSessionID)
    if (!record) {
      const scope = await sessions.taskScope(parentSessionID, signal)
      if (scope === null) throw new WorkerCapabilityError("cannot authenticate tool access: the host parent session is missing")
      if (scope === "unmanaged" && !managedChildren.has(sessionID)) return null
    }
    managedChildren.add(sessionID)
    if (!record || children.get(record) !== sessionID) {
      const original = await opening(sessionID, parentSessionID, result, reader, signal)
      if (original.kind === "native") {
        if (isLaneAgent(info.agent)) throw new WorkerCapabilityError("a Concord lane requires an active authorized attempt")
        if (await sessions.hasManagedParent(parentSessionID, signal)) throw new WorkerCapabilityError("an ordinary host child cannot nest under a managed parent")
        return null
      }
      if (!record || original.agent !== `concord-${record.packet.lane_id}` || !readExportOpeningPacket(original.body, sessionID, record.packet).ok) {
        throw new WorkerCapabilityError(record ? "worker session did not open with the authorized packet" : "worker session has no active authorized attempt")
      }
      const bound = children.get(record)
      if (bound !== undefined && bound !== sessionID) throw new WorkerCapabilityError("the authorized attempt already belongs to another host child")
      children.set(record, sessionID)
    }
    if (!record || windows.activeAttempt(parentSessionID) !== record) {
      throw new WorkerCapabilityError("worker session has no active authorized attempt")
    }
    const mismatch = dispatchDirectoryIdentityMismatch(record.workerDirectoryIdentity, info.directory)
    if (mismatch) throw new WorkerCapabilityError(mismatch)
    const registered = laneForIdentity(record.packet.lane_id, record.packet.lane_digest)
    if (!registered) throw new WorkerCapabilityError("the authorized packet requires lane content revalidation; preserve its original identity and evidence")
    // Review editing requires the exact current digest.
    const lane = (registered.capabilities as readonly string[]).includes("inspect_diff") && !repairingReview(registered, record.packet)
      ? { ...registered, capabilities: registered.capabilities.filter((c) => c !== "edit_scoped_files") } as unknown as AgentLane
      : registered
    if (!entered.has(record) || !repairingReview(lane, record.packet)) {
      const subjectFailure = await reviewSubjectFailure(lane, record.packet, record.workerDirectory, signal, options.subjectRunner ?? defaultRunner)
      if (subjectFailure) throw new WorkerCapabilityError(subjectFailure.detail)
    }
    if (windows.activeAttempt(parentSessionID) !== record) throw new WorkerCapabilityError("worker session has no active authorized attempt")
    entered.add(record)
    return { dispatch: record, parentSessionID, sessionID, lane }
  }

  async function before(input: { tool: string; sessionID: string }, output: { args: Record<string, unknown> }): Promise<void> {
    if (roots.has(input.sessionID)) {
      if (roots.get(input.sessionID)) throw new WorkerCapabilityError("a Concord lane requires an active authorized attempt")
      return
    }
    if (!windows.hasActiveAttempts() && !managedChildren.has(input.sessionID) && !unresolved.has(input.sessionID)) return
    const context = await authorize(input.sessionID)
    if (!context) return
    if (!toolAllowedByCapabilities(context.lane.capabilities, input.tool)) {
      throw new WorkerCapabilityError(`lane ${context.lane.id} does not grant tool ${input.tool}`)
    }
    if (input.tool === "concord_worker_check") {
      const subjectFailure = await reviewSubjectFailure(context.lane, context.dispatch.packet, context.dispatch.workerDirectory, AbortSignal.timeout(5_000), options.subjectRunner ?? defaultRunner, "terminal")
      if (subjectFailure) throw new WorkerCapabilityError(subjectFailure.detail)
    }
    if (input.tool.startsWith("lgrep_") && dispatchDirectoryIdentityMismatch(context.dispatch.workerDirectoryIdentity, output.args.path)) {
      throw new WorkerCapabilityError("local repository tools require the dispatched repository path")
    }
  }

  return { authorize, before, observe, isNativeChildOf }
}
