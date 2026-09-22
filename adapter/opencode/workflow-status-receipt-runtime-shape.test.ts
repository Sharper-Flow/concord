import { describe, expect, test } from "bun:test"
import { formatWorkClosureReceipt } from "./workflow-status"

// The closure receipt reads ONE fact from the lifecycle-completion response:
// the terminal pin the core returns. These fixtures reproduce the shape the
// core actually returns for concord_work_transition.lifecycle with target
// "completed", rather than a hand-built pairing, because the shipped banner
// was unreachable precisely where the pin was carried by a different
// response than the mutation outcome. The receipt bytes themselves are owned
// by the core's internal/receipt golden tests; this file pins the delegation
// contract against the runtime shape.
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

const RECEIPT_BYTES = "| 🛫 CON-392 Complete (work-cross) |\n| :-- |\n| ✅ Delegate passes the verb bytes |\n| ✓ check check:repo:verify · pass |"

function receiptRunner(calls: string[][], stdout: string) {
  return { async run(argv: string[], stdin: string) {
    calls.push([...argv, stdin])
    return { exitCode: 0, stdout, stderr: "" }
  } }
}

describe("workflow_status_receipt_runtime_shape", () => {
  test("delegates the completed pin to the core receipt verb and queues its bytes", async () => {
    const envelope = lifecycleCompletionEnvelope()
    const calls: string[][] = []
    const receipt = await formatWorkClosureReceipt(envelope.result.work_pins[0], { runner: receiptRunner(calls, RECEIPT_BYTES), binary: "concord-test" })
    expect(calls).toEqual([["concord-test", "receipt", JSON.stringify({ work_id: "work-cross" })]])
    expect(receipt).toBe(RECEIPT_BYTES)
  })

  test("a non-terminal pin never reaches the receipt verb", async () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.result.work_pins[0].lifecycle = "in_progress"
    const calls: string[][] = []
    const receipt = await formatWorkClosureReceipt(envelope.result.work_pins[0], { runner: receiptRunner(calls, RECEIPT_BYTES), binary: "concord-test" })
    expect(calls).toEqual([])
    expect(receipt).toBeNull()
  })

  test("the verb printing nothing leaves no notice: a non-completed closure is silent", async () => {
    const envelope = lifecycleCompletionEnvelope()
    envelope.result.work_pins[0].lifecycle = "cancelled"
    const calls: string[][] = []
    const receipt = await formatWorkClosureReceipt(envelope.result.work_pins[0], { runner: receiptRunner(calls, ""), binary: "concord-test" })
    expect(calls).toHaveLength(1)
    expect(receipt).toBeNull()
  })

  test("a failed receipt verb is a warning, never a thrown mutation failure", async () => {
    const envelope = lifecycleCompletionEnvelope()
    const warnings: string[] = []
    const runner = { async run() { return { exitCode: 1, stdout: "", stderr: "work item is not recorded" } } }
    const receipt = await formatWorkClosureReceipt(envelope.result.work_pins[0], { runner, binary: "concord-test", warnings })
    expect(receipt).toBeNull()
    expect(warnings).toEqual(["Concord could not render the work closure receipt: receipt exited 1."])
  })
})
