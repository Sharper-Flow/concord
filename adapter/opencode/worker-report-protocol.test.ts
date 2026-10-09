import { expect, test } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { resolveWorkerReport, resolveWorkerReportFromText, validateAgentLanePacket, type AgentLanePacket } from "./dispatch"

const lane = agentLanes[0]
const protocol = "concord-worker-result-v1"
const packet = (current = true): AgentLanePacket => ({
  schema_version: "1.1", attempt_id: "attempt-protocol", work_id: "work-protocol", step_id: "investigate",
  lane_id: lane.id, lane_version: lane.version, lane_digest: lane.digest,
  inputs: { task: "Report bounded findings.", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "bounded_findings" }, ...(current ? { report_protocol: protocol } : {}) },
})
const report = () => ({
  readback_model: "provider/model", status: "completed", evidence: lane.evidence_obligations.map(obligation => ({ obligation, detail: "Bounded fixture evidence." })),
})
const frame = (body = JSON.stringify(report()), info = protocol) => `\`\`\`${info}\n${body}\n\`\`\``
const run = (texts: string[]) => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-protocol", part: { type: "step-start" } }),
  ...texts.map((text, index) => JSON.stringify({ type: "text", timestamp: index + 2, sessionID: "session-protocol", part: { id: `part-${index}`, sessionID: "session-protocol", type: "text", text } })),
  JSON.stringify({ type: "step_finish", timestamp: texts.length + 3, sessionID: "session-protocol", part: { type: "step-finish", reason: "stop" } }),
].join("\n")
const refusal = (value: ReturnType<typeof resolveWorkerReportFromText>) => "detail" in value ? value.detail : "unexpected admission"

test("current packet declares the dispatch-owned report protocol", () => {
  expect(validateAgentLanePacket(packet())).toBe(true)
  expect(validateAgentLanePacket({ ...packet(), inputs: { ...packet().inputs, report_protocol: "unknown" } })).toBe(false)
})

test("designated report survives trailing unrelated JSON on native and stream admission", () => {
  const text = `${frame()}\nAn unrelated example follows.\n{"example":true}`
  for (const result of [resolveWorkerReportFromText(text, packet()), resolveWorkerReport(run([frame(), '{"example":true}']), packet())]) {
    expect("report" in result).toBe(true)
    if ("report" in result) expect(result.report.evidence).toEqual(report().evidence)
  }
})

test("current protocol never falls back to an unframed valid legacy report", () => {
  expect(refusal(resolveWorkerReportFromText(JSON.stringify(report()), packet()))).toContain("no agent-lane-report")
})

test("two designated frames refuse even when their bytes match", () => {
  for (const text of [`${frame()}\n${frame()}`, `${frame()}\n${frame(JSON.stringify({ ...report(), status: "failed" }))}`]) {
    expect(refusal(resolveWorkerReportFromText(text, packet()))).toContain("ambiguous")
  }
  expect(refusal(resolveWorkerReport(run([frame(), frame()]), packet()))).toContain("ambiguous")
})

test("malformed announced final frame cannot use an earlier legacy report", () => {
  for (const broken of [frame('{"status":'), `\`\`\`${protocol}\n${JSON.stringify(report())}`, frame("[]")]) {
    expect(refusal(resolveWorkerReportFromText(`${JSON.stringify(report())}\n${broken}`, packet()))).toContain("malformed")
  }
})

test("unknown reserved protocol versions refuse instead of legacy fallback", () => {
  expect(refusal(resolveWorkerReportFromText(`${JSON.stringify(report())}\n${frame(JSON.stringify(report()), "concord-worker-result-v2")}`, packet()))).toContain("unsupported")
})

test("duplicate keys in report content are malformed, including escaped keys", () => {
  for (const body of [JSON.stringify(report()).replace('"status":"completed"', '"status":"failed","status":"completed"'), '{"status":"completed","statu\\u0073":"failed","evidence":[]}']) {
    expect(refusal(resolveWorkerReportFromText(frame(body), packet()))).toContain("duplicate")
  }
})

test("framed admission preserves closed content validation and dispatch-owned identity", () => {
  const value = { ...report(), schema_version: "forged", worker_job: "forged", attempt_id: "forged", context_findings: [{ kind: "observation" as const, statement: "A retained claim.", subject_ref: "fixture", evidence_refs: [], domain_id: "agent-surface" }] }
  const result = resolveWorkerReportFromText(frame(JSON.stringify(value)), packet())
  expect("report" in result).toBe(true)
  if ("report" in result) {
    expect(result.report.attempt_id).toBe(packet().attempt_id)
    expect(result.report.schema_version).toBe("1.1")
    expect(result.report.worker_job).toBeUndefined()
    expect(result.report.context_findings).toEqual(value.context_findings)
  }
  expect(refusal(resolveWorkerReportFromText(frame(JSON.stringify({ ...report(), extra: true })), packet()))).toContain("closed agent-lane-report")
  expect(refusal(resolveWorkerReportFromText(frame(JSON.stringify({ ...report(), evidence: report().evidence.slice(0, 1) })), packet()))).toContain("undischarged")
})

test("legacy report selection is schema-aware and refuses two distinct candidates", () => {
  const legacy = { schema_version: "1.1", ...report() }
  const text = `${JSON.stringify(legacy)}\n{"example":true}`
  expect("report" in resolveWorkerReportFromText(text, packet(false))).toBe(true)
  expect(refusal(resolveWorkerReportFromText(`${JSON.stringify(legacy)}\n${JSON.stringify(legacy)}`, packet(false)))).toContain("ambiguous")
})

test("legacy plain and fenced reports remain readable without repairing malformed JSON", () => {
  const json = JSON.stringify({ schema_version: "1.1", ...report() })
  for (const text of [json, `\`\`\`json\n${json}\n\`\`\``, `Here is the report:\n${json}\nDone.`]) expect("report" in resolveWorkerReportFromText(text, packet(false))).toBe(true)
  expect(refusal(resolveWorkerReportFromText(`${json}\n\`\`\`json\n{"status":\n\`\`\``, packet(false)))).toContain("malformed")
})
