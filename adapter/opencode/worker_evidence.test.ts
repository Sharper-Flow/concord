import { test, expect } from "bun:test"
import { createHash } from "node:crypto"
import { agentLanes } from "./generated-agent-lanes"
import { canonicalObservationJSON, canonicalWorkerEvidence, workerFailureObservationDigest, abandonWorkerAttempt, completeWorkerAttempt, type AgentLanePacket, type DispatchRunner, type WorkerFailureObservation } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { hostControlPlane, type SessionReader } from "./move-session"
import workerEvidenceVector from "./worker-evidence-vector.json"
import workerCLIRequiredFields from "./worker-cli-required-fields.json"

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
// CD-0067 D6: the completion options carry packetDigest so the adapter
// can quote it on the signed dispatch assertion. Every completion test
// passes this constant; the missing-digest refusal test omits it.
const PACKET_DIGEST = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
const WORKER_DIRECTORY = process.cwd()
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }


function packet(): AgentLanePacket {
  return {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id,
    lane_digest: lane.digest, work_id: "work-1", step_id: "step-1", inputs: { task: "bounded task", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "bounded_findings" } },
  }
}

// A completion is only reachable when the worker returns an admissible
// agent-lane-report.v1 report (CD-0056 D7), so the fixture stream carries one.
// The dispatch window owns attempt and lane identity.
const laneReport = () => ({
  schema_version: "1.0", readback_model: READBACK_MODEL, status: "completed",
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
})

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
    expect(result.envelope.error?.message).toContain("the host observed that the lane never reported")
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

