import { createHash } from "node:crypto"
import { validateGeneratedPayload } from "./generated-contract-tests"
import { maxEnvelopeBytes } from "./generated-contracts"

function isRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}
const projectionBytes = new TextEncoder()
const projectionText = new TextDecoder("utf-8", { fatal: true })
type MutationResultPayload = Record<string, any>
const completeMutationResults = new WeakMap<object, MutationResultPayload>()

// The reference stays on the wire. Only a verified original result becomes
// available to in-process authority consumers, including after envelope copies.
export function mutationResult(envelope: unknown): MutationResultPayload | undefined {
  if (!isRecord(envelope) || !isRecord(envelope.result)) return undefined
  return Object.hasOwn(envelope.result, "projection_reference") ? completeMutationResults.get(envelope.result) : envelope.result
}

export async function reconstructMutationResult(
  envelope: MutationResultPayload,
  source: { tool: string; operation: string; idempotencyKey: unknown },
  read: (input: Record<string, unknown>) => Promise<Record<string, unknown>>,
): Promise<void> {
  if (envelope.outcome !== "ok" || !isRecord(envelope.result) || !Object.hasOwn(envelope.result, "projection_reference")) return
  const fail = (message: string): never => { throw new Error(`mutation_result projection: ${message}; the mutation receipt remains recorded, but no complete authority was consumed`) }
  if (!validateGeneratedPayload("mutation_projection_reference_result", envelope.result)) fail("malformed reference")
  const reference = envelope.result.projection_reference as MutationResultPayload
  const pointer = reference.read_pointer.input
  if (reference.source_tool !== source.tool || reference.source_operation !== source.operation
    || pointer.source_tool !== source.tool || pointer.source_operation !== source.operation
    || pointer.idempotency_key !== source.idempotencyKey || pointer.digest !== reference.digest) fail("reference does not bind the original request")
  if (!Number.isSafeInteger(reference.total_bytes) || reference.total_bytes < 1) fail("invalid total_bytes")
  if (!Array.isArray(envelope.next_valid_intents) || envelope.next_valid_intents.length !== 0) fail("reference carries unverified intents")
  if (completeMutationResults.has(envelope.result)) return
  const fragments: Uint8Array[] = []
  let offset = 0
  let cursor: string | null = null
  const cursors = new Set<string>()
  for (;;) {
    const response = await read({ ...pointer, page: { cursor, limit: 1 } })
    if (Buffer.byteLength(JSON.stringify(response)) > maxEnvelopeBytes) fail("fragment envelope exceeds the byte cap")
    if (!isRecord(response) || response.outcome !== "ok" || response.tool !== "concord_work_trace" || response.operation !== "mutation_result") fail("read returned no successful mutation_result envelope")
    const page = response.result as MutationResultPayload
    if (!isRecord(page) || !validateGeneratedPayload("json_fragment_page", page)) fail("malformed fragment page")
    if (page.digest !== reference.digest) fail("digest changed")
    if (page.total_bytes !== reference.total_bytes) fail("total_bytes changed")
    if (!Number.isSafeInteger(page.offset) || page.offset !== offset) fail("offset is not contiguous")
    if (!fragmentIsWellFormed(page.data)) fail("fragment contains an unpaired surrogate")
    const bytes = projectionBytes.encode(page.data)
    if (page.fragment_bytes !== bytes.length || bytes.length < 1) fail("fragment_bytes does not match UTF-8 bytes")
    offset += bytes.length
    if (offset > reference.total_bytes || page.document_complete !== (offset === reference.total_bytes)) fail("completion does not match total_bytes")
    fragments.push(bytes)
    if (page.document_complete) {
      if (response.next_cursor !== null && response.next_cursor !== undefined) fail("complete mutation document carries a continuation")
      break
    }
    if (typeof response.next_cursor !== "string" || response.next_cursor.length === 0 || cursors.has(response.next_cursor)) fail("missing or repeated continuation cursor")
    cursor = response.next_cursor as string
    cursors.add(cursor)
  }
  const bytes = Buffer.concat(fragments)
  if (`sha256:${createHash("sha256").update(bytes).digest("hex")}` !== reference.digest) fail("document digest does not match")
  let payload: unknown
  try { payload = JSON.parse(projectionText.decode(bytes)) } catch { fail("document is not valid UTF-8 JSON") }
  if (!validateGeneratedPayload("mutation_result", payload) || !isRecord(payload)) fail("document fails the original inline result schema")
  const result = payload as MutationResultPayload
  const refKey = (ref: MutationResultPayload) => JSON.stringify([ref.entity_kind, ref.id, String(ref.version)])
  const refs = (values: unknown) => Array.isArray(values) && values.every(isRecord) ? values.map(refKey).sort() : null
  if (JSON.stringify(refs(result.changed_refs)) !== JSON.stringify(refs(envelope.changed_refs))) fail("changed_refs do not match the mutation receipt")
  const workRefs = result.changed_refs.filter((ref: MutationResultPayload) => ref.entity_kind === "work_item")
  const expected = reference.work_refs.map((ref: MutationResultPayload) => JSON.stringify([ref.id, ref.version])).sort()
  const actual = workRefs.map((ref: MutationResultPayload) => JSON.stringify([ref.id, Number(ref.version)])).sort()
  if (JSON.stringify(expected) !== JSON.stringify(actual)) fail("work_refs do not match changed work versions")
  const pins = Array.isArray(result.work_pins) ? result.work_pins : []
  for (const ref of reference.work_refs) {
    if (!pins.some((pin: MutationResultPayload) => pin.work_id === ref.id && pin.version === ref.version)) fail("document lacks a complete changed-version WorkPin")
  }
  completeMutationResults.set(envelope.result, result)
}

function fragmentIsWellFormed(text: string): boolean {
  for (const codePoint of text) {
    const unit = codePoint.codePointAt(0) ?? 0
    if (codePoint.length === 1 && unit >= 0xd800 && unit <= 0xdfff) return false
  }
  return true
}
