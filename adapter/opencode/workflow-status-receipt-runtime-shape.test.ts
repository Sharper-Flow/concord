import { describe, expect, test } from "bun:test"
import { formatWorkClosureBox } from "./workflow-status"

// The closure banner reads two facts from ONE response: the pin's terminal
// lifecycle and the envelope's evidence_refs. These fixtures reproduce the
// shape the core actually returns for concord_work_transition.lifecycle with
// target "completed", rather than a hand-built pairing, because the shipped
// banner was unreachable precisely where those two facts were carried by
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
          project_id: "project-1",
          project_display_name: "Concord",
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
  test("renders the closure box from the runtime lifecycle-completion envelope", () => {
    const envelope = lifecycleCompletionEnvelope()
    expect(formatWorkClosureBox(envelope.result.work_pins[0], envelope)).toBe(
      "```\n+=======================================================+\n| Concord Work Item Complete                            |\n+=======================================================+\n| work-cross | Concord                                  |\n| Emit an operator-facing closure receipt on completion |\n| lifecycle=completed | evidence=2                      |\n+=======================================================+\n```",
    )
  })

  test("an envelope without evidence renders evidence=none instead of going silent", () => {
    // This is the exact pre-repair runtime shape: lifecycle completed, and an
    // empty evidence_refs because the binding sat on a non-lifecycle action.
    // Evidence left the gate and moved into the rendering.
    const envelope = lifecycleCompletionEnvelope()
    envelope.evidence_refs = []
    expect(formatWorkClosureBox(envelope.result.work_pins[0], envelope)).toBe(
      "```\n+=======================================================+\n| Concord Work Item Complete                            |\n+=======================================================+\n| work-cross | Concord                                  |\n| Emit an operator-facing closure receipt on completion |\n| lifecycle=completed | evidence=none                   |\n+=======================================================+\n```",
    )
  })

  test("withholds the closure box for a non-terminal lifecycle", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.result.work_pins[0].lifecycle = "in_progress"
    expect(formatWorkClosureBox(envelope.result.work_pins[0], envelope)).toBeNull()
  })

  test("prefers the Linear issue key as the closure box identifier", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.result.work_pins[0].linear_issue_key = "SHA-188"
    expect(formatWorkClosureBox(envelope.result.work_pins[0], envelope)).toContain("\n| SHA-188 (work-cross) | Concord ")
  })

  test("an evidence ref without a locator renders evidence=none", () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.evidence_refs = [{ kind: "verification", authority: "agent-verifier", locator_kind: "test" } as never]
    expect(formatWorkClosureBox(envelope.result.work_pins[0], envelope as never)).toContain("lifecycle=completed | evidence=none")
  })
})
