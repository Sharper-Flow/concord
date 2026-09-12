import { describe, expect, test } from "bun:test"
import { formatWorkClosureReceipt, workStatePins } from "./workflow-status"

// The closure receipt reads two facts from ONE response: the pin's lifecycle
// and the envelope's evidence_refs. These fixtures reproduce the shape the
// core actually returns for concord_work_transition.lifecycle with
// target "completed", rather than a hand-built pairing, because the shipped
// receipt was unreachable precisely where those two facts were carried by
// different responses.
function lifecycleCompletionEnvelope() {
  return {
    schema_version: "1.0",
    origin: "core",
    tool: "concord_work_transition",
    operation: "lifecycle",
    outcome: "ok",
    authority: "authoritative",
    replayed: false,
    evidence_refs: [
      { kind: "verification", authority: "agent-verifier", locator_kind: "test", locator: "verification-pass" },
      { kind: "review", authority: "github", locator_kind: "pull_request", locator: "https://github.com/Sharper-Flow/concord/pull/1063" },
    ],
    result: {
      changed_refs: [{ entity_kind: "work_item", id: "work-cross", version: 66 }],
      work_pins: [
        {
          work_id: "work-cross",
          linear_issue_key: "",
          title: "Emit an operator-facing closure receipt on completion",
          version: 66,
          lifecycle: "completed",
          workflow_type: "workflow.break_fix",
          step: "acceptance",
          attempt: null,
          pending_operator_decision: null,
          watermark: "seq:13513",
          next_valid_intents: [],
        },
      ],
    },
  }
}

describe("workflow_status_receipt_runtime_shape", () => {
  test("renders the receipt from the runtime lifecycle-completion envelope", () => {
    const envelope = lifecycleCompletionEnvelope()
    const pins = workStatePins(envelope)
    expect(pins).toHaveLength(1)
    expect(pins[0].receipt).toBe(
      "◆ CONCORD WORK CLOSURE | work-cross | title=Emit an operator-facing closure receipt on completion | release=pending | evidence=verification-pass,https://github.com/Sharper-Flow/concord/pull/1063",
    )
  })

  test("still reports the state line alongside the receipt", () => {
    const pins = workStatePins(lifecycleCompletionEnvelope())
    expect(pins[0].line).toContain("lifecycle=completed")
    expect(pins[0].work_id).toBe("work-cross")
  })

  test("withholds the receipt when the envelope carries no evidence", () => {
    // This is the exact pre-repair runtime shape: lifecycle completed, and an
    // empty evidence_refs because the binding sat on a non-lifecycle action.
    const envelope = lifecycleCompletionEnvelope()
    envelope.evidence_refs = []
    const pins = workStatePins(envelope)
    expect(pins).toHaveLength(1)
    expect(pins[0].receipt).toBeUndefined()
  })

  test("withholds the receipt for a non-terminal lifecycle", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.result.work_pins[0].lifecycle = "in_progress"
    expect(workStatePins(envelope)[0].receipt).toBeUndefined()
  })

  test("prefers the Linear issue key as the receipt identifier", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.result.work_pins[0].linear_issue_key = "SHA-188"
    expect(workStatePins(envelope)[0].receipt).toContain("| SHA-188 |")
  })

  test("withholds the receipt when an evidence ref carries no locator", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.evidence_refs = [{ kind: "verification", authority: "agent-verifier", locator_kind: "test" } as never]
    expect(formatWorkClosureReceipt(envelope.result.work_pins[0], envelope as never)).toBeNull()
  })

  test("withholds the receipt on a refused envelope", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.outcome = "error"
    expect(workStatePins(envelope)).toHaveLength(0)
  })
})
