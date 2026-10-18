import { hostToolDescriptions, hostToolSchemas } from "./generated-contracts"
import { validateAgainstSchema } from "./dispatch"
import { type createWorkerCapabilityGuard, isRecord, toolAllowedByCapabilities, WorkerCapabilityError } from "./lane_capabilities"
import { renderBoundedBytePage, type WorkerReadingRequest } from "./worker_source"

export type WorkerDiffRequest = Omit<WorkerReadingRequest, "ordinal">
const TOOL = "concord_worker_diff"
const contract = hostToolSchemas.concord_worker_diff
type Guard = Pick<ReturnType<typeof createWorkerCapabilityGuard>, "authorize">
type Reader = (request: WorkerDiffRequest, signal: AbortSignal, directory: string) => Promise<unknown>

export function workerDiffTool(guard: Guard, read: Reader) {
  return {
    description: hostToolDescriptions.concord_worker_diff,
    args: contract.properties,
    async execute(args: unknown, context: {
      sessionID: string
      abort: AbortSignal
      ask: (request: { permission: string; patterns: string[]; always: string[]; metadata: Record<string, unknown> }) => Promise<void>
    }): Promise<string> {
      if (!validateAgainstSchema(contract, args)) throw new WorkerCapabilityError("review diff requires only offset and length from 1 to 4096 bytes")
      const selectors = args as { offset: number; length: number }
      const worker = await guard.authorize(context.sessionID, context.abort)
      if (!worker || !toolAllowedByCapabilities(worker.lane.capabilities, TOOL)) throw new WorkerCapabilityError("review diff requires an active authorized worker with inspect_diff capability")
      const subject = worker.dispatch.packet.inputs.work_context?.subject_commit
      if (typeof subject !== "string" || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(subject)) throw new WorkerCapabilityError("review diff requires the original held raw subject")
      await context.ask({ permission: TOOL, patterns: ["*"], always: ["*"], metadata: { ...selectors } })
      context.abort.throwIfAborted()
      const { dispatch } = worker
      const page = await read({ ...selectors, work_id: dispatch.packet.work_id, attempt_id: dispatch.packet.attempt_id, worktree_identity: dispatch.workerDirectoryIdentity, packet: dispatch.packet }, context.abort, dispatch.workerDirectory)
      const keys = ["base_commit", "subject_commit", "content_hash", "offset", "next_offset", "total_bytes", "eof", "data"]
      if (!isRecord(page) || Object.keys(page).length !== keys.length || !keys.every(key => Object.hasOwn(page, key)) ||
          typeof page.base_commit !== "string" || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(page.base_commit) ||
          page.subject_commit !== subject) {
        throw new WorkerCapabilityError("diff reader returned an invalid review identity")
      }
      return renderBoundedBytePage(page, selectors)
    },
  }
}

export async function publishWorkerDiffDefinition(input: { toolID: string }, output: { description: string; parameters: unknown; jsonSchema?: unknown }): Promise<void> {
  if (input.toolID !== TOOL) return
  output.description = hostToolDescriptions.concord_worker_diff
  output.jsonSchema = contract
}
