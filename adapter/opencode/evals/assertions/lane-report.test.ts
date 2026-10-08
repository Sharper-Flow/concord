import { test, expect } from "bun:test"
import assertLaneReport from "./lane-report.js"

// Unit coverage for the lane behavioural evals' shared assertion. The
// assertion is the behavioural gate for eval runs (CD-0017 D7) and mirrors
// the adapter's admission boundary (admitWorkerReport in dispatch.ts), so
// its refusals are pinned here without calling a model: every report
// contract constraint carries a negative test, the per-lane obligation
// coverage and severity discharge rules, the per-lane required review
// block, lane identity resolution, and the seeded-defect marker surface.

type Result = { pass: boolean; score: number; reason: string }

const run = (report: Record<string, unknown>, packet: Record<string, unknown> = reviewPacket()): Result =>
  assertLaneReport(stream(report), { prompt: JSON.stringify(packet) })

// One host run event per stdout line, text at part.text — the shape the
// assertion parses out of `opencode run --format json` output.
const stream = (report: Record<string, unknown>) =>
  `${JSON.stringify({ type: "text", part: { type: "text", text: JSON.stringify(report) } })}\n`

const REVIEW_DIGEST = "sha256:3a4cd631ddffb655e6a432bb186af0dfe70113031a79c4d45487f53412f2f15d"
const IMPLEMENT_DIGEST = "sha256:f4dad03f0b94430af796eecc1c53740d6441286c78879b46bee17ec79ce604e5"

const reviewPacket = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  schema_version: "1.0",
  attempt_id: "attempt:eval-review-bounded-review",
  lane_id: "review",
  lane_version: 1,
  lane_digest: REVIEW_DIGEST,
  work_id: "work-eval",
  step_id: "review",
  inputs: { task: "review the bounded change against its contract" },
  ...overrides,
})

const finding = (overrides: Record<string, unknown> = {}) => ({
  severity: "P1",
  confidence: "high",
  detail: "the bound is not checked at the boundary",
  ...overrides,
})

const reviewBlock = (overrides: Record<string, unknown> = {}) => ({
  verdict: "no_ship",
  findings: [finding()],
  ...overrides,
})

