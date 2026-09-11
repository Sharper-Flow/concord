import { afterEach, beforeEach, describe, expect, test } from "bun:test"
import { mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

import * as adapter from "./concord"
import { contractOperations, manifestDigest } from "./generated-contracts"
import { activeManifestDigest, adoptManifestDigest, resetManifestPinForTesting, resolveDiskManifestDigest, setManifestSourceForTesting } from "./manifest-pin"
import { configureCoreBinary } from "./dispatch"
import { hostControlPlane } from "./move-session"

// Fake-runner suite: bind the transport to a nominal core path instead of the
// unstamped repository placeholder (CD-0111 D1). Each file sets this itself,
// because bun runs the suite's files in one process in an order no file
// controls.
configureCoreBinary("concord")

const hostCall = (operation: string, input: Record<string, unknown>) => ({ request: { operation, input } })
const contextFor = (): any => ({ sessionID: "session-1", messageID: "message-1", agent: "agent-1", worktree: "/worktree", directory: "/worktree", abort: new AbortController().signal, ask: async () => {} })
const contextResponse = () => ({ project_id: "project-1", product_ids: ["product-1"], scope_version: "1", main_worktree: true })
const coreEnvelope = (tool: string, operation: string, outcome: string, fields: Record<string, unknown> = {}) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: "session-1-message-1", origin: "core", tool, operation, ...((contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any)?.query_id ? { query_id: (contractOperations.find((candidate: any) => candidate.tool === tool && candidate.id.endsWith(`.${operation}`)) as any).query_id } : {}), outcome, resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})
const foreignDigest = "sha256:" + "0".repeat(63) + "1"

beforeEach(() => {
  resetManifestPinForTesting()
  hostControlPlane().bind({
    get: async () => ({ data: { id: "session-1", directory: "/worktree" }, response: new Response(null, { status: 200 }) }),
    post: async () => ({ response: new Response(null, { status: 204 }) }),
  })
})
afterEach(() => { adapter.configureConcordAdapter({ reset: true }); resetManifestPinForTesting(); setManifestSourceForTesting(null) })

describe("the manifest pin", () => {
  test("resolves the digest the installed files declare", () => {
    const dir = mkdtempSync(join(tmpdir(), "manifest-pin-"))
    const path = join(dir, "generated-contracts.ts")
    writeFileSync(path, `export const manifestDigest = "${foreignDigest}" as const\n`)
    setManifestSourceForTesting(path)
    expect(resolveDiskManifestDigest()).toBe(foreignDigest)
  })

  test("resolves null for an unreadable source rather than a guess", () => {
    setManifestSourceForTesting(join(tmpdir(), "manifest-pin-absent", "generated-contracts.ts"))
    expect(resolveDiskManifestDigest()).toBeNull()
  })

  test("adoption requires a well-formed digest and takes effect", () => {
    expect(activeManifestDigest()).toBe(manifestDigest)
    expect(adoptManifestDigest("not-a-digest")).toBe(false)
    expect(adoptManifestDigest(foreignDigest)).toBe(true)
    expect(activeManifestDigest()).toBe(foreignDigest)
  })
})

describe("version-skew self-heal", () => {
  test("a stale pin whose disk files match the core adopts and retries once", async () => {
    const dir = mkdtempSync(join(tmpdir(), "manifest-pin-"))
    writeFileSync(join(dir, "generated-contracts.ts"), `export const manifestDigest = "${foreignDigest}" as const\n`)
    setManifestSourceForTesting(join(dir, "generated-contracts.ts"))

    const invokeInputs: any[] = []
    let invokeCalls = 0
    adapter.configureConcordAdapter({ runner: { async run(_argv: string[], input: string) {
      invokeCalls++
      if (invokeCalls === 1) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      invokeInputs.push(JSON.parse(input))
      return { exitCode: 0, stdout: JSON.stringify({ ...coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } }), manifest_digest: foreignDigest }), stderr: "" }
    } } })

    const raw = await adapter.product_view.execute(hostCall("resolve", { product_id: "product-1" }), contextFor())
    const result: any = typeof raw === "string" ? JSON.parse(raw) : JSON.parse((raw as any).output)
    expect(invokeCalls).toBe(3) // context, skewed invoke, healed retry
    expect(result.outcome).toBe("ok")
    expect(result.manifest_digest).toBe(foreignDigest)
    expect(invokeInputs[0].call_envelope.manifest_digest).toBe(manifestDigest)
    expect(invokeInputs[1].call_envelope.manifest_digest).toBe(foreignDigest)
  })

  test("a pin whose disk files disagree with the core still refuses typed", async () => {
    const dir = mkdtempSync(join(tmpdir(), "manifest-pin-"))
    const otherDigest = "sha256:" + "2".repeat(64)
    writeFileSync(join(dir, "generated-contracts.ts"), `export const manifestDigest = "${otherDigest}" as const\n`)
    setManifestSourceForTesting(join(dir, "generated-contracts.ts"))

    adapter.configureConcordAdapter({ runner: { async run(_argv: string[], input: string) {
      const parsed = JSON.parse(input)
      if (parsed.directory !== undefined) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      return { exitCode: 0, stdout: JSON.stringify({ ...coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } }), manifest_digest: foreignDigest }), stderr: "" }
    } } })

    const raw = await adapter.product_view.execute(hostCall("resolve", { product_id: "product-1" }), contextFor())
    const result: any = typeof raw === "string" ? JSON.parse(raw) : JSON.parse((raw as any).output)
    expect(result.outcome).toBe("error")
    expect(result.error.kind).toBe("transport_failure")
    expect(result.error.adapter_reason).toBe("manifest_mismatch")
    expect(result.error.message).toContain(otherDigest)
    // CD-0111 D4: the refusal names the operator and both digests, never a
    // session restart.
    expect(result.error.message).toContain("contact the operator with both digests")
    expect(result.error.message).not.toContain("restart")
  })

  test("a healed retry that skews again refuses rather than looping", async () => {
    const dir = mkdtempSync(join(tmpdir(), "manifest-pin-"))
    writeFileSync(join(dir, "generated-contracts.ts"), `export const manifestDigest = "${foreignDigest}" as const\n`)
    setManifestSourceForTesting(join(dir, "generated-contracts.ts"))
    const churnedDigest = "sha256:" + "3".repeat(64)

    let invokeCalls = 0
    adapter.configureConcordAdapter({ runner: { async run(_argv: string[], input: string) {
      invokeCalls++
      const parsed = JSON.parse(input)
      if (parsed.directory !== undefined) return { exitCode: 0, stdout: JSON.stringify(contextResponse()), stderr: "" }
      return { exitCode: 0, stdout: JSON.stringify({ ...coreEnvelope("concord_product_view", "resolve", "ok", { result: { product_id: "product-1", projects: [], stage: "prototype" } }), manifest_digest: invokeCalls === 2 ? foreignDigest : churnedDigest }), stderr: "" }
    } } })

    const raw = await adapter.product_view.execute(hostCall("resolve", { product_id: "product-1" }), contextFor())
    const result: any = typeof raw === "string" ? JSON.parse(raw) : JSON.parse((raw as any).output)
    expect(invokeCalls).toBe(3) // exactly one retry
    expect(result.outcome).toBe("error")
    expect(result.error.adapter_reason).toBe("manifest_mismatch")
  })
})
