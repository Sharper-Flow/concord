import { hostToolDescriptions, hostToolSchemas } from "./generated-contracts"
import { validateAgainstSchema, type AgentLanePacket } from "./dispatch"
import { type createWorkerCapabilityGuard, isRecord as record, toolAllowedByCapabilities, WorkerCapabilityError } from "./lane_capabilities"

export const WORKER_READ_TOOL = "concord_worker_read"
const contract = hostToolSchemas.concord_worker_read

export type WorkerReadingRequest = {
  work_id: string
  attempt_id: string
  worktree_identity: string
  packet: AgentLanePacket
  ordinal: number
  offset: number
  length: number
}

type Reader = (request: WorkerReadingRequest, signal: AbortSignal, directory: string) => Promise<unknown>
type Guard = Pick<ReturnType<typeof createWorkerCapabilityGuard>, "authorize">
type Selectors = Pick<WorkerReadingRequest, "ordinal" | "offset" | "length">

function renderPage(value: unknown, selectors: Selectors): string {
  const keys = ["ordinal", "source_identity", "content_hash", "offset", "next_offset", "total_bytes", "eof", "data"]
  if (!record(value) || Object.keys(value).length !== keys.length || !keys.every((key) => Object.hasOwn(value, key)) ||
      value.ordinal !== selectors.ordinal ||
      typeof value.source_identity !== "string" || !value.source_identity) {
    throw new WorkerCapabilityError("pinned reader returned an invalid source identity")
  }
  return renderBoundedBytePage(value, selectors)
}

export function renderBoundedBytePage(value: Record<string, unknown>, selectors: { offset: number; length: number }): string {
  if (value.offset !== selectors.offset || typeof value.content_hash !== "string" || !/^sha256:[0-9a-f]{64}$/.test(value.content_hash) ||
      typeof value.data !== "string" || typeof value.eof !== "boolean" ||
      !Number.isSafeInteger(value.next_offset) || !Number.isSafeInteger(value.total_bytes)) {
    throw new WorkerCapabilityError("pinned reader returned an invalid page")
  }
  const bytes = Buffer.from(value.data, "base64")
  const next = value.next_offset as number
  const total = value.total_bytes as number
  if (bytes.toString("base64") !== value.data || bytes.length > selectors.length || !bytes.length && !value.eof ||
      next !== selectors.offset + bytes.length || total < next || value.eof !== (next === total)) {
    throw new WorkerCapabilityError("pinned reader returned inconsistent byte bounds")
  }
  // Byte pages can split UTF-8 characters. Keep the exact bytes in every
  // result and supply a text preview only for a complete UTF-8 page.
  let text: string | undefined
  try { text = new TextDecoder("utf-8", { fatal: true }).decode(bytes) } catch { /* Binary or partial UTF-8 page. */ }
  return JSON.stringify({ ...value, ...(text === undefined ? {} : { text }) })
}

export function workerReadTool(guard: Guard, read: Reader) {
  return {
    description: hostToolDescriptions.concord_worker_read,
    args: contract.properties,
    async execute(args: unknown, context: {
      sessionID: string
      abort: AbortSignal
      ask: (request: { permission: string; patterns: string[]; always: string[]; metadata: Record<string, unknown> }) => Promise<void>
    }): Promise<string> {
      if (!validateAgainstSchema(contract, args)) throw new WorkerCapabilityError("pinned reading requires only ordinal, offset, and a length from 1 to 4096 bytes")
      const selectors = args as Selectors
      const worker = await guard.authorize(context.sessionID, context.abort)
      if (!worker || !toolAllowedByCapabilities(worker.lane.capabilities, WORKER_READ_TOOL)) {
        throw new WorkerCapabilityError("pinned reading requires an active authorized worker with read_repository capability")
      }
      await context.ask({ permission: WORKER_READ_TOOL, patterns: ["*"], always: ["*"], metadata: { ...selectors } })
      context.abort.throwIfAborted()
      const { dispatch } = worker
      const page = await read({
        ...selectors,
        work_id: dispatch.packet.work_id,
        attempt_id: dispatch.packet.attempt_id,
        worktree_identity: dispatch.workerDirectoryIdentity,
        packet: dispatch.packet,
      }, context.abort, dispatch.workerDirectory)
      return renderPage(page, selectors)
    },
  }
}

export async function publishWorkerReadDefinition(input: { toolID: string }, output: { description: string; parameters: unknown; jsonSchema?: unknown }): Promise<void> {
  if (input.toolID !== WORKER_READ_TOOL) return
  output.description = hostToolDescriptions.concord_worker_read
  output.jsonSchema = contract
}
