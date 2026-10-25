import { expect, test } from "bun:test"
import { DispatchWindows } from "./dispatch-window"

const packet = {
  schema_version: "1.0" as const,
  attempt_id: "attempt-current",
  lane_id: "implement",
  lane_digest: "sha256:" + "a".repeat(64),
  work_id: "work-current",
  step_id: "step-current",
  inputs: { task: "bounded work", context: "", constraints: [], binding: { objective_source: "contract_premise" as const, work_version: 1, contract_version: 1, assigned_result: "files_touched" } },
}
const selector = () => ({ subagent_type: "concord-implement", prompt: packet.attempt_id, description: "bounded work" })

for (const [name, variant] of Object.entries({
  "wrong lane": { subagent_type: "concord-review" },
  "stale reference": { prompt: "attempt-old" },
  "queued full packet": { prompt: JSON.stringify(packet) },
  "resume target": { task_id: "ses_prior" },
  "background request": { background: true },
  "false background variant": { background: false },
  "command variant": { command: "review" },
  "unknown execution field": { model: "provider/model" },
})) {
  test(`${name} leaves the valid native Task window and caller arguments usable`, async () => {
    const windows = new DispatchWindows()
    windows.open("parent", packet, "digest", process.cwd())
    const args = { ...selector(), ...variant }
    const before = { ...args }
    let reads = 0
    await expect(windows.bind("task", "parent", args, "invalid-call", async () => { reads++; return process.cwd() }, process.cwd())).rejects.toThrow()
    expect(reads).toBe(0)
    expect(args).toEqual(before)
    expect(windows.has("parent")).toBe(true)
    expect(windows.inFlightAttempt("parent")).toBeNull()

    const valid = selector()
    await windows.bind("task", "parent", valid, "valid-call", async () => process.cwd(), process.cwd())
    expect(JSON.parse(valid.prompt)).toEqual(packet)
    expect(windows.inFlight("parent", "valid-call")?.packet).toBe(packet)
    await expect(windows.bind("task", "parent", selector(), "duplicate-call", async () => process.cwd(), process.cwd())).rejects.toThrow(/no authorized dispatch/)
  })
}
