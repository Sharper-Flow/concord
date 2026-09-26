import { test, expect } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { abandonWorkerAttempt, canonicalWorkerEvidence, completeWorkerAttempt, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { hostControlPlane, type SessionReader } from "./move-session"
import workerEvidenceVector from "./worker-evidence-vector.json"
import workerCLIRequiredFields from "./worker-cli-required-fields.json"

const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
// CD-0067 D6: the dispatchWorker options carry packetDigest so the
// adapter can quote it on the signed assertion. Every test that
// exercises the dispatch path passes this constant; tests that
// probe the missing-digest refusal build their own call without it.
const PACKET_DIGEST = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
const WORKER_DIRECTORY = process.cwd()
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }


function packet(): AgentLanePacket {
  return {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
    lane_digest: lane.digest, work_id: "work-1", step_id: "step-1", inputs: { task: "bounded task" },
  }
}

// A completion is only reachable when the worker returns an admissible
// agent-lane-report.v1 report (CD-0056 D7), so the fixture stream carries one.
// The dispatch window owns attempt and lane identity.
const laneReport = () => ({
  schema_version: "1.0", readback_model: READBACK_MODEL, status: "completed",
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
})

const runOutput = () => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  JSON.stringify({ type: "text", timestamp: 2, sessionID: "session-1", part: { type: "text", text: JSON.stringify(laneReport()) } }),
  JSON.stringify({ type: "step_finish", timestamp: 3, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
].join("\n")

test("worker-abandon observes no sessions and sends a signed typed close request", async () => {
  const calls: { argv: string[]; input: Record<string, unknown> }[] = []
  hostControlPlane().bind({
    get: async () => ({ response: new Response("", { status: 200 }), data: [{ id: "ses_other", directory: "/other/worktree" }] }),
    post: async () => ({ response: new Response("", { status: 204 }) }),
  })
  try {
    const runner: DispatchRunner = {
      async run(argv, input) {
        calls.push({ argv, input: JSON.parse(input) as Record<string, unknown> })
        return { exitCode: 0, stdout: "", stderr: "" }
      },
    }
    const result = await abandonWorkerAttempt(lane, packet(), "the host observed that the lane never reported", {
      runner, concordBinary: "concord", credentials: testCredentials,
    }, new AbortController().signal)
    expect(result.error?.message).toContain("the host observed that the lane never reported")
    expect(calls).toHaveLength(1)
    expect(calls[0].argv).toEqual(["concord", "worker-abandon"])
    // worker-fail carries no host session observation; the core reads process
    // liveness from worktree_occupancy (CD-0178 D3).
    expect(calls[0].input.observed_session_directories).toBeUndefined()
    expect(calls[0].input.assertion).toMatchObject({ verb: "worker-fail", failure_kind: "abandoned", readback_model: "" })
    expect(typeof (calls[0].input.assertion as Record<string, unknown>).signature).toBe("string")
  } finally {
    hostControlPlane().bind(undefined)
  }
})

// CD-0102: the fixture session opens with the dispatch packet, so completion's
// packet-identity readback admits it.
const exportedSession = (model = READBACK_MODEL) => JSON.stringify({
  info: { id: "session-1" },
  messages: [
    { info: { id: "message-0", sessionID: "session-1", role: "user", agent: "concord-research", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(packet()) }] },
    { info: { id: "message-1", sessionID: "session-1", role: "assistant", agent: "concord-research", providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] },
  ],
})

const laneRunner: DispatchRunner = {
  async run(argv) {
    if (argv[1] === "run") return { exitCode: 0, stdout: runOutput(), stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(), stderr: "" }
    if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "session-1", directory: "/claimed/worktree" }]), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
}

// The readback reads the worker session in process through the host session
// API; the fixture answers the same transcript the old export arm carried.
const laneSessionReader: SessionReader = {
  async get() { return { data: { id: "session-1" }, response: new Response("{}", { status: 200 }) } },
  async messages(_sessionID, _limit, before) {
    if (before) return { data: [], response: new Response("[]", { status: 200 }) }
    return { data: JSON.parse(exportedSession()).messages, response: new Response("[]", { status: 200 }) }
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
    if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "session-1", directory: "/claimed/worktree" }]), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
}

// CD-0102 D5: completion admits the host's task result. The runner answers the
// session export only, because the host ran the worker.
const SIGNAL = new AbortController().signal
const taskBody = (doc: unknown) =>
  ['<task id="session-1" state="completed">', "<task_result>", JSON.stringify(doc), "</task_result>", "</task>"].join("\n")
