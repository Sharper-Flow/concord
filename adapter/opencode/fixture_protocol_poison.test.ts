// Permanent composed regression for the fixture protocol's TEST_CONCORD_*
// inputs. The repository preload (adapter/opencode/test_preload.ts) loads in
// every bun:test child and strips every live CONCORD_* key, so the private
// fixture protocol — run root, nonce, owner bun path, scenario, report, and
// pulse — rides TEST_CONCORD_* keys, the same convention Go test children
// use. This regression drives one REAL nested owned suite (this test ->
// fixture-root-owner.py -> inner `bun test` fixture-owner.case.ts) while the
// environment deliberately carries the old private key names, plus another
// live CONCORD_* key, as poison: any runtime path still reading a CONCORD_*
// name would fail the run. The inner case must receive its TEST_CONCORD_*
// ownership and scenario inputs, record that zero live CONCORD_* keys
// survived the preload in its own process, and the owner must still drain
// the whole writer chain, remove exactly its allocated root, and leave a
// concurrent foreign-run sentinel and a same-prefix sibling byte-identical.
// No model call happens anywhere here.
import { expect, test } from "bun:test"
import { mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const OWNER = join(import.meta.dir, "fixture-root-owner.py")
const CASE_FILE = join(import.meta.dir, "fixture-owner.case.ts")
const PYTHON = (Bun.which("python3", { PATH: "/usr/bin:/bin" }) ?? Bun.which("python3")) as string

// The old private key names live on ONLY here, as deliberate negative
// poison: values that would break the run if any producer or consumer still
// read them. They are not runtime aliases.
const POISON: Record<string, string> = {
  CONCORD_FIXTURE_RUN_ROOT: "/nonexistent/poison-fixture-root",
  CONCORD_FIXTURE_RUN_NONCE: "poison-nonce",
  CONCORD_OWNER_CASE: "poison-case",
  CONCORD_OWNER_REPORT: "/nonexistent/poison-report.json",
  CONCORD_OWNER_PULSE: "/nonexistent/poison-pulse.txt",
  CONCORD_OWNER_BUN: "/nonexistent/poison-bun",
  CONCORD_FUTURE_INPUT: "poison-future",
}

interface OwnerEvent {
  kind?: string
  root?: string
  status?: number
  killed?: number[]
  reaped?: number[]
}

async function pathExists(path: string): Promise<boolean> {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}

async function digestTree(root: string): Promise<string> {
  const names = await readdir(root)
  const parts: string[] = []
  for (const name of names.sort()) parts.push(`${name}:${await readFile(join(root, name), "utf8")}`)
  return parts.join("|")
}

function parseEvents(stderr: string): OwnerEvent[] {
  const events: OwnerEvent[] = []
  for (const line of stderr.split("\n")) {
    if (!line.startsWith("@@concord-owner ")) continue
    try {
      events.push(JSON.parse(line.slice("@@concord-owner ".length)) as OwnerEvent)
    } catch {
      // A malformed line never breaks the regression.
    }
  }
  return events
}

test("nested owned suite runs through TEST_CONCORD_* inputs under poisoned live CONCORD_* keys", async () => {
  // A concurrent foreign run and a same-prefix sibling, created and hashed
  // BEFORE the owner launches: their unchanged bytes afterwards are real
  // isolation evidence, not an artifact of arriving after cleanup.
  const sentinel = await mkdtemp(join(tmpdir(), "concord-owner-foreign-"))
  await Bun.write(join(sentinel, "active-run.txt"), "another active run owns this root\n")
  const sentinelBefore = await digestTree(sentinel)
  const sibling = await mkdtemp(join(tmpdir(), "cfx-"))
  await Bun.write(join(sibling, "unregistered.txt"), "not owned by this run\n")
  const siblingBefore = await digestTree(sibling)
  const workDir = await mkdtemp(join(tmpdir(), "concord-owner-protocol-"))
  const reportPath = join(workDir, "chain.report.json")
  const pulsePath = join(workDir, "chain.pulse.txt")
  const owner = Bun.spawn([PYTHON, OWNER, CASE_FILE], {
    cwd: import.meta.dir,
    env: {
      ...process.env,
      ...POISON,
      TEST_CONCORD_OWNER_CASE: "chain-writer",
      TEST_CONCORD_OWNER_REPORT: reportPath,
      TEST_CONCORD_OWNER_PULSE: pulsePath,
      TEST_CONCORD_OWNER_BUN: process.execPath,
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const [stdout, stderr, exited] = await Promise.all([
    new Response(owner.stdout).text(),
    new Response(owner.stderr).text(),
    owner.exited,
  ])
  const events = parseEvents(stderr)
  const at = (kind: string): OwnerEvent | undefined => events.find((event) => event.kind === kind)
  const indexOf = (kind: string): number => events.findIndex((event) => event.kind === kind)
  let root: string | undefined
  try {
    // The run itself passed: nothing read a poisoned CONCORD_* name.
    expect(exited, `owned run failed under poison: ${stdout}${stderr.slice(-2000)}`).toBe(0)
    root = at("allocated_root")?.root
    expect(root, "owner never journalled its run root").toBeDefined()
    expect(at("removal_error"), "unexpected removal_error").toBeUndefined()

    // The inner case received its TEST_CONCORD_* inputs: the scenario it
    // acted on, the report path it published to, the pulse path its writer
    // chain wrote to — and its allocations sit under the marker-validated
    // run root, which the nonce proof gates.
    expect(await pathExists(reportPath), "inner case never received its report input").toBe(true)
    const findings = JSON.parse(await readFile(reportPath, "utf8")) as { findings: Record<string, unknown> }
    expect(findings.findings.scenario, "inner case ran the wrong scenario").toBe("chain-writer")
    expect(String(findings.findings.root).startsWith(root as string), "allocation escaped the owned run root").toBe(true)
    // Zero live CONCORD_* keys in the inner run: the preload scrubbed every
    // poisoned key while the TEST_CONCORD_* inputs survived it.
    expect(findings.findings.liveConcordKeys, "inner run still held live CONCORD_* keys").toEqual([])

    // Full descendant drainage precedes removal: the six-link chain was
    // killed and reaped to the kernel's boundary BEFORE the cleanup entry.
    expect(at("child_exit")?.status, "inner run did not pass").toBe(0)
    const drain = at("drain_complete")
    expect(drain, "owner never reached the drain boundary").toBeDefined()
    expect((drain?.reaped ?? []).length, "the six-link chain was not fully reaped").toBeGreaterThanOrEqual(6)
    expect((drain?.killed ?? []).length, "chain links died without SIGKILL escalation").toBeGreaterThanOrEqual(6)
    expect(indexOf("cleanup_entry"), "removal began before the drain boundary").toBeGreaterThan(indexOf("drain_complete"))
    expect(at("removal_entry"), "no removal entry journalled").toBeDefined()
    expect(at("completion"), "no completion event").toBeDefined()

    // Exact-root absence that stays absent, and byte-identical neighbors.
    expect(await pathExists(root as string), "owned run root survived").toBe(false)
    await new Promise((resolve) => setTimeout(resolve, 300))
    expect(await pathExists(root as string), "run root was recreated after removal").toBe(false)
    const pulse = await readFile(pulsePath, "utf8")
    const pids = [...new Set(pulse.trim().split("\n").filter(Boolean).map((line) => Number(line.split(" ")[0])))]
    expect(pids.length, "chain never reached six links").toBeGreaterThanOrEqual(6)
    for (const pid of pids) {
      let gone = false
      for (let attempt = 0; attempt < 100 && !gone; attempt++) {
        try {
          process.kill(pid, 0)
          await new Promise((resolve) => setTimeout(resolve, 100))
        } catch {
          gone = true
        }
      }
      expect(gone, `chain link ${pid} survived the cleanup`).toBe(true)
    }
    expect(await digestTree(sibling), "same-prefix sibling changed during cleanup").toBe(siblingBefore)
    expect(await digestTree(sentinel), "foreign sentinel changed").toBe(sentinelBefore)
  } finally {
    if (root && (await pathExists(root))) await rm(root, { recursive: true, force: true })
    await rm(workDir, { recursive: true, force: true })
    await rm(sibling, { recursive: true, force: true })
    await rm(sentinel, { recursive: true, force: true })
  }
}, 120_000)
