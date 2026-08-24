import { test, expect } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { canonicalWorkerEvidence, dispatchWorker, type AgentLanePacket, type DispatchAuthorizer, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import workerEvidenceVector from "./worker-evidence-vector.json"

const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
// CD-0067 D6: the dispatchWorker options carry packetDigest so the
// adapter can quote it on the signed assertion. Every test that
// exercises the dispatch path passes this constant; tests that
// probe the missing-digest refusal build their own call without it.
const PACKET_DIGEST = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

// Permissive authorizer: every dispatch_worker request acknowledges with an
// `ok` core envelope so the evidence-write path under test can run.
const permissiveAuthorizer = (): DispatchAuthorizer => async () => ({ schema_version: "1.0", request_id: "auth-evidence", origin: "core", tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false })

function packet(): AgentLanePacket {
  return {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
    lane_digest: lane.digest, work_id: "work-1", step_id: "step-1", inputs: { task: "bounded task" },
  }
}

// A completion is only reachable when the worker returns an admissible
// agent-lane-report.v1 report (CD-0056 D7), so the fixture stream carries one.
const laneReport = () => ({
  schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
  lane_digest: lane.digest, readback_model: READBACK_MODEL, status: "completed",
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
})

const runOutput = () => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  JSON.stringify({ type: "text", timestamp: 2, sessionID: "session-1", part: { type: "text", text: JSON.stringify(laneReport()) } }),
  JSON.stringify({ type: "step_finish", timestamp: 3, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
].join("\n")

const exportedSession = (model = READBACK_MODEL) => JSON.stringify({
  info: { id: "session-1" },
  messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] }],
})