const completedBody = () => taskBody(laneReport())
const failedBody = () => taskBody({ ...laneReport(), status: "failed" })

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
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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

test("worker evidence timestamps are minted after credential retrieval", async () => {
  const availableAt: number[] = []
  const delayedCredentials: CredentialStore = {
    async getPrivateKey() {
      await new Promise((resolve) => setTimeout(resolve, 20))
      availableAt.push(Date.now())
      return new Uint8Array(32).fill(7)
    },
  }
  const recorded: Record<string, unknown>[] = []
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), {
    credentials: delayedCredentials, sessionReader: laneSessionReader,
    evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY,
  }, SIGNAL)
  expect(result.outcome).toBe("ok")
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-complete"])
  for (const [index, entry] of recorded.entries()) {
    const assertion = (entry.request as any).assertion
    expect(Date.parse(assertion.issued_at)).toBeGreaterThanOrEqual(availableAt[index])
  }

  const refused: Record<string, unknown>[] = []
  const refusal = await completeWorkerAttempt(lane, packet(), completedBody(), {
    credentials: delayedCredentials,
    sessionReader: {
      async get() { return { data: { id: "session-other" }, response: new Response("{}", { status: 200 }) } },
      async messages() { return { data: [], response: new Response("[]", { status: 200 }) } },
    },
    evidenceRunner: evidenceCollector(refused), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY,
  }, SIGNAL)
  expect(refusal.error?.kind).toBe("readback_refusal")
  expect(refused.map((entry) => entry.command)).toEqual(["worker-dispatch"])
  const failedRequest = refused[0].request as Record<string, any>
  expect(failedRequest.terminal).toBe("failed")
  expect(Date.parse(failedRequest.assertion.issued_at)).toBeGreaterThanOrEqual(availableAt[2])
})

// The core refuses an assertion whose issued_at is older than its clock-skew
// window, so the terminal signature must be minted after the dispatch write
// has landed, not beside the dispatch signature. A delayed dispatch write
// therefore cannot stale the completion evidence before its own CLI write.
test("a delayed dispatch write does not stale the terminal assertion", async () => {
  const recorded: { command: string; request: Record<string, any>; landedAt: number }[] = []
  const delayedDispatch: DispatchRunner = {
    async run(argv, input) {
      if (argv[1] === "worker-dispatch") await new Promise((resolve) => setTimeout(resolve, 50))
      recorded.push({ command: argv[1], request: JSON.parse(input), landedAt: Date.now() })
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), {
    credentials: testCredentials, sessionReader: laneSessionReader,
    evidenceRunner: delayedDispatch, concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY,
  }, SIGNAL)
  expect(result.outcome).toBe("ok")
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-complete"])
  expect(Date.parse(recorded[1].request.assertion.issued_at)).toBeGreaterThanOrEqual(recorded[0].landedAt)
})

// The CLI refuses a request that omits a required top-level field, and the
// stubbed evidence runner cannot see that refusal. The shared file names the
// fields each verb requires, cmd/concord holds it to commandSpecs, and this
// test holds every request the adapter builds to it (issue #789).
const requiredFieldsFor = (verb: string): string[] => (workerCLIRequiredFields.verbs as Record<string, string[]>)[verb]

test("every worker evidence request carries the fields its CLI verb requires", async () => {
  const recorded: Record<string, unknown>[] = []
  await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  const failed: Record<string, unknown>[] = []
  await completeWorkerAttempt(lane, packet(), failedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(failed), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  const all = [...recorded, ...failed]
  expect(all.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-complete", "worker-dispatch", "worker-fail"])
  for (const entry of all) {
    const request = entry.request as Record<string, unknown>
    for (const field of requiredFieldsFor(entry.command as string)) {
      expect(request, `${entry.command} request lacks ${field}`).toHaveProperty(field)
    }
  }
  expect((recorded[0].request as any).packet_digest).toBe(PACKET_DIGEST)
})

test("each evidence write carries its own nonce", async () => {
  const recorded: Record<string, unknown>[] = []
  await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
      if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "session-1", directory: "/claimed/worktree" }]), stderr: "" }
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  const result = await completeWorkerAttempt(lane, packet(), failedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  for (const [runner, body] of [[laneRunner, completedBody()], [failingLaneRunner, failedBody()]] as const) {
    const recorded: Record<string, unknown>[] = []
    await completeWorkerAttempt(lane, packet(), body, { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: broken, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
test("completion refuses to sign without packetDigest", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toContain("packet digest")
  expect(recorded).toHaveLength(0)
})
