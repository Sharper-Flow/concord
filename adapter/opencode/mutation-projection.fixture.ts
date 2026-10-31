import { createHash } from "node:crypto"

export function largeMutationPayload() {
  const work_pins = Array.from({ length: 16 }, (_, i) => ({
    work_id: `work-projection-${i}`, title: `Lossless result 😀 ${i}`, linear_issue_key: "",
    project_id: "project-test", project_display_name: "Fixture", version: 3,
    lifecycle: "in_progress", cancelled_instance_closes: 0,
    workflow_type: "workflow.implementation", step: "execution",
    driving_sessions: Array.from({ length: 16 }, (_, n) => ({
      session_ref: `session-${n}-`.padEnd(128, "x"), last_action_id: "action-".padEnd(128, "y"), last_acted_at: "2026-10-28T00:00:00Z",
    })),
    watermark: "seq:3", next_valid_intents: [],
    workflow_definition_digest: "sha256:" + "a".repeat(64), obligations: [],
  }))
  return {
    changed_refs: work_pins.map(pin => ({ entity_kind: "work_item", id: pin.work_id, version: pin.version })),
    next_valid_intents: [], work_pins,
  }
}

export function mutationProjectionFixture(payload: Record<string, any> = largeMutationPayload(), tool = "concord_work_relate", operation = "message_send", key = "projection-key") {
  const document = JSON.stringify(payload)
  const digest = `sha256:${createHash("sha256").update(document).digest("hex")}`
  const total_bytes = Buffer.byteLength(document)
  const pointer = { source_tool: tool, source_operation: operation, idempotency_key: key, digest }
  const reference = {
    encoding: "json-fragment", digest, total_bytes, source_tool: tool, source_operation: operation,
    work_refs: payload.changed_refs.filter((ref: any) => ref.entity_kind === "work_item").map((ref: any) => ({ id: ref.id, version: Number(ref.version) })),
    read_pointer: { tool: "concord_work_trace", operation: "mutation_result", input: pointer },
  }
  const envelope = {
    outcome: "ok", tool, operation, changed_refs: payload.changed_refs.map((ref: any) => ({ ...ref, version: String(ref.version) })), next_valid_intents: [],
    warnings: [{ kind: "mutation_result_projected" }], result: { projection_reference: reference },
  }
  const points = Array.from(document)
  const pages: Record<string, any>[] = []
  let offset = 0
  for (let i = 0; i < points.length; i += 4096) {
    const data = points.slice(i, i + 4096).join("")
    const fragment_bytes = Buffer.byteLength(data)
    const complete = offset + fragment_bytes === total_bytes
    pages.push({
      outcome: "ok", tool: "concord_work_trace", operation: "mutation_result", next_cursor: complete ? null : String(pages.length + 1),
      result: { encoding: "json-fragment", digest, total_bytes, offset, data, fragment_bytes, document_complete: complete },
    })
    offset += fragment_bytes
  }
  return { payload, document, pointer, envelope, pages, source: { tool, operation, idempotencyKey: key } }
}