// The committed fact is the CLI success result itself: the store recorded the
// closure. Nothing infers commitment from retry flags or a missing effect
// state, so the fact is explicit on both arms.
test("abandonWorkerAttempt returns the explicit committed fact from the CLI result", async () => {
  hostControlPlane().bind({
    get: async () => ({ response: new Response("", { status: 200 }), data: [{ id: "ses_other", directory: "/other/worktree" }] }),
    post: async () => ({ response: new Response("", { status: 204 }) }),
  })
  try {
    const recorded: DispatchRunner = { async run() { return { exitCode: 0, stdout: "", stderr: "" } } }
    const closed = await abandonWorkerAttempt(lane, packet(), "the host observed that the lane never reported", { runner: recorded, concordBinary: "concord", credentials: testCredentials }, new AbortController().signal)
    expect(closed.committed).toBe(true)
    const refusing: DispatchRunner = { async run() {
      return { exitCode: 1, stdout: JSON.stringify({ ok: false, event_ids: [], error: { kind: "operation_conflict", operation: "worker_evidence", retry_safe: false, effect_state: "none", message: "store: worker_fail: the attempt cannot close" } }), stderr: "" }
    } }
    const refused = await abandonWorkerAttempt(lane, packet(), "the host observed that the lane never reported", { runner: refusing, concordBinary: "concord", credentials: testCredentials }, new AbortController().signal)
    expect(refused.committed).toBe(false)
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

// The readback reads the worker session in process through the host session
// API, so the fixture answers the worker transcript there.
const laneSessionReader: SessionReader = {
  async get() { return { data: { id: "session-1" }, response: new Response("{}", { status: 200 }) } },
  async messages(_sessionID, _limit, before) {
    if (before) return { data: [], response: new Response("[]", { status: 200 }) }
    return { data: JSON.parse(exportedSession()).messages, response: new Response("[]", { status: 200 }) }
  },
}

// CD-0102 D5: the host runs the worker, so completion receives the host's task
// result, and the evidence runner records only the core CLI evidence writes.
// A worker that reports its own failure is the reachable worker-fail path
// (CD-0056 D7): the report is admissible, and its status is the failure.
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
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  const failed: Record<string, unknown>[] = []
  await completeWorkerAttempt(lane, packet(), failedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(failed), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  const nonces = recorded.map((entry) => (entry.request as any).assertion.nonce)
  expect(new Set(nonces).size).toBe(nonces.length)
})

// The worker never sees the proof that authorizes its own evidence. If it did,
// a lane run could forge evidence for a later attempt. Completion signs both
// assertions, so the packet the worker received must stay unchanged and the
// result returned to the coordinator must carry no signature.
test("the signing proof never reaches the worker packet or the completion result", async () => {
  const recorded: Record<string, unknown>[] = []
  const workerPacket = packet()
  const issued = JSON.stringify(workerPacket)
  const result = await completeWorkerAttempt(lane, workerPacket, completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  expect(result.outcome).toBe("ok")
  expect(recorded.map((entry) => typeof (entry.request as any).assertion.signature)).toEqual(["string", "string"])
  expect(JSON.stringify(workerPacket)).toBe(issued)
  expect(JSON.stringify(result)).not.toContain("signature")
})

// A failure is evidence about the same attempt as its dispatch, so it claims
// the same identity. The CLI enriches lane identity from the stored attempt row
// for both terminal verbs (cmd/concord/main.go applyWorkerEvidence), so an
// assertion that leaves those fields empty cannot match the binding.
test("failure evidence carries a bound assertion including lane identity", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await completeWorkerAttempt(lane, packet(), failedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  expect(result.outcome).toBe("error")
  expect(recorded.map((entry) => entry.command)).toEqual(["worker-dispatch", "worker-fail"])

  const assertion = (recorded[1].request as any).assertion
  expect(assertion.verb).toBe("worker-fail")
  expect(assertion.work_id).toBe("work-1")
  expect(assertion.attempt_id).toBe("attempt-1")
  expect(assertion.lane_id).toBe(lane.id)
  expect(assertion).not.toHaveProperty("lane_version")
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
  const requests = new Map<string, Record<string, unknown>>()
  for (const body of [completedBody(), failedBody()]) {
    const recorded: Record<string, unknown>[] = []
    await completeWorkerAttempt(lane, packet(), body, { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
    for (const entry of recorded) {
      signed.set(entry.command as string, (entry.request as any).assertion)
      requests.set(entry.command as string, entry.request as Record<string, unknown>)
    }
  }
  expect([...signed.keys()].sort()).toEqual(workerEvidenceVector.cases.map((vectorCase) => vectorCase.verb).sort())

  // a worker-fail assertion signs the canonical observation digest of
  // the attribution its request carries, so the signed field set gains the v2
  // members exactly on the failing verbs. Dispatch and completion stay v1.
  const V2_ATTRIBUTION_FIELDS = ["version", "observation_digest"]
  for (const vectorCase of workerEvidenceVector.cases) {
    const assertion = signed.get(vectorCase.verb)
    expect(assertion).toBeDefined()
    const attributionFields = vectorCase.verb === "worker-fail" ? V2_ATTRIBUTION_FIELDS : []
    const want = [...vectorCase.bound_fields, ...attributionFields, ...SIGNING_ENVELOPE_FIELDS].sort()
    expect(Object.keys(assertion as Record<string, unknown>).sort()).toEqual(want)
  }
  const failAssertion = signed.get("worker-fail") as Record<string, unknown>
  expect(failAssertion.version).toBe(2)
  expect(failAssertion.observation_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
  expect(typeof (requests.get("worker-fail") as Record<string, unknown>).failure_attribution).toBe("object")
  const completedAssertion = signed.get("worker-complete") as Record<string, unknown>
  expect(completedAssertion.version).toBeUndefined()
  expect(completedAssertion.observation_digest).toBeUndefined()
})

// Without a credential the adapter cannot authorize evidence. A run that cannot
// be recorded is never reported as a success.
test("an unavailable credential fails the run instead of recording unsigned evidence", async () => {
  const recorded: Record<string, unknown>[] = []
  const broken: CredentialStore = { async getPrivateKey() { throw new Error("credential service unavailable") } }
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: broken, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
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
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  expect(result.outcome).toBe("ok")
  const dispatched = (recorded[0].request as any).assertion
  expect(dispatched.packet_digest).toBe(PACKET_DIGEST)
})

// CD-0067 D6: completion refuses to sign without the digest the
// core recorded. The error is invalid_input — the same kind the
// packet validator returns — so a worker that did not see the digest
// reports the omission the same way it would report any other
// malformed input. The run already happened on the worker side; the
// envelope is the failure surface.
test("completion refuses to sign without packetDigest", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: evidenceCollector(recorded), concordBinary: "concord", workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toContain("packet digest")
  expect(recorded).toHaveLength(0)
})

// Vector structure is not production signer parity or observation authenticity.
type WorkerEvidenceVectorV2Case = {
  verb: string
  bound_fields: string[]
  canonical_base64: string
  assertion: { version: number; observation_digest: string; verb: string } & Record<string, unknown>
}

const v2VectorCases = (workerEvidenceVector as { v2_cases: WorkerEvidenceVectorV2Case[] }).v2_cases
const V2_PREFIX = Buffer.from("worker-evidence-v2\0", "binary")

// production signer parity. The same canonical encoder the
// adapter signs with must reproduce every v2 vector byte for byte, exactly as
// the v1 loop above pins the released format — a structure witness alone
// cannot prove the production signer agrees with the Go encoder.
for (const vectorCase of v2VectorCases) {
  test(`the TypeScript canonical encoder matches the Go v2 vector byte for byte for ${vectorCase.verb}`, () => {
    const encoded = canonicalWorkerEvidence(vectorCase.assertion as Record<string, unknown>)
    expect(Buffer.from(encoded).toString("base64")).toBe(vectorCase.canonical_base64)
  })
}

test("v2 canonical encoding is order-fixed, not object-key dependent", () => {
  const vectorCase = v2VectorCases[0]
  const reversed = Object.fromEntries(Object.entries(vectorCase.assertion).reverse())
  expect(Buffer.from(canonicalWorkerEvidence(reversed)).toString("base64")).toBe(vectorCase.canonical_base64)
})

// The observation digest hashes exactly the bytes Go's json.Marshal produces
// for the same observation: fixed member order, omitempty members that
// vanish when empty, and Go's default HTML escaping. The canonical string is
// pinned literally here, so an escaping drift fails this test before it can
// forge a signature the core would refuse.
test("the observation digest canonicalizes exactly the Go marshal form", () => {
  const observation: WorkerFailureObservation = {
    observer: "host_adapter",
    phase: "host_execution",
    cause: "host_provider",
    basis: "supported",
    sources: [
      {
        kind: "host_event",
        locator: "msg-1",
        digest: "sha256:" + "a".repeat(64),
        message: 'fault <&> "quoted"\n\té🎉',
        host: { tag: "terminal", event_type: "message.updated", session_id: "ses_1", message_id: "msg-1", error_name: "APIError", status_code: 429 },
      },
      { kind: "validator", message: "schema: <probe>" },
    ],
  }
  expect(canonicalObservationJSON(observation)).toBe(
    '{"observer":"host_adapter","phase":"host_execution","cause":"host_provider","basis":"supported","sources":[' +
    '{"kind":"host_event","locator":"msg-1","digest":"sha256:' + "a".repeat(64) +
    '","message":"fault \\u003c\\u0026\\u003e \\"quoted\\"\\n\\té🎉",' +
    '"host":{"tag":"terminal","event_type":"message.updated","session_id":"ses_1","message_id":"msg-1","error_name":"APIError","status_code":429}},' +
    '{"kind":"validator","message":"schema: \\u003cprobe\\u003e"}]}',
  )
  expect(workerFailureObservationDigest(observation)).toBe(
    "sha256:" + createHash("sha256").update(canonicalObservationJSON(observation), "utf8").digest("hex"),
  )
})

// Observation-digest parity. The observation_vectors samples carry exact
// observations — Go's HTML escaping of < > &, raw U+2028/U+2029 line
// separators, multibyte text, and observations whose optional members are
// omitted — beside the digest the Go production helper
// (store.WorkerFailureObservationDigest) computed when the fixture was
// written. The TypeScript production digest helper must reproduce each one
// exactly: a divergence here would sign an assertion the core refuses, or
// worse, authenticate an observation the client never saw.
type ObservationVectorCase = {
  name: string
  observation: WorkerFailureObservation
  observation_digest: string
}

const observationVectors = (workerEvidenceVector as { observation_vectors?: ObservationVectorCase[] }).observation_vectors ?? []

test("the TypeScript production observation digest matches every Go vector digest", () => {
  expect(observationVectors.length).toBeGreaterThan(0)
  for (const vectorCase of observationVectors) {
    expect(workerFailureObservationDigest(vectorCase.observation), `observation digest mismatch for ${vectorCase.name}`).toBe(vectorCase.observation_digest)
  }
})

test("v2 vector cases declare well-formed worker-evidence-v2 vector structure", () => {
  expect(Array.isArray(v2VectorCases)).toBe(true)
  expect(v2VectorCases.length).toBeGreaterThan(0)
  const wantOrder = [
    "client_ref", "verb", "work_id", "attempt_id", "lane_id",
    "lane_digest", "readback_model", "failure_kind", "host_provenance_digest",
    "packet_digest", "observation_digest", "issued_at", "nonce",
  ]
  for (const vectorCase of v2VectorCases) {
    const raw = Buffer.from(vectorCase.canonical_base64, "base64")
    expect(Buffer.compare(raw.subarray(0, V2_PREFIX.length), V2_PREFIX)).toBe(0)
    const segments = raw.subarray(V2_PREFIX.length).toString("binary").split("|").filter((segment) => segment.length > 0)
    const names = segments.map((segment) => segment.slice(0, segment.indexOf("=")))
    expect(names).toEqual(wantOrder)
    const signed: Record<string, string> = {}
    for (const segment of segments) {
      const name = segment.slice(0, segment.indexOf("="))
      const declared = Number(segment.slice(segment.indexOf("=") + 1, segment.indexOf(":")))
      const value = segment.slice(segment.indexOf(":") + 1)
      expect(Buffer.byteLength(value, "utf8")).toBe(declared)
      signed[name] = value
    }
    expect(signed["observation_digest"]).toMatch(/^sha256:[0-9a-f]{64}$/)
    expect(vectorCase.assertion.version).toBe(2)
    expect(vectorCase.assertion.observation_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    expect(vectorCase.assertion.observation_digest).toBe(signed["observation_digest"])
    expect(vectorCase.assertion.verb).toBe(vectorCase.verb)
    expect(vectorCase.bound_fields).toContain("version")
    expect(vectorCase.bound_fields).toContain("observation_digest")
  }
})

// A typed Git observation refusal from the
// core's terminal ingest reaches the coordinator as one typed, retry-safe,
// zero-effect failure. The adapter surfaces it and stops: it never retries
// the write in a loop and never takes the invalid_report close path, so the
// attempt stays recoverable through the retained-report recovery route.
test("a typed git observation refusal is surfaced once and never retried", async () => {
  const calls: { command: string; request: Record<string, unknown> }[] = []
  const refusingRunner: DispatchRunner = {
    async run(argv, input) {
      calls.push({ command: argv[1]!, request: JSON.parse(input) as Record<string, unknown> })
      if (argv[1] === "worker-dispatch") return { exitCode: 0, stdout: "", stderr: "" }
      return {
        exitCode: 1,
        stdout: JSON.stringify({
          ok: false,
          event_ids: [],
          error: {
            kind: "git_unreachable",
            operation: "worker_terminal_observation",
            retry_safe: true,
            effect_state: "none",
            message: "store: worker_terminal_observation: git_unreachable: cannot observe the attempt's clean status",
          },
        }),
        stderr: "",
      }
    },
  }
  const result = await completeWorkerAttempt(lane, packet(), completedBody(), { credentials: testCredentials, sessionReader: laneSessionReader, evidenceRunner: refusingRunner, concordBinary: "concord", packetDigest: PACKET_DIGEST, workerDirectory: WORKER_DIRECTORY }, SIGNAL)
  expect(result.outcome).toBe("error")
  expect(result.error?.message).toContain("cannot observe the attempt's clean status")
  expect(result.error?.retry_safe).toBe(true)
  expect((result.error as any)?.details?.effect_state).toBe("none")
  expect((result.error as any)?.details?.operation).toBe("worker_terminal_observation")
  expect((result.error as any)?.details?.core_kind).toBe("git_unreachable")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(calls.map((call) => call.command)).toEqual(["worker-dispatch", "worker-complete"])
})
