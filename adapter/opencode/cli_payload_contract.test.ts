// The adapter writes worker evidence by running `concord <verb>` with a JSON
// payload on stdin. The CLI's required fields live in commandSpecs and are
// projected into contracts/cli-commands.v1.json by a golden test beside them.
// Nothing compared the two, so a payload missing a required field was found
// only by a live run — after the worker had finished, at the evidence write,
// with the attempt already unrecoverable for that dispatch.
//
// These tests drive the real completion path with a capturing runner and check
// every payload it emits against the contract. A verb the adapter calls with a
// field missing fails here rather than in a session.
import { expect, test } from "bun:test"
import { readFileSync } from "node:fs"
import { agentLanes } from "./generated-agent-lanes"
import { completeWorkerAttempt, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"

interface CommandContractField {
  name: string
  nested?: string[]
}

interface CommandContract {
  schema_version: string
  commands: { canonical: string; two_word?: string; required_fields: CommandContractField[] }[]
}

const contract = JSON.parse(readFileSync(new URL("../../contracts/cli-commands.v1.json", import.meta.url), "utf8")) as CommandContract

const requiredFieldsFor = (canonical: string): CommandContractField[] => {
  const command = contract.commands.find((candidate) => candidate.canonical === canonical)
  if (!command) throw new Error(`the command contract does not carry ${canonical}`)
  return command.required_fields
}

const lane = agentLanes.find((candidate) => candidate.id === "verify") ?? agentLanes[0]
const READBACK_MODEL = "openai/gpt-5.6-luna"
const PACKET_DIGEST = "sha256:" + "d".repeat(64)
const SIGNAL = new AbortController().signal
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

const packet = (): AgentLanePacket => ({
  schema_version: "1.0", attempt_id: "attempt-contract", lane_id: lane.id, lane_version: lane.version,
  lane_digest: lane.digest, work_id: "work-contract", step_id: "repair", inputs: { task: "bounded task" },
})

const report = (status = "completed") => ({
  schema_version: "1.0", attempt_id: "attempt-contract", lane_id: lane.id, lane_version: lane.version,
  lane_digest: lane.digest, readback_model: READBACK_MODEL, status,
  evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `${obligation} discharged` })),
})

const exportedSession = JSON.stringify({
  info: { id: "ses_worker" },
  messages: [{ info: { id: "message-1", sessionID: "ses_worker", role: "assistant", agent: `concord-${lane.id}`, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] }],
})

const taskWrap = (text: string) =>
  ['<task id="ses_worker" state="completed">', "<task_result>", text, "</task_result>", "</task>"].join("\n")

// One runner answers the session export and captures every evidence payload.
const capturingRunner = (captured: { verb: string; payload: Record<string, unknown> }[]): DispatchRunner => ({
  async run(argv, input) {
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession, stderr: "" }
    captured.push({ verb: argv[1], payload: JSON.parse(input) })
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

function assertSatisfiesContract(verb: string, payload: Record<string, unknown>): void {
  for (const field of requiredFieldsFor(verb)) {
    expect(payload[field.name], `${verb} payload omits required field ${field.name}`).toBeDefined()
    for (const nested of field.nested ?? []) {
      const parent = payload[field.name]
      expect(parent !== null && typeof parent === "object", `${verb} field ${field.name} must be an object`).toBe(true)
      expect((parent as Record<string, unknown>)[nested], `${verb} payload omits required field ${field.name}.${nested}`).toBeDefined()
    }
  }
}

test("a completed attempt writes worker-dispatch and worker-complete payloads the CLI accepts", async () => {
  const captured: { verb: string; payload: Record<string, unknown> }[] = []
  const result = await completeWorkerAttempt(lane, packet(), taskWrap(JSON.stringify(report())), {
    credentials: testCredentials, runner: capturingRunner(captured), packetDigest: PACKET_DIGEST,
  }, SIGNAL)
  expect(result.outcome).toBe("ok")
  expect(captured.map((entry) => entry.verb)).toEqual(["worker-dispatch", "worker-complete"])
  for (const entry of captured) assertSatisfiesContract(entry.verb, entry.payload)
})

test("a failed attempt writes a worker-fail payload the CLI accepts", async () => {
  const captured: { verb: string; payload: Record<string, unknown> }[] = []
  await completeWorkerAttempt(lane, packet(), taskWrap(JSON.stringify(report("failed"))), {
    credentials: testCredentials, runner: capturingRunner(captured), packetDigest: PACKET_DIGEST,
  }, SIGNAL)
  expect(captured.map((entry) => entry.verb)).toEqual(["worker-dispatch", "worker-fail"])
  for (const entry of captured) assertSatisfiesContract(entry.verb, entry.payload)
})

// The digest the core recorded is what binds the evidence to the authorized
// packet. Carrying it only inside the signed assertion left the CLI's own
// required field empty, which is the omission this file exists to catch.
test("the dispatch payload carries the packet digest the authorization recorded", async () => {
  const captured: { verb: string; payload: Record<string, unknown> }[] = []
  await completeWorkerAttempt(lane, packet(), taskWrap(JSON.stringify(report())), {
    credentials: testCredentials, runner: capturingRunner(captured), packetDigest: PACKET_DIGEST,
  }, SIGNAL)
  expect(captured[0].payload.packet_digest).toBe(PACKET_DIGEST)
})

// Every verb the adapter can call must be one the contract carries. A verb the
// CLI dropped or renamed would otherwise fail only when a worker finishes.
test("every evidence verb the adapter emits exists in the command contract", async () => {
  const captured: { verb: string; payload: Record<string, unknown> }[] = []
  await completeWorkerAttempt(lane, packet(), taskWrap(JSON.stringify(report())), {
    credentials: testCredentials, runner: capturingRunner(captured), packetDigest: PACKET_DIGEST,
  }, SIGNAL)
  await completeWorkerAttempt(lane, packet(), taskWrap(JSON.stringify(report("failed"))), {
    credentials: testCredentials, runner: capturingRunner(captured), packetDigest: PACKET_DIGEST,
  }, SIGNAL)
  const verbs = new Set(captured.map((entry) => entry.verb))
  expect(verbs).toEqual(new Set(["worker-dispatch", "worker-complete", "worker-fail"]))
  for (const verb of verbs) expect(() => requiredFieldsFor(verb)).not.toThrow()
})
