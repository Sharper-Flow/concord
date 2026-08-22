import { test, expect } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { canonicalWorkerEvidence, dispatchWorker, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import workerEvidenceVector from "./worker-evidence-vector.json"

const lane = agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

function packet(): AgentLanePacket {
  return {
    schema_version: "1.0", attempt_id: "attempt-1", lane_id: lane.id, lane_version: lane.version,
    lane_digest: lane.digest, work_id: "work-1", step_id: "step-1", inputs: { task: "bounded task" },
  }
}

const runOutput = () => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
].join("\n")

const exportedSession = (model = READBACK_MODEL) => JSON.stringify({
  info: { id: "session-1" },
  messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] }],
})

const laneRunner: DispatchRunner = {
  async run(argv) {
    if (argv[1] === "run") return { exitCode: 0, stdout: runOutput(), stderr: "" }
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
test("the TypeScript canonical encoder matches the Go vector byte for byte", () => {
  const encoded = canonicalWorkerEvidence(workerEvidenceVector as Record<string, unknown>)
  expect(Buffer.from(encoded).toString("base64")).toBe(workerEvidenceVector.canonical_base64)
})

test("canonical encoding is order-fixed, not object-key dependent", () => {
  const reversed = Object.fromEntries(Object.entries(workerEvidenceVector).reverse())
  expect(Buffer.from(canonicalWorkerEvidence(reversed)).toString("base64")).toBe(workerEvidenceVector.canonical_base64)
})

test("dispatch and completion evidence each carry a bound assertion", async () => {
  const recorded: Record<string, unknown>[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: laneRunner, evidenceRunner: evidenceCollector(recorded) })
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
  await dispatchWorker(packet(), { credentials: testCredentials, runner: laneRunner, evidenceRunner: evidenceCollector(recorded) })
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
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner, evidenceRunner: evidenceCollector(recorded) })
  expect(result.outcome).toBe("ok")
  expect(spawnedArgv.join(" ")).not.toContain("signature")
  expect(spawnedArgv.join(" ")).not.toContain("assertion")
  expect(JSON.stringify(result)).not.toContain("signature")
})

// Without a credential the adapter cannot authorize evidence. A run that cannot
// be recorded is never reported as a success.
test("an unavailable credential fails the run instead of recording unsigned evidence", async () => {
  const recorded: Record<string, unknown>[] = []
  const broken: CredentialStore = { async getPrivateKey() { throw new Error("credential service unavailable") } }
  const result = await dispatchWorker(packet(), { credentials: broken, runner: laneRunner, evidenceRunner: evidenceCollector(recorded) })
  expect(result.outcome).toBe("error")
  expect(result.error?.recovery_action).toBe("contact_operator")
  expect(recorded).toHaveLength(0)
})
