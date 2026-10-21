// Shared helpers for the end-to-end .case.ts suites that drive the real core
// binary against a real store.
import { expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createPrivateKey, createPublicKey, type KeyObject } from "node:crypto"
import { runFixtureProcess } from "./fixture-temp-root"
import { WORKER_REPORT_PROTOCOL } from "./worker-report-protocol.js"

export type JSONRecord = Record<string, any>

export function nativeTaskSelector(directive: { output?: string }): JSONRecord {
  expect(directive.output).toContain("Authorized only: dispatch launches no worker")
  const text = directive.output!
  return JSON.parse(text.slice(text.indexOf("{"), text.lastIndexOf("}") + 1))
}

const PRIVATE_KEY_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex")

export function fixturePrivateKey(seed: Uint8Array): KeyObject {
  return createPrivateKey({ key: Buffer.concat([PRIVATE_KEY_PREFIX, Buffer.from(seed)]), format: "der", type: "pkcs8" })
}

export function fixturePublicKeyBase64(seed: Uint8Array): string {
  return createPublicKey(fixturePrivateKey(seed)).export({ format: "der", type: "spki" }).subarray(-32).toString("base64")
}

export async function runProcess(argv: string[], input = "", cwd?: string, env: Record<string, string> = {}): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  return runFixtureProcess(argv, input, { cwd, env: { ...process.env, ...env } })
}

export async function runJSON(binary: string, dbPath: string, command: string, value: JSONRecord, cwd?: string): Promise<JSONRecord> {
  const result = await runProcess([binary, command], JSON.stringify(value), cwd, { CONCORD_DB_PATH: dbPath })
  expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
  const lines = result.stdout.trim().split("\n").filter(Boolean)
  expect(lines).toHaveLength(1)
  return JSON.parse(lines[0]) as JSONRecord
}

export async function git(cwd: string, ...args: string[]): Promise<void> {
  const result = await runProcess(["git", ...args], "", cwd)
  expect(result.exitCode, `git ${args.join(" ")}: ${result.stderr}`).toBe(0)
}

export function dbValue(dbPath: string, sql: string, ...params: string[]): any {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).get(...params)
  } finally {
    db.close()
  }
}

export function dbRows(dbPath: string, sql: string, ...params: string[]): any[] {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).all(...params)
  } finally {
    db.close()
  }
}

export function reportText(report: JSONRecord): string {
  return `\`\`\`${WORKER_REPORT_PROTOCOL}\n${JSON.stringify(report)}\n\`\`\``
}

export function requiredFields(value: JSONRecord, fields: string[]): void {
  for (const field of fields) expect(value[field], `missing ${field}`).toBeDefined()
}

// The route tests drive the real `concord` binary against a real store. A
// clean checkout has no binary on PATH; declare the test skipped when no
// Go toolchain can build one, so a suite run without the
// toolchain reports a skip, never a silent pass. The probe runs on the first
// declaration, so a suite that imports only other helpers spawns no probe.
let goAvailable: boolean | undefined

export function routeDeclaration(name: string, body: () => Promise<void>, timeout: number) {
  goAvailable ??= Bun.spawnSync(["go", "version"], { env: { ...process.env } }).exitCode === 0
  const selected = process.env.TEST_CONCORD_ROUTE_CASE
  return (selected && !new RegExp(selected).test(name) || !goAvailable ? test.skip : test)(name, async () => {
    await body()
    console.log(`[pass] ${name}`)
  }, timeout)
}

// reportWorkerJob copies the packet's worker-job binding onto the report
// claim, as a lane worker does (CD-0205).
export function reportWorkerJob(packet: JSONRecord): JSONRecord {
  const job = (packet.inputs as JSONRecord).worker_job as JSONRecord
  return { job_id: job.job_id, revision: job.revision, digest: job.digest }
}