const laneRunner: DispatchRunner = {
  async run(argv) {
    if (argv[1] === "run") return { exitCode: 0, stdout: runOutput(), stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
}

// A worker that reports its own failure is the reachable worker-fail path
// (CD-0056 D7): the report is admissible, and its status is the failure.
const failedRunOutput = () => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  JSON.stringify({ type: "text", timestamp: 2, sessionID: "session-1", part: { type: "text", text: JSON.stringify({ ...laneReport(), status: "failed" }) } }),
  JSON.stringify({ type: "step_finish", timestamp: 3, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
].join("\n")

const failingLaneRunner: DispatchRunner = {
  async run(argv) {
    if (argv[1] === "run") return { exitCode: 0, stdout: failedRunOutput(), stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
}

function evidenceCollector(recorded: Record<string, unknown>[]): DispatchRunner {
  return {
    async run(argv, input) {
      recorded.push({ command: argv[1], request: JSON.parse(input) })
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
}

// The Go encoder produced this vector. Agreement here is what makes the
// signature meaningful: a TypeScript encoder that drifts would sign bytes the
// core never verifies, and the boundary would fail open at the adapter.
for (const vectorCase of workerEvidenceVector.cases) {
  test(`the TypeScript canonical encoder matches the Go vector byte for byte for ${vectorCase.verb}`, () => {
    const encoded = canonicalWorkerEvidence(vectorCase.assertion as Record<string, unknown>)
    expect(Buffer.from(encoded).toString("base64")).toBe(vectorCase.canonical_base64)
  })
}

test("canonical encoding is order-fixed, not object-key dependent", () => {
  const vectorCase = workerEvidenceVector.cases[0]
  const reversed = Object.fromEntries(Object.entries(vectorCase.assertion).reverse())
  expect(Buffer.from(canonicalWorkerEvidence(reversed)).toString("base64")).toBe(vectorCase.canonical_base64)
})

test("dispatch and completion evidence each carry a bound assertion", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: laneRunner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  expect(result.outcome).toBe("ok")
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-complete"])

  for (const entry of recorded) {
    const assertion = (entry.request as any).assertion
    expect(typeof assertion.signature).toBe("string")
    expect(assertion.signature.length).toBeGreaterThan(0)
    expect(assertion.verb).toBe(entry.command)
    expect(assertion.work_id).toBe("work-1")
    expect(assertion.attempt_id).toBe("attempt-1")
    expect(assertion.lane_digest).toBe(lane.digest)
    expect(assertion.readback_model).toBe(READBACK_MODEL)
    expect(assertion.nonce.length).toBeGreaterThanOrEqual(16)
  }
})

test("each evidence write carries its own nonce", async () => {
  const recorded: Record<string, unknown>[] = []
  await dispatchWorker(packet(), { credentials: testCredentials, runner: laneRunner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  const nonces = recorded.map((entry) => (entry.request as any).assertion.nonce)
  expect(new Set(nonces).size).toBe(nonces.length)
})

// The worker never sees the proof that authorizes its own evidence. If it did,
// a lane run could forge evidence for a later attempt.
test("the signing proof never reaches the worker packet or prompt", async () => {
  let spawnedArgv: string[] = []
  const recorded: Record<string, unknown>[] = []
  const runner: DispatchRunner = {
    async run(argv) {
      if (argv[1] === "run") { spawnedArgv = argv; return { exitCode: 0, stdout: runOutput(), stderr: "" } }
      if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  expect(result.outcome).toBe("ok")
  expect(spawnedArgv.join(" ")).not.toContain("signature")
  expect(spawnedArgv.join(" ")).not.toContain("assertion")
  expect(JSON.stringify(result)).not.toContain("signature")
})

// A failure is evidence about the same attempt as its dispatch, so it claims
// the same identity. The CLI enriches lane identity from the stored attempt row
// for both terminal verbs (cmd/concord/main.go applyWorkerEvidence), so an
// assertion that leaves those fields empty cannot match the binding.
test("failure evidence carries a bound assertion including lane identity", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: failingLaneRunner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  expect(result.outcome).toBe("error")
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-fail"])

  const assertion = (recorded[1].request as any).assertion
  expect(assertion.verb).toBe("worker-fail")
  expect(assertion.work_id).toBe("work-1")
  expect(assertion.attempt_id).toBe("attempt-1")
  expect(assertion.lane_id).toBe(lane.id)
  expect(assertion.lane_version).toBe(lane.version)
  expect(assertion.lane_digest).toBe(lane.digest)
  expect(assertion.readback_model).toBe(READBACK_MODEL)
  expect(assertion.failure_kind).toBe("worker_error")
})

// The signed field set is the boundary, not any one verb's fields. The shared
// vector declares what the CLI binding populates per verb, and the Go side
// proves that declaration against the CLI, so an adapter that signs a
// different set fails here rather than at a caller's evidence write.
const SIGNING_ENVELOPE_FIELDS = ["client_ref", "issued_at", "nonce", "signature"]

test("every verb signs exactly the field set its CLI binding populates", async () => {
  const signed = new Map<string, Record<string, unknown>>()
  for (const runner of [laneRunner, failingLaneRunner]) {
    const recorded: Record<string, unknown>[] = []
    await dispatchWorker(packet(), { credentials: testCredentials, runner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
    for (const entry of recorded) signed.set(entry.command as string, (entry.request as any).assertion)
  }
  expect([...signed.keys()].sort()).toEqual(workerEvidenceVector.cases.map((vectorCase) => vectorCase.verb).sort())

  for (const vectorCase of workerEvidenceVector.cases) {
    const assertion = signed.get(vectorCase.verb)
    expect(assertion).toBeDefined()
    const want = [...vectorCase.bound_fields, ...SIGNING_ENVELOPE_FIELDS].sort()
    expect(Object.keys(assertion as Record<string, unknown>).sort()).toEqual(want)
  }
})

// Without a credential the adapter cannot authorize evidence. A run that cannot
// be recorded is never reported as a success.
test("an unavailable credential fails the run instead of recording unsigned evidence", async () => {
  const recorded: Record<string, unknown>[] = []
  const broken: CredentialStore = { async getPrivateKey() { throw new Error("credential service unavailable") } }
  const result = await dispatchWorker(packet(), { credentials: broken, runner: laneRunner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  expect(result.outcome).toBe("error")
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(recorded).toHaveLength(0)
})

// CD-0067 D6: dispatch evidence carries the packet digest the core
// recorded. complete and fail sign packet_digest as empty (the canonical
// encoder emits a 0-byte slot regardless of whether the JSON carries the
// key), and the shared vector pins that byte sequence. The dispatch
// assertion is the only verb that names packet_digest in its JSON, so the
// test asserts the dispatch side here; the byte-level agreement is the
// vector-driven loop above.
test("dispatch evidence carries the packet digest the core recorded", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: laneRunner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer(), packetDigest: PACKET_DIGEST })
  expect(result.outcome).toBe("ok")
  const dispatched = (recorded[0].request as any).assertion
  expect(dispatched.packet_digest).toBe(PACKET_DIGEST)
})

// CD-0067 D6: dispatchWorker refuses to sign without the digest the
// core recorded. The error is invalid_input — the same kind the
// packet validator returns — so a worker that did not see the digest
// reports the omission the same way it would report any other
// malformed input. The run already happened on the worker side; the
// envelope is the failure surface.
test("dispatchWorker refuses to sign without packetDigest", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: laneRunner, evidenceRunner: evidenceCollector(recorded), authorize: permissiveAuthorizer() })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toContain("packet digest")
  expect(recorded).toHaveLength(0)
})
