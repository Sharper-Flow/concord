import { test, expect } from "bun:test"
import { agentLanes, routingPolicies, routingPolicyManifestDigest, routingPolicyVersion } from "./generated-agent-lanes"
import { dispatchWorker, readSessionMetadata, validateAgentLanePacket, type AgentLanePacket } from "./dispatch"

const lane = agentLanes[0]
const packet = (): AgentLanePacket => ({
  schema_version: "1.0",
  attempt_id: "attempt-1",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  work_id: "work-1",
  step_id: "step-1",
  inputs: { task: "Run the bounded worker fixture." },
})

const metadata = (model = lane.pinned_model) => JSON.stringify({ type: "step_start", properties: { sessionID: "session-1", part: { model: { providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/") } } } })

test("generated adapter routing policy digest and preferred cross-validation are deterministic", async () => {
  const digest = (await Bun.file(new URL("../../contracts/routing-policy.digest", import.meta.url)).text()).trim()
  expect(digest).toBe(routingPolicyManifestDigest)
  expect(routingPolicyVersion).toBe("routing-v1")
  for (const policy of routingPolicies) {
    expect(policy.resolution_set[0]).toBe(policy.preferred_model)
    expect(policy.resolution_set.length).toBeGreaterThanOrEqual(1)
  }
})

test("packet validation is closed before any runner call", async () => {
  let calls = 0
  const invalid = { ...packet(), inputs: { task: "" } }
  expect(validateAgentLanePacket(invalid)).toBe(false)
  const result = await dispatchWorker(invalid, { runner: { async run() { calls++; return { exitCode: 0, stdout: metadata(), stderr: "" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(calls).toBe(0)
})

test("unknown lane identity fails closed before spawn", async () => {
  const unknown = { ...packet(), lane_id: "unknown" }
  const result = await dispatchWorker(unknown, { runner: { async run() { throw new Error("must not spawn") } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
})

test("spawn failure is a typed blocked outcome with bounded diagnostic", async () => {
  let argv: string[] = []
  const result = await dispatchWorker(packet(), { binary: "opencode-test", runner: { async run(args) { argv = args; return { exitCode: 1, stdout: "", stderr: "provider unavailable", fallbackExhausted: true } } } })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.kind).toBe("blocked")
  expect(argv).toEqual(["opencode-test", "run", "--agent", "concord-research", "--model", lane.pinned_model, "--format", "json", JSON.stringify(packet())])
})

test("spawn failure without exhaustion evidence is not mislabeled blocked", async () => {
  const result = await dispatchWorker(packet(), { runner: { async run() { return { exitCode: 1, stdout: "", stderr: "provider unavailable" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
})

test("recorded session metadata proves the executing model and exposes fallback", async () => {
  const fallback = "zai-coding-plan/glm-5.2"
  const fallbackOutput = `${metadata(lane.pinned_model)}\n${JSON.stringify({ type: "message.updated", properties: { sessionId: "session-1", message: { model: { providerID: "zai-coding-plan", modelID: "glm-5.2" } }, status: { action: { reason: "account_rate_limit" } } } })}`
  const readback = readSessionMetadata(fallbackOutput)
  expect(readback).toEqual({ readback_model: fallback, session_id: "session-1", fallback_reason: "rate_limit" })
  const result = await dispatchWorker(packet(), { runner: { async run() { return { exitCode: 0, stdout: fallbackOutput, stderr: "" } } } })
  expect(result.outcome).toBe("fallback")
  expect(result.readback_model).toBe(fallback)
  expect(result.error?.kind).toBe("fallback")
  expect(result.resolution_role).toBe("fallback")
  expect(result.fallback_reason).toBe("rate_limit")
})

test("matching recorded session metadata returns bounded ok envelope", async () => {
  const result = await dispatchWorker(packet(), { runner: { async run() { return { exitCode: 0, stdout: metadata(), stderr: "" } } } })
  expect(result.outcome).toBe("ok")
  expect(result.agent).toBe("concord-research")
  expect(result.resolved_model).toBe(lane.pinned_model)
  expect(result.resolution_role).toBe("preferred")
  expect(result.fallback_reason).toBe("")
  expect(result.readback_model).toBe(lane.pinned_model)
  expect(result.session_id).toBe("session-1")
})

test("undeclared readback is rejected instead of becoming an implicit fallback", async () => {
  const result = await dispatchWorker(packet(), { runner: { async run() { return { exitCode: 0, stdout: metadata("openai/not-declared"), stderr: "" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("model_identity_mismatch")
})
