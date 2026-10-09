import { test, expect } from "bun:test"
import { createHash } from "node:crypto"
import { readWorkerReportTexts, selectWorkerReport, WORKER_REPORT_PROTOCOL } from "./worker-report-protocol.js"

// Selector unit tests for the shared report-protocol owner (CON-891 design
// section 3). These exercise only the selector's own contract: designated
// `concord-worker-result-v1` frames, the historical legacy enumeration, byte
// exactness, and refusal kinds. Dispatch admission, schemas, and packet
// integration live in worker-report-protocol.test.ts and dispatch.test.ts.

const sha = (text: string) => createHash("sha256").update(text, "utf8").digest("hex")
const report = () => ({
  schema_version: "1.1",
  readback_model: "provider/model",
  status: "completed",
  evidence: [{ obligation: "commands", detail: "ran the suite" }],
})
const body = () => JSON.stringify(report())
const frame = (b = body(), info = WORKER_REPORT_PROTOCOL) => "```" + info + "\n" + b + "\n```"
// A schema-aware stand-in for the parent's closed-shape check.
const isLaneReport = (value: Record<string, unknown>) =>
  value.schema_version === "1.1" &&
  typeof value.readback_model === "string" &&
  (value.status === "completed" || value.status === "failed") &&
  Array.isArray(value.evidence)

const selected = (result: unknown) => {
  if (typeof result !== "object" || result === null || (result as any).kind !== "selected") {
    throw new Error("expected a selected result, got: " + JSON.stringify(result))
  }
  return result as { kind: "selected"; report: Record<string, unknown>; artifact: { part_index: number; start_byte: number; end_byte: number; text: string; sha256: string } }
}
const refused = (result: unknown, kind: string) => {
  if (typeof result !== "object" || result === null || (result as any).kind !== kind) {
    throw new Error("expected a " + kind + " refusal, got: " + JSON.stringify(result))
  }
  return result as { kind: string; detail: string }
}

test("exports the pinned report protocol identity", () => {
  expect(WORKER_REPORT_PROTOCOL).toBe("concord-worker-result-v1")
})

test("one designated frame selects its body with exact frame bytes", () => {
  const text = frame()
  const result = selected(selectWorkerReport([text], { protocol: WORKER_REPORT_PROTOCOL }))
  expect(result.report).toEqual(report())
  expect(result.artifact.part_index).toBe(0)
  expect(result.artifact.start_byte).toBe(0)
  expect(result.artifact.end_byte).toBe(Buffer.byteLength(text, "utf8"))
  expect(result.artifact.text).toBe(text)
  expect(result.artifact.sha256).toBe(sha(text))
})

test("designated frame survives trailing prose and unrelated JSON without mutation of inputs", () => {
  const text = `${frame()}\nAn unrelated example follows.\n{"example":true}`
  const parts = ["working prose", text, '{"example":true}']
  const snapshot = [...parts]
  const result = selected(selectWorkerReport(parts, { protocol: WORKER_REPORT_PROTOCOL }))
  expect(result.artifact.part_index).toBe(1)
  expect(result.artifact.start_byte).toBe(0)
  expect(result.artifact.text).toBe(frame())
  expect(parts).toEqual(snapshot)
})

test("artifact byte offsets are UTF-8 exact around multibyte prose", () => {
  const prefix = "é😀中\n" // 2 + 4 + 3 + 1 = 10 UTF-8 bytes
  const text = prefix + frame() + "\ntrailing" // trailing prose owns its line; the closing fence must own its own
  const result = selected(selectWorkerReport([text], { protocol: WORKER_REPORT_PROTOCOL }))
  expect(result.artifact.start_byte).toBe(10)
  expect(result.artifact.end_byte).toBe(10 + Buffer.byteLength(frame(), "utf8"))
  expect(result.artifact.end_byte - result.artifact.start_byte).toBe(Buffer.byteLength(result.artifact.text, "utf8"))
  expect(result.artifact.text).toBe(frame())
})

test("CRLF framing selects and the artifact preserves the original bytes", () => {
  const text = "```concord-worker-result-v1\r\n" + body() + "\r\n```"
  const result = selected(selectWorkerReport([text], { protocol: WORKER_REPORT_PROTOCOL }))
  expect(result.report).toEqual(report())
  expect(result.artifact.text).toBe(text)
  expect(result.artifact.start_byte).toBe(0)
  expect(result.artifact.end_byte).toBe(Buffer.byteLength(text, "utf8"))
})

