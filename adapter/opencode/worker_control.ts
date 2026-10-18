import { hostToolDescriptions, hostToolSchemas } from "./generated-contracts"
import { canonicalClientAssertion } from "./credentials"
import { validateAgainstSchema, type AgentLanePacket } from "./dispatch"
import { dispatchDirectoryIdentityMismatch } from "./dispatch-window"
import { type createWorkerCapabilityGuard, toolAllowedByCapabilities, WorkerCapabilityError } from "./lane_capabilities"

export const WORKER_CHECK_TOOL = "concord_worker_check"
const contract = hostToolSchemas.concord_worker_check
export const workerControlFields = ["client_ref", "agent_ref", "session_ref", "directory", "worktree", "manifest_digest", "work_id", "product_id", "project_id", "attempt_id", "packet_digest", "worktree_identity", "control_id", "lease_id", "request_id", "issued_at", "nonce"] as const

export function canonicalWorkerControl(fields: Record<string, unknown>): Uint8Array {
  return canonicalClientAssertion("worker-control-v1\0", workerControlFields, fields)
}

export type WorkerControlRequest = {
  work_id: string
  project_id: string
  attempt_id: string
  worktree_identity: string
  packet: AgentLanePacket
  control_id: string
}

export type WorkerCheckContext = {
  sessionID: string
  messageID: string
  agent: string
  directory: string
  abort: AbortSignal
  ask: (request: { permission: string; patterns: string[]; always: string[]; metadata: Record<string, unknown> }) => Promise<void>
}
type Executor = (request: WorkerControlRequest, context: WorkerCheckContext, packetDigest: string, directory: string) => Promise<unknown>
type Guard = Pick<ReturnType<typeof createWorkerCapabilityGuard>, "authorize">

export function workerCheckTool(guard: Guard, execute: Executor) {
  return {
    description: hostToolDescriptions.concord_worker_check,
    args: contract.properties,
    async execute(args: unknown, context: WorkerCheckContext): Promise<string> {
      if (!validateAgainstSchema(contract, args)) throw new WorkerCapabilityError("recorded checking requires only a bounded control_id")
      const { control_id } = args as { control_id: string }
      const worker = await guard.authorize(context.sessionID, context.abort)
      if (!worker || !toolAllowedByCapabilities(worker.lane.capabilities, WORKER_CHECK_TOOL)) {
        throw new WorkerCapabilityError("recorded checking requires an active authorized worker with execution capability")
      }
      const { dispatch } = worker
      const job = dispatch.packet.inputs.worker_job
      const control = job?.acceptance_oracle?.controls.find((control) => control.control_id === control_id)
      if (!control) {
        throw new WorkerCapabilityError("control_id has no recorded executable backing in this worker job; ordinary checks strings are not commands")
      }
      const projectID = control.recipe_source.project_id
      if (control.recipe_source.kind !== "repository_file" || typeof projectID !== "string" || !projectID) {
        throw new WorkerCapabilityError("recorded control has no repository Project backing")
      }
      if (!context.agent || !context.messageID || dispatchDirectoryIdentityMismatch(dispatch.workerDirectoryIdentity, context.directory)) {
        throw new WorkerCapabilityError("recorded checking requires the actual child agent and dispatched tool context")
      }
      await context.ask({ permission: WORKER_CHECK_TOOL, patterns: [control_id], always: ["*"], metadata: { control_id } })
      context.abort.throwIfAborted()
      return JSON.stringify(await execute({
        work_id: dispatch.packet.work_id, project_id: projectID,
        attempt_id: dispatch.packet.attempt_id, worktree_identity: dispatch.workerDirectoryIdentity,
        packet: dispatch.packet, control_id,
      }, context, dispatch.packetDigest, dispatch.workerDirectory))
    },
  }
}

export async function publishWorkerCheckDefinition(input: { toolID: string }, output: { description: string; parameters: unknown; jsonSchema?: unknown }): Promise<void> {
  if (input.toolID !== WORKER_CHECK_TOOL) return
  output.description = hostToolDescriptions.concord_worker_check
  output.jsonSchema = contract
}
