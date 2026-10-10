import { test, expect } from "bun:test"
import { validateAgentLaneReport } from "./dispatch"

// Both sides of the lane-report boundary must enforce the same bound. The
// schema is the single place both sides read: the adapter bounds report
// details to the schema's x-maxBytes UTF-8 bytes before validation
// (dispatch.ts normalizeWorkerReport), while the store counts UTF-8 bytes
// (worker_lanes.go) and stays authoritative for anything unnormalized. An
// ASCII detail is 1 byte and 1 code unit per character, so the counts
// agree; byte-heavy non-ASCII prose splits them, and an unbounded report
// would consume the attempt at the store after the adapter accepted it.

const report = (detail: string) => ({
  schema_version: "1.0",
  readback_model: "opencode/glm/concord-2",
  status: "completed",
  evidence: [{ obligation: "commands", detail }],
})

// 257 e-acute characters: 257 UTF-16 units, 514 UTF-8 bytes.
const byteHeavy = "\u00e9".repeat(257)

test("the report schema bounds detail by UTF-8 bytes: byte-heavy detail is refused", () => {
  const failures: string[] = []
  const ok = validateAgentLaneReport(report(byteHeavy), failures)
  expect(ok).toBe(false)
  expect(failures.join(" ")).toContain("UTF-8 bytes")
})

test("the report schema admits an ASCII detail at the 512-character maximum", () => {
  const failures: string[] = []
  const ok = validateAgentLaneReport(report("a".repeat(512)), failures)
  expect(ok).toBe(true)
  expect(failures).toEqual([])
})

test("the report schema refuses an ASCII detail past the 512-character maximum", () => {
  const failures: string[] = []
  const ok = validateAgentLaneReport(report("a".repeat(513)), failures)
  expect(ok).toBe(false)
})