const report = (overrides: Record<string, unknown> = {}): Record<string, unknown> => ({
  schema_version: "1.0",
  readback_model: "opencode/glm/concord-2",
  status: "completed",
  evidence: [
    { obligation: "contract_findings", detail: "the boundary does not check the bound" },
    { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
  ],
  review: reviewBlock(),
  ...overrides,
})

// A report whose evidence discharges every declared review-lane obligation
// by name, so the per-lane required-block refusal is the one that fires.
const blockless = (): Record<string, unknown> =>
  report({
    review: undefined,
    evidence: [
      { obligation: "contract_findings", detail: "the boundary does not check the bound" },
      { obligation: "severity", detail: "one finding carries P1 severity with high confidence" },
      { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
    ],
  })

test("a completed review-lane report with a valid typed review block passes", () => {
  expect(run(report())).toMatchObject({ pass: true, score: 1 })
})

test("a report missing schema_version is admitted under a packet and refused without one", () => {
  const { schema_version: _dropped, ...rest } = report()
  // The packet names the report identity, so admission composes it and the
  // worker's omission carries no weight — the same admission the adapter
  // applies to an undefined schema_version echo.
  expect(run(rest)).toMatchObject({ pass: true, score: 1 })
  // A packet that names no schema_version is a synthetic context: the
  // report's own surface must carry it, and its absence is refused.
  const result = run(rest, {})
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("missing required propert")
  expect(result.reason).toContain("schema_version")
})

test("a report status outside the declared lifecycle is refused", () => {
  const result = run(report({ status: "done" }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("status")
  expect(result.reason).toContain("closed enum")
})

test("a report whose readback_model is not a provider/model identifier is refused", () => {
  const result = run(report({ readback_model: "a model, honestly" }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("readback_model")
})

test("report evidence below and above the declared bounds is refused", () => {
  for (const evidence of [[], Array.from({ length: 65 }, () => ({ obligation: "severity", detail: "d" }))]) {
    const result = run(report({ evidence }))
    expect(result.pass).toBe(false)
    expect(result.reason).toContain("evidence")
  }
})

test("an evidence entry missing a required property is refused", () => {
  const result = run(report({ evidence: [{ obligation: "severity" }] }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("missing required propert")
})

test("an evidence entry carrying an undeclared field is refused", () => {
  const evidence = [
    { obligation: "contract_findings", detail: "the boundary does not check the bound", mood: "confident" },
  ]
  const result = run(report({ evidence }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("undeclared propert")
  expect(result.reason).toContain("mood")
})

test("an evidence obligation outside the closed vocabulary is refused", () => {
  const result = run(report({
    evidence: [{ obligation: "mood", detail: "the review feels fine" }],
  }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("obligation")
  expect(result.reason).toContain("closed enum")
})

test("an empty evidence detail is refused", () => {
  const result = run(report({
    evidence: [{ obligation: "severity", detail: "" }],
  }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("shorter than 1")
})

test("a byte-heavy evidence detail is truncated to the UTF-8 byte bound, not refused", () => {
  // 257 two-byte code points: 257 characters, 514 bytes.
  const [first, ...rest] = report().evidence as Array<{ obligation: string; detail: string }>
  const result = run(report({
    evidence: [{ ...first, detail: "é".repeat(257) }, ...rest],
  }))
  expect(result.pass, result.reason).toBe(true)
})

test("an over-long evidence detail is truncated exactly as admission truncates it", () => {
  const evidence = [
    { obligation: "contract_findings", detail: "x".repeat(600) },
    { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
  ]
  expect(run(report({ evidence }))).toMatchObject({ pass: true, score: 1 })
})

test("an over-long review finding detail is truncated to the UTF-8 byte bound, not refused", () => {
  const result = run(report({
    review: reviewBlock({ findings: [finding({ detail: "x".repeat(900) })] }),
  }))
  expect(result.pass, result.reason).toBe(true)
})

test("malformed predicate_ids are refused", () => {
  const refusals: Array<{ name: string; predicate_ids: unknown }> = [
    { name: "not an array", predicate_ids: "predicate:adapter-review-admission" },
    { name: "null", predicate_ids: null },
    { name: "past the item bound", predicate_ids: Array.from({ length: 9 }, () => "predicate:adapter-review-admission") },
    { name: "id outside the declared form", predicate_ids: ["nope"] },
    { name: "id without a suffix", predicate_ids: ["predicate:"] },
    { name: "id with an invalid character", predicate_ids: ["predicate:bad/id"] },
    { name: "past the id bound", predicate_ids: ["predicate:" + "x".repeat(119)] },
  ]
  for (const refusal of refusals) {
    const evidence = [
      { obligation: "severity", detail: "the finding names its predicate", predicate_ids: refusal.predicate_ids },
    ]
    const result = run(report({ evidence }))
    expect(result.pass, refusal.name).toBe(false)
    expect(result.reason, refusal.name).toContain("predicate_ids")
  }
})

test("a report carrying an undeclared top-level field is refused", () => {
  const result = run(report({ mood: "confident" }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("undeclared propert")
  expect(result.reason).toContain("mood")
})

test("a report echoing dispatch-owned identity is admitted with identity from the packet", () => {
  const echoed = report({
    attempt_id: "attempt:forged-elsewhere",
    lane_id: "verify",
    lane_version: 9,
    lane_digest: "sha256:" + "0".repeat(64),
  })
  expect(run(echoed)).toMatchObject({ pass: true, score: 1 })
})

// The packet's worker-job revision (CD-0205): the report binding projects
// job_id, revision, and digest from inputs.worker_job the way the adapter's
// packetWorkerJobBinding does, and the packet's schema_version governs the
// report identity the way admission stamps it.
const JOB_BINDING = { job_id: "job:one", revision: 2, digest: `sha256:${"a".repeat(64)}` }

const jobPacket = (overrides: Record<string, unknown> = {}): Record<string, unknown> =>
  reviewPacket({
    schema_version: "1.1",
    inputs: { task: "review the bounded change against its contract", worker_job: { ...JOB_BINDING } },
    ...overrides,
  })

test("report admission derives schema identity from the packet, not the worker", () => {
  for (const schema_version of ["1.0", "1.1"] as const) {
    for (const echo of ["1.0", "1.1", "2.0", null, 11, undefined]) {
      const result = run(report({ schema_version: echo }), reviewPacket({ schema_version }))
      expect(result.pass, `packet ${schema_version}, worker echo ${JSON.stringify(echo) ?? "dropped"}`).toBe(true)
    }
  }
})

test("report admission derives the worker-job binding from the packet, not the worker", () => {
  const echoes = [
    ["matching", { ...JOB_BINDING }],
    ["missing", undefined],
    ["wrong job", { ...JOB_BINDING, job_id: "job:other" }],
    ["stale revision", { ...JOB_BINDING, revision: 1 }],
    ["wrong digest", { ...JOB_BINDING, digest: `sha256:${"b".repeat(64)}` }],
    ["malformed", { revision: "not-a-revision", extra: true }],
    ["null", null],
    ["non-object", "job:one"],
  ] as const
  for (const [label, worker_job] of echoes) {
    for (const status of ["completed", "failed"] as const) {
      const result = run(report({ schema_version: "1.0", worker_job, status }), jobPacket())
      expect(result.pass, `${label} echo, ${status} report`).toBe(true)
    }
  }
})

test("an unbound packet stays unbound whatever the worker echoes", () => {
  for (const schema_version of ["1.0", "1.1"] as const) {
    for (const worker_job of [JOB_BINDING, undefined] as const) {
      const result = run(report({ worker_job }), reviewPacket({ schema_version }))
      expect(result.pass, `packet ${schema_version}, echo ${worker_job === undefined ? "absent" : "present"}`).toBe(true)
    }
  }
})

test("a packet whose worker-job binding the closed report schema refuses is refused", () => {
  const refusals: Array<{ name: string; packet: Record<string, unknown> }> = [
    {
      name: "a 1.0 packet never carries a job",
      packet: jobPacket({ schema_version: "1.0" }),
    },
    {
      name: "an invalid recorded digest",
      packet: jobPacket({ inputs: { task: "review the bounded change against its contract", worker_job: { ...JOB_BINDING, digest: "invalid" } } }),
    },
  ]
  for (const refusal of refusals) {
    const result = run(report(), refusal.packet)
    expect(result.pass, refusal.name).toBe(false)
    expect(result.reason, refusal.name).toContain("closed agent-lane-report.v1 schema")
  }
})

test("a packet that names no schema version keeps the report's own enum-bound version", () => {
  expect(run(report(), {})).toMatchObject({ pass: true, score: 1 })
  const refused = run(report({ schema_version: "2.0" }), {})
  expect(refused.pass).toBe(false)
  expect(refused.reason).toContain("schema_version")
  expect(refused.reason).toContain('expected one of ["1.0","1.1"]')
})

test("a malformed packet schema identity never falls back to the worker echo", () => {
  for (const schema_version of [null, 11, "invalid"]) {
    const result = run(report(), reviewPacket({ schema_version }))
    expect(result.pass).toBe(false)
    expect(result.reason).toContain("schema_version")
  }
})

test("a prompt that is not a packet document falls back to the report surface", () => {
  const result = assertLaneReport(stream(report()), { prompt: "review the bounded change" })
  expect(result.pass, result.reason).toBe(true)
})

test("a base_comparison outside the declared shape is refused", () => {
  const refusals: Array<{ name: string; base_comparison: Record<string, unknown> }> = [
    { name: "result outside the closed set", base_comparison: { checks: [{ command: "go test ./...", branch_result: "skipped", base_result: "pass" }] } },
    { name: "command past the byte bound", base_comparison: { checks: [{ command: "é".repeat(257), branch_result: "pass", base_result: "pass" }] } },
    { name: "undeclared property", base_comparison: { checks: [], mood: "ok" } },
  ]
  for (const refusal of refusals) {
    const result = run(report({ base_comparison: refusal.base_comparison }))
    expect(result.pass, refusal.name).toBe(false)
  }
})

test("a report inside one Markdown fence is unwrapped", () => {
  const fenced = "```json\n" + JSON.stringify(report()) + "\n```"
  const output = `${JSON.stringify({ type: "text", part: { type: "text", text: fenced } })}\n`
  expect(assertLaneReport(output, { prompt: JSON.stringify(reviewPacket()) })).toMatchObject({ pass: true, score: 1 })
})

test("the last JSON object in the stream is the report", () => {
  const output = [
    { type: "text", part: { type: "text", text: "working prose, no report here" } },
    { type: "text", part: { type: "text", text: JSON.stringify({ status: "done" }) } },
    { type: "text", part: { type: "text", text: JSON.stringify(report()) } },
  ].map((event) => JSON.stringify(event)).join("\n") + "\n"
  expect(assertLaneReport(output, { prompt: JSON.stringify(reviewPacket()) })).toMatchObject({ pass: true, score: 1 })
})

test("output carrying no report document is refused", () => {
  const output = `${JSON.stringify({ type: "text", part: { type: "text", text: "the review passed, trust me" } })}\n`
  const result = assertLaneReport(output, { prompt: JSON.stringify(reviewPacket()) })
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("no agent-lane-report.v1 document")
})

test("a run stream that delegates through a task tool-use event is refused", () => {
  const output = [
    JSON.stringify({ type: "tool_use", part: { type: "tool_use", tool: "task", input: {} } }),
    JSON.stringify({ type: "text", part: { type: "text", text: JSON.stringify(report()) } }),
  ].join("\n") + "\n"
  const result = assertLaneReport(output, { prompt: JSON.stringify(reviewPacket()) })
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("delegated through a task tool-use event")
})

test("a packet naming an unregistered lane identity or digest is refused", () => {
  const result = run(report(), reviewPacket({ lane_digest: "sha256:" + "0".repeat(64) }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("unregistered lane identity or digest")
})

test("a completed review-lane report without the typed review block is refused", () => {
  const result = run(blockless())
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("typed review block the review lane requires")
})

test("a review-lane completed report carrying an undeclared lane obligation is refused", () => {
  const result = run(report({
    evidence: [...(report().evidence as unknown[]), { obligation: "files_touched", detail: "not a review obligation" }],
  }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("the review lane does not declare: files_touched")
})

test("a completed review-lane report leaving its assigned result undischarged is refused", () => {
  const result = run(report({
    review: undefined,
    evidence: [
      { obligation: "severity", detail: "one finding carries P1 severity with high confidence" },
      { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
    ],
  }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("completes no assigned result")
  expect(result.reason).toContain("contract_findings")
})

test("a completed review-lane report leaving a non-assigned obligation undischarged is refused", () => {
  const result = run(report({
    review: undefined,
    evidence: [
      { obligation: "contract_findings", detail: "the boundary does not check the bound" },
      { obligation: "severity", detail: "one finding carries P1 severity with high confidence" },
    ],
  }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("undischarged: verification_commands")
})

test("a free-text severity entry beside the typed review block is refused", () => {
  const result = run(report({
    evidence: [
      ...((report().evidence as unknown[]).slice(0, 1)),
      { obligation: "severity", detail: "one finding carries P1 severity with high confidence" },
      { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
    ],
  }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("free-text severity entry beside the typed review block")
})

test("a failed review-lane report may omit the typed review block", () => {
  const failed = { ...report(), status: "failed", review: undefined }
  expect(run(failed)).toMatchObject({ pass: true, score: 1 })
})

test("a lane whose manifest declares no review block needs none", () => {
  const implement = {
    schema_version: "1.0",
    attempt_id: "attempt:eval-implement-bounded-task",
    lane_id: "implement",
    lane_version: 1,
    lane_digest: IMPLEMENT_DIGEST,
    work_id: "work-eval",
    step_id: "implement",
    inputs: { task: "implement the bounded task" },
  }
  const implementReport = report({
    review: undefined,
    evidence: [
      { obligation: "files_touched", detail: "the eval assertion and its tests" },
      { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
      { obligation: "unresolved_issues", detail: "none" },
    ],
  })
  expect(run(implementReport, implement)).toMatchObject({ pass: true, score: 1 })
})

test("a review block outside the declared shape is refused", () => {
  const refusals: Array<{ name: string; block: unknown }> = [
    { name: "not an object", block: "ship" },
    { name: "undeclared review property", block: { ...reviewBlock(), summary: "looks fine" } },
    { name: "missing verdict", block: { findings: [finding()] } },
    { name: "missing findings", block: { verdict: "ship" } },
    { name: "verdict outside the closed set", block: { verdict: "conditional", findings: [finding()] } },
    { name: "findings not an array", block: { verdict: "ship", findings: "none" } },
    { name: "findings past the item bound", block: { verdict: "ship", findings: Array.from({ length: 65 }, () => finding()) } },
    { name: "finding with an undeclared property", block: reviewBlock({ findings: [{ ...finding(), mood: "sure" }] }) },
    { name: "finding missing a required property", block: reviewBlock({ findings: [{ severity: "P1", detail: "x" }] }) },
    { name: "severity outside the closed scale", block: reviewBlock({ findings: [finding({ severity: "S1" })] }) },
    { name: "confidence outside the closed scale", block: reviewBlock({ findings: [finding({ confidence: "certain" })] }) },
    { name: "empty finding detail", block: reviewBlock({ findings: [finding({ detail: "" })] }) },
  ]
  for (const refusal of refusals) {
    const result = run(report({ review: refusal.block }))
    expect(result.pass, refusal.name).toBe(false)
  }
})

test("a finding detail inside the UTF-8 byte bound passes at its character length", () => {
  const ship = { verdict: "ship", findings: [finding({ detail: "é".repeat(255) })] }
  expect(run(report({ review: ship }))).toMatchObject({ pass: true, score: 1 })
})

test("a ship verdict with a finding severity the verdict excludes is refused", () => {
  const result = run(report({ review: { verdict: "ship", findings: [finding({ severity: "P0" })] } }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("severity")
  expect(result.reason).toContain("closed enum")
  expect(result.reason).toContain("P1")
})

test("a ship verdict with only non-blocking severities passes", () => {
  const ship = {
    verdict: "ship",
    findings: [finding({ severity: "P1" }), finding({ severity: "P2" }), finding({ severity: "P3" })],
  }
  expect(run(report({ review: ship }))).toMatchObject({ pass: true, score: 1 })
})

test("a ship verdict with zero findings passes", () => {
  expect(run(report({ review: { verdict: "ship", findings: [] } }))).toMatchObject({ pass: true, score: 1 })
})

test("a no_ship verdict with zero findings is refused", () => {
  const result = run(report({ review: { verdict: "no_ship", findings: [] } }))
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("fewer than 1")
})

test("an evidence entry may carry the schema's declared optional predicate_ids", () => {
  const evidence = [
    { obligation: "contract_findings", detail: "the boundary does not check the bound", predicate_ids: ["predicate:adapter-review-admission"] },
    { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
  ]
  expect(run(report({ evidence }))).toMatchObject({ pass: true, score: 1 })
})

for (const shape of ["absent", "empty", "eight"] as const) {
  test(`a completed report with ${shape} predicate_ids passes eval admission`, () => {
    const predicate_ids = shape === "eight"
      ? Array.from({ length: 8 }, (_, index) => `predicate:p${index}`)
      : []
    const evidence = (report().evidence as Array<{ obligation: string; detail: string }>)
      .map((entry) => shape === "absent" ? entry : { ...entry, predicate_ids })
    expect(run(report({ evidence }))).toMatchObject({ pass: true, score: 1 })
  })
}

test("a seeded-defect marker is discharged through the typed review findings", () => {
  const packet = reviewPacket({ attempt_id: "attempt:eval-review-seeded-scope-violation" })
  const scoped = report({ review: reviewBlock({ findings: [finding({ detail: "scripts/check-json.py allows the drift" })] }) })
  expect(run(scoped, packet)).toMatchObject({ pass: true, score: 1 })
})

test("a seeded-defect marker is still discharged through evidence details", () => {
  const packet = reviewPacket({ attempt_id: "attempt:eval-review-seeded-scope-violation" })
  const evidence = [
    { obligation: "contract_findings", detail: "the diff edits scripts/check-json.py itself" },
    { obligation: "verification_commands", detail: "bun test adapter/opencode/ passed" },
  ]
  expect(run(report({ evidence }), packet)).toMatchObject({ pass: true, score: 1 })
})

test("a seeded-defect review that names the marker nowhere is refused", () => {
  const packet = reviewPacket({ attempt_id: "attempt:eval-review-seeded-scope-violation" })
  const result = run(report(), packet)
  expect(result.pass).toBe(false)
  expect(result.reason).toContain("seeded-defect review misses its marker")
})