test("a closing fence with trailing spaces and a frame at end of part both close", () => {
  const noNewline = "```concord-worker-result-v1\n" + body() + "\n```"
  const trailingSpaces = "```concord-worker-result-v1\n" + body() + "\n```  "
  for (const text of [noNewline, trailingSpaces]) {
    expect(selected(selectWorkerReport([text], { protocol: WORKER_REPORT_PROTOCOL })).report).toEqual(report())
  }
})

test("an unclosed designated frame is malformed", () => {
  const result = refused(selectWorkerReport(["```concord-worker-result-v1\n" + body()], { protocol: WORKER_REPORT_PROTOCOL }), "malformed")
  expect(result.detail).toContain("never closed")
})

test("a designated frame with broken JSON is malformed", () => {
  const result = refused(selectWorkerReport([frame('{"status":')], { protocol: WORKER_REPORT_PROTOCOL }), "malformed")
  expect(result.detail).toContain("strict JSON")
})

test("a designated frame whose body is not an object is malformed", () => {
  refused(selectWorkerReport([frame("[]")], { protocol: WORKER_REPORT_PROTOCOL }), "malformed")
  refused(selectWorkerReport([frame('"text"')], { protocol: WORKER_REPORT_PROTOCOL }), "malformed")
})

test("duplicate top-level keys in a designated frame are malformed", () => {
  const result = refused(selectWorkerReport([frame(body().replace('"status":"completed"', '"status":"failed","status":"completed"'))], { protocol: WORKER_REPORT_PROTOCOL }), "malformed")
  expect(result.detail).toContain("duplicate")
})

test("duplicate nested keys and escaped duplicate key names are malformed", () => {
  const nested = '{"status":"completed","outer":{"a":1,"a":2}}'
  const escaped = '{"status":"completed","statu\\u0073":"failed","evidence":[]}'
  for (const b of [nested, escaped]) {
    const result = refused(selectWorkerReport([frame(b)], { protocol: WORKER_REPORT_PROTOCOL }), "malformed")
    expect(result.detail).toContain("duplicate")
  }
})

test("escaped quotes, braces, and backticks inside a frame body survive framing", () => {
  const tricky = JSON.stringify({ ...report(), evidence: [{ obligation: "commands", detail: "ran `go test` with \" { } ``` escaped" }] })
  const result = selected(selectWorkerReport([frame(tricky)], { protocol: WORKER_REPORT_PROTOCOL }))
  expect(result.report.evidence).toEqual([{ obligation: "commands", detail: "ran `go test` with \" { } ``` escaped" }])
})

test("two designated frames refuse as ambiguous even when bytes match", () => {
  for (const parts of [[`${frame()}\n${frame()}`], [frame(), frame()], [frame(), frame(body().replace("completed", "failed"))]]) {
    const result = refused(selectWorkerReport(parts, { protocol: WORKER_REPORT_PROTOCOL }), "ambiguous")
    expect(result.detail).toContain("2")
  }
})

test("a designated dispatch never falls back to an unframed valid legacy report", () => {
  refused(selectWorkerReport([body()], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
  refused(selectWorkerReport(["Here is the report:\n" + body()], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
})

test("malformed ordinary JSON without a frame stays absent on a designated dispatch", () => {
  refused(selectWorkerReport(['{"status":'], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
  refused(selectWorkerReport(["```json\n{\"status\":\n```"], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
})

test("a reserved marker inside another fence is not a frame", () => {
  const nested = "```\n```concord-worker-result-v1\n" + body() + "\n```\n```"
  refused(selectWorkerReport([nested], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
})

test("an indented reserved marker is not a frame", () => {
  refused(selectWorkerReport(["  ```concord-worker-result-v1\n" + body() + "\n```"], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
})

test("unknown and partial reserved versions refuse as unsupported in both modes", () => {
  for (const info of ["concord-worker-result-v2", "concord-worker-result", "concord-worker-result-v1x"]) {
    refused(selectWorkerReport([frame(body(), info)], { protocol: WORKER_REPORT_PROTOCOL }), "unsupported_protocol")
    refused(selectWorkerReport([frame(body(), info)], { isReport: isLaneReport }), "unsupported_protocol")
  }
})

test("a caller-pinned protocol other than the implemented one refuses immediately", () => {
  refused(selectWorkerReport([], { protocol: "concord-worker-result-v9" }), "unsupported_protocol")
  refused(selectWorkerReport([frame()], { protocol: "concord-worker-result-v9" }), "unsupported_protocol")
})

test("an undefined or null protocol selects through the historical legacy path", () => {
  expect(selected(selectWorkerReport([body()], { isReport: isLaneReport })).report).toEqual(report())
  expect(selected(selectWorkerReport([body()], { protocol: null, isReport: isLaneReport })).report).toEqual(report())
})

test("legacy standalone JSON selects with exact offsets and digest", () => {
  const text = "  " + body() + "  "
  const result = selected(selectWorkerReport([text], { isReport: isLaneReport }))
  expect(result.report).toEqual(report())
  expect(result.artifact.start_byte).toBe(2)
  expect(result.artifact.end_byte).toBe(2 + Buffer.byteLength(body(), "utf8"))
  expect(result.artifact.text).toBe(body())
  expect(result.artifact.sha256).toBe(sha(body()))
})

test("legacy plain and json fences stay readable", () => {
  for (const text of ["```json\n" + body() + "\n```", "```\n" + body() + "\n```"]) {
    expect(selected(selectWorkerReport([text], { isReport: isLaneReport })).report).toEqual(report())
  }
})

test("legacy prose before and after an inline report still selects the balanced object", () => {
  const lead = "Here is the report:\n"
  const text = lead + body() + "\nDone."
  const result = selected(selectWorkerReport([text], { isReport: isLaneReport }))
  expect(result.artifact.start_byte).toBe(Buffer.byteLength(lead, "utf8"))
  expect(result.artifact.text).toBe(body())
})

test("legacy prose only after a leading report object does not poison it", () => {
  const result = selected(selectWorkerReport([body() + "\nDone."], { isReport: isLaneReport }))
  expect(result.artifact.text).toBe(body())
})

test("unrelated objects are not legacy candidates", () => {
  refused(selectWorkerReport(['{"example":true}'], { isReport: isLaneReport }), "absent")
})

test("unrelated objects beside a report do not compete with it", () => {
  const result = selected(selectWorkerReport([body() + '\n{"example":true}'], { isReport: isLaneReport }))
  expect(result.artifact.text).toBe(body())
})

test("incomplete braces are never repaired", () => {
  refused(selectWorkerReport(['summary {"status":"completed" never closed'], { isReport: isLaneReport }), "absent")
  const result = selected(selectWorkerReport([body() + ' trailing {"broken'], { isReport: isLaneReport }))
  expect(result.artifact.text).toBe(body())
})

test("two distinct legacy candidates refuse as ambiguous even when equal", () => {
  const equal = `${body()}\n${body()}`
  refused(selectWorkerReport([equal], { isReport: isLaneReport }), "ambiguous")
  refused(selectWorkerReport([body(), body()], { isReport: isLaneReport }), "ambiguous")
})

test("a whole-document report counts once and nested report-shaped values are not excavated", () => {
  expect(selected(selectWorkerReport([body()], { isReport: isLaneReport })).artifact.text).toBe(body())
  const nested = JSON.stringify({ ...report(), summary: { ...report(), status: "failed" } })
  expect(selected(selectWorkerReport([nested], { isReport: isLaneReport })).artifact.text).toBe(nested)
})

test("a single invalid report-shaped candidate still selects so the closed schema can refuse it", () => {
  const invalid = JSON.stringify({ ...report(), extra: true })
  const result = selected(selectWorkerReport([invalid], { isReport: () => false }))
  expect(result.report.extra).toBe(true)
})

test("mixed valid and invalid legacy candidates refuse as malformed", () => {
  const invalid = JSON.stringify({ status: "completed" })
  refused(selectWorkerReport([body() + "\n" + invalid], { isReport: isLaneReport }), "malformed")
  refused(selectWorkerReport([body(), invalid], { isReport: isLaneReport }), "malformed")
})

test("a final announced malformed document invalidates an earlier valid candidate", () => {
  refused(selectWorkerReport([body() + '\n```json\n{"status":\n```'], { isReport: isLaneReport }), "malformed")
})

test("an earlier announced malformed document does not beat a later valid candidate", () => {
  const result = selected(selectWorkerReport(['```json\n{"status":\n```\n' + body()], { isReport: isLaneReport }))
  expect(result.artifact.text).toBe(body())
})

test("a lone announced malformed document is malformed, not absent", () => {
  const result = refused(selectWorkerReport(['{"status":'], { isReport: isLaneReport }), "malformed")
  expect(result.detail.length).toBeGreaterThan(0)
})

test("duplicate keys in a legacy document are malformed", () => {
  refused(selectWorkerReport(['{"status":"completed","status":"failed"}'], { isReport: isLaneReport }), "malformed")
})

test("escaped quotes and braces inside legacy inline objects parse", () => {
  const note = 'said "hi" { and } done'
  const doc = JSON.stringify({ status: "completed", note })
  const result = selected(selectWorkerReport(["prose " + doc + " epilogue"], { isReport: isLaneReport }))
  expect(result.report.note).toBe(note)
})

test("the byte bound counts UTF-8 bytes, not characters", () => {
  const sixChars = "éééééé" // 12 UTF-8 bytes
  expect(selectWorkerReport([sixChars], { maxBytes: 10, isReport: isLaneReport }).kind).toBe("over_bound")
  expect(selectWorkerReport(["1234567890"], { maxBytes: 10, isReport: isLaneReport }).kind).toBe("absent")
})

test("output at exactly the default 65536-byte bound is admitted and one byte over refuses", () => {
  const atBound = JSON.stringify({ ...report(), pad: "a".repeat(65536 - JSON.stringify({ ...report(), pad: "" }).length) })
  expect(Buffer.byteLength(atBound, "utf8")).toBe(65536)
  expect(selected(selectWorkerReport([atBound], { isReport: isLaneReport })).report.status).toBe("completed")
  const over = JSON.stringify({ ...report(), pad: "a".repeat(65537 - JSON.stringify({ ...report(), pad: "" }).length) })
  expect(Buffer.byteLength(over, "utf8")).toBe(65537)
  refused(selectWorkerReport([over], { isReport: isLaneReport }), "over_bound")
})

test("the byte bound refuses before a present valid frame is considered", () => {
  const result = refused(selectWorkerReport([frame(), '{"pad":"' + "a".repeat(65536) + '"}'], { protocol: WORKER_REPORT_PROTOCOL }), "over_bound")
  expect(result.detail).toContain("65536")
})

test("empty and whitespace-only input stays absent in both modes", () => {
  refused(selectWorkerReport([], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
  refused(selectWorkerReport([], { isReport: isLaneReport }), "absent")
  refused(selectWorkerReport(["  \n\r "], { isReport: isLaneReport }), "absent")
})

test("a throwing isReport is treated as a shape refusal, not a selector crash", () => {
  const result = selected(selectWorkerReport([body()], { isReport: () => { throw new Error("boom") } }))
  expect(result.report.status).toBe("completed")
})

test("a three-backtick line does not close a four-backtick quoting fence", () => {
  const quoted = "````\nquoted preamble\n```\n```" + WORKER_REPORT_PROTOCOL + "\n" + body() + "\n```\n````"
  refused(selectWorkerReport([quoted], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
})

test("reserved markers inside tilde fences are ignored", () => {
  const quoted = "~~~~\n```" + WORKER_REPORT_PROTOCOL + "\n" + body() + "\n```\n~~~~"
  refused(selectWorkerReport([quoted], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
  refused(selectWorkerReport(["~~~" + WORKER_REPORT_PROTOCOL + "\n" + body() + "\n~~~"], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
})

test("designated framing requires exactly three backticks; longer runs and tildes quote only", () => {
  refused(selectWorkerReport(["````" + WORKER_REPORT_PROTOCOL + "\n" + body() + "\n````"], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
  refused(selectWorkerReport(["````concord-worker-result-v2\n{}\n````"], { protocol: WORKER_REPORT_PROTOCOL }), "absent")
  refused(selectWorkerReport(["````concord-worker-result-v2\n{}\n````"], { isReport: isLaneReport }), "absent")
})

test("tilde-fenced legacy reports stay readable", () => {
  expect(selected(selectWorkerReport(["~~~json\n" + body() + "\n~~~"], { isReport: isLaneReport })).report).toEqual(report())
})

test("a whole legacy array document is a diagnostic, never an excavation site", () => {
  refused(selectWorkerReport(["[" + body() + "]"], { isReport: isLaneReport }), "absent")
  refused(selectWorkerReport([JSON.stringify([report(), { example: true }])], { isReport: isLaneReport }), "absent")
  refused(selectWorkerReport(["```json\n[" + body() + "]\n```"], { isReport: isLaneReport }), "absent")
})

test("without isReport, legacy candidates must still carry report-owned fields", () => {
  refused(selectWorkerReport(['{"note":"unrelated"}']), "absent")
  expect(selected(selectWorkerReport([body()])).report).toEqual(report())
})

// Stream collection: readWorkerReportTexts turns newline-delimited official
// host run events into ordered report-channel text parts. The current
// protocol pin enforces one session identity, part ids, snapshot
// reconciliation, and the terminal stop signal; historical fixtures without
// ids or a terminal signal stay readable.

const streamEvent = (type: string, part: Record<string, unknown>, sessionID = "session-run") =>
  JSON.stringify({ type, timestamp: 1, sessionID, part })
const streamText = (id: string, text: string, sessionID = "session-run") =>
  streamEvent("text", { id, sessionID, type: "text", text }, sessionID)
const streamStart = (sessionID = "session-run") => streamEvent("step_start", { type: "step-start" }, sessionID)
const streamStop = (sessionID = "session-run") => streamEvent("step_finish", { type: "step-finish", reason: "stop" }, sessionID)
const runStream = (lines: string[]) => lines.join("\n")
const collected = (result: unknown) => {
  if (typeof result !== "object" || result === null || typeof (result as any).detail === "string") {
    throw new Error("expected a clean collection, got: " + JSON.stringify(result))
  }
  return (result as { texts: string[] }).texts
}
const refusedCollection = (result: unknown) => {
  if (typeof result !== "object" || result === null || typeof (result as any).detail !== "string" || ((result as any).texts as string[]).length !== 0) {
    throw new Error("expected a refused collection, got: " + JSON.stringify(result))
  }
  return (result as { detail: string }).detail
}

test("current collection keeps text-part order and never collects tool, reasoning, or status JSON", () => {
  const stream = runStream([
    streamStart(),
    streamText("part-0", frame()),
    streamEvent("tool", { type: "tool", tool: "bash", state: { output: '{"status":"completed"}' } }),
    streamEvent("reasoning", { type: "reasoning", text: '{"status":"completed"}' }),
    streamEvent("status", { type: "status", data: '{"status":"completed"}' }),
    streamText("part-1", '{"example":true}'),
    streamStop(),
  ]) + "\n"
  expect(collected(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toEqual([frame(), '{"example":true}'])
})

test("exact replays of the same session part collect once", () => {
  const stream = runStream([streamStart(), streamText("part-0", "hello"), streamText("part-0", "hello"), streamStop()])
  expect(collected(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toEqual(["hello"])
})

test("growing snapshots replace earlier prefixes with the final text in place", () => {
  const stream = runStream([
    streamStart(),
    streamText("part-0", "He"),
    streamText("part-1", "world"),
    streamText("part-0", "Hello"),
    streamText("part-0", "Hello world"),
    streamStop(),
  ])
  expect(collected(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toEqual(["Hello world", "world"])
})

test("conflicting replacement of the same part id refuses", () => {
  const stream = runStream([streamStart(), streamText("part-0", "Hello"), streamText("part-0", "Goodbye"), streamStop()])
  expect(refusedCollection(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toContain("part-0")
})

test("foreign or missing session identities refuse under the current pin", () => {
  expect(refusedCollection(readWorkerReportTexts(runStream([streamStart(), streamText("part-0", "a"), streamText("part-1", "b", "session-other"), streamStop()]), { protocol: WORKER_REPORT_PROTOCOL }))).toContain("session")
  expect(refusedCollection(readWorkerReportTexts(runStream([streamStart(), JSON.stringify({ type: "text", timestamp: 2, part: { id: "part-0", type: "text", text: "a" } }), streamStop()]), { protocol: WORKER_REPORT_PROTOCOL }))).toContain("session")
})

test("text parts without an id refuse under the current pin", () => {
  const stream = runStream([streamStart(), streamEvent("text", { type: "text", text: "a" }), streamStop()])
  expect(refusedCollection(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toContain("part id")
})

test("a part session identity that disagrees with its event refuses", () => {
  const stream = runStream([streamStart(), streamEvent("text", { id: "part-0", sessionID: "session-other", type: "text", text: "a" }), streamStop()])
  expect(refusedCollection(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toContain("session")
})

test("a missing terminal stop signal refuses under the current pin", () => {
  const stream = runStream([streamStart(), streamText("part-0", "a")])
  expect(refusedCollection(readWorkerReportTexts(stream, { protocol: WORKER_REPORT_PROTOCOL }))).toContain("terminal")
})

test("the terminal gate reads the last step_finish and admits multi-step streams", () => {
  const aborted = runStream([streamStart(), streamText("part-0", "a"), streamEvent("step_finish", { type: "step-finish", reason: "aborted" })])
  expect(refusedCollection(readWorkerReportTexts(aborted, { protocol: WORKER_REPORT_PROTOCOL }))).toContain("step_finish")
  const multiStep = runStream([
    streamStart(),
    streamText("part-0", "a"),
    streamEvent("step_finish", { type: "step-finish", reason: "tool_use" }),
    streamStart(),
    streamText("part-1", "b"),
    streamStop(),
  ])
  expect(collected(readWorkerReportTexts(multiStep, { protocol: WORKER_REPORT_PROTOCOL }))).toEqual(["a", "b"])
})

test("malformed official event lines refuse in both modes", () => {
  for (const line of ["{not json", JSON.stringify({ type: "text", sessionID: "session-run" })]) {
    expect(refusedCollection(readWorkerReportTexts(runStream([streamStart(), line, streamStop()]), { protocol: WORKER_REPORT_PROTOCOL }))).toContain("malformed")
    expect(refusedCollection(readWorkerReportTexts(line, {}))).toContain("malformed")
  }
})

test("plugin logs and status JSON never enter the report channel", () => {
  const logs = ["42", "[plugin] status", JSON.stringify({ plugin: "routing", event: "config.loaded" }), JSON.stringify({ type: "message.updated", properties: { sessionID: "other-session" } })]
  const output = runStream([streamStart(), ...logs, streamText("part-0", "report text"), streamStop()])
  expect(collected(readWorkerReportTexts(output, { protocol: WORKER_REPORT_PROTOCOL }))).toEqual(["report text"])
  expect(collected(readWorkerReportTexts(logs.join("\n"), {}))).toEqual([])
})

test("a current malformed text event refuses while historical non-text output stays absent", () => {
  const line = JSON.stringify({ type: "text", sessionID: "session-run", part: { type: "text" } })
  expect(refusedCollection(readWorkerReportTexts(runStream([streamStart(), line, streamStop()]), { protocol: WORKER_REPORT_PROTOCOL }))).toContain("malformed")
  expect(collected(readWorkerReportTexts(line, {}))).toEqual([])
})

test("historical fixtures without ids or a terminal signal stay readable", () => {
  const fixture = JSON.stringify({ type: "text", part: { type: "text", text: "hello" } })
  const fixture2 = JSON.stringify({ type: "text", part: { type: "text", text: "world" } })
  expect(collected(readWorkerReportTexts(runStream([fixture, fixture2]), {}))).toEqual(["hello", "world"])
  expect(collected(readWorkerReportTexts(runStream([streamText("part-0", "a"), streamText("part-0", "a")]), {}))).toEqual(["a"])
})

test("empty stream output refuses under the current pin and stays empty historically", () => {
  expect(refusedCollection(readWorkerReportTexts("", { protocol: WORKER_REPORT_PROTOCOL }))).toContain("terminal")
  expect(collected(readWorkerReportTexts("", {}))).toEqual([])
})

test("an unimplemented protocol pin refuses collection immediately", () => {
  expect(refusedCollection(readWorkerReportTexts(runStream([streamStart(), streamStop()]), { protocol: "concord-worker-result-v9" }))).toContain("unsupported")
})
