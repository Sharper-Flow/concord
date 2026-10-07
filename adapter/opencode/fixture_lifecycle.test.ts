// Regression matrix for the process-owned fixture lifecycle
// (fixture-lifecycle.ts). Each case runs in a real child process that
// allocates a real fixture root, spawns a real fixture-owned child, and ends
// along one lifecycle path — success, assertion failure, timeout, teardown
// exception, cooperative abort, SIGINT, SIGTERM — so every removal path is
// observed together with the exit status it must preserve. The matrix runs
// three consecutive times and asserts zero remaining owned fixture roots, an
// unregistered same-prefix sibling and a concurrent foreign-run sentinel stay
// untouched, and SIGKILL — which cannot run in-process cleanup — leaves an
// identifiable, confined leftover rather than a reclamation claim. No model
// call happens anywhere in this file.
import { expect, test } from "bun:test"
import { chmod, mkdtemp, readdir, readFile, rm, stat } from "node:fs/promises"
import { tmpdir } from "node:os"
import { basename, join } from "node:path"
import { OWNED_ROOT_MARKER, type FixtureJournalEntry } from "./fixture-lifecycle"

const CASE_FILE = join(import.meta.dir, "fixture-lifecycle.case.ts")
const FOREIGN_PREFIX = "concord-lifecycle-foreign-"

interface CaseSynopsis {
  scenario: string
  root: string
  sibling: string
  pid: number
  childPid: number
}

interface CaseResult {
  synopsis: CaseSynopsis
  journal: FixtureJournalEntry[]
  stdout: string
  stderr: string
  exitCode: number | null
}

// Bun.file().exists() reports false for directories, so directory existence
// goes through stat.
async function pathExists(path: string): Promise<boolean> {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}

async function runCase(scenario: string): Promise<CaseResult> {
  const child = Bun.spawn([process.execPath, CASE_FILE, scenario], { stdin: "ignore", stdout: "pipe", stderr: "pipe" })
  const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  let synopsis: CaseSynopsis | undefined
  let journal: FixtureJournalEntry[] = []
  for (const line of stdout.split("\n")) {
    if (!line.startsWith("@@")) continue
    const parsed = JSON.parse(line.slice(2)) as Record<string, unknown>
    if (parsed.scenario !== undefined) synopsis = parsed as unknown as CaseSynopsis
    if (Array.isArray(parsed.journal)) journal = parsed.journal as FixtureJournalEntry[]
  }
  if (!synopsis) throw new Error(`case ${scenario} emitted no synopsis: stdout=${stdout} stderr=${stderr}`)
  return { synopsis, journal, stdout, stderr, exitCode }
}

// A child the case process spawned is dead once the parent resumes: poll
// briefly because a just-killed child can linger as an unreaped entry.
async function expectProcessGone(pid: number, label: string): Promise<void> {
  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      process.kill(pid, 0)
    } catch {
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  throw new Error(`fixture child ${pid} (${label}) survived the cleanup`)
}

// The failing exit statuses the matrix must preserve: a cleanup path never
// converts a failing run into a passing one.
const EXPECTED_EXIT: Record<string, number> = {
  success: 0,
  "assertion-failure": 1,
  timeout: 1,
  "teardown-exception": 1,
  "cooperative-abort": 0,
  sigint: 130,
  sigterm: 143,
}
const REMOVING_SCENARIOS = Object.keys(EXPECTED_EXIT)

async function digestTree(root: string): Promise<string> {
  const names = await readdir(root)
  const parts: string[] = []
  for (const name of names.sort()) parts.push(`${name}:${await readFile(join(root, name), "utf8")}`)
  return parts.join("|")
}

test("every lifecycle case removes its owned root with the exit status preserved, three consecutive runs", async () => {
  // The concurrent foreign-run sentinel: a fixture-shaped root another active
  // run owns. No cleanup in this matrix may touch it, while every root this
  // matrix's own runs registered is gone by the end of each run.
  const sentinel = await mkdtempForeign()
  const sentinelBefore = await digestTree(sentinel)
  try {
    for (let run = 1; run <= 3; run++) {
      for (const scenario of REMOVING_SCENARIOS) {
        const result = await runCase(scenario)
        const label = `${scenario} run ${run}`
        expect(result.exitCode, `${label} stderr: ${result.stderr}`).toBe(EXPECTED_EXIT[scenario])
        // Zero remaining owned fixture roots: the exact registered path is
        // gone after success, failure, timeout, and cancellation alike.
        expect(await pathExists(result.synopsis.root), `${label}: owned root survived`).toBe(false)
        // The unregistered same-prefix sibling proves the removal is scoped to
        // the registered root, never a wildcard prefix match.
        expect(await pathExists(join(result.synopsis.sibling, "unregistered.txt")), `${label}: sibling was wildcard-deleted`).toBe(true)
        await rm(result.synopsis.sibling, { recursive: true, force: true })
        expect(await digestTree(sentinel), `${label}: foreign sentinel changed`).toBe(sentinelBefore)
        await expectProcessGone(result.synopsis.childPid, label)
      }
    }
  } finally {
    expect(await digestTree(sentinel), "foreign sentinel changed at teardown").toBe(sentinelBefore)
    await rm(sentinel, { recursive: true, force: true })
  }
}, 120_000)

test("a completed release records the cleanup entry, removal entry, and completion", async () => {
  const result = await runCase("success")
  const kinds = result.journal.map((entry) => entry.kind)
  expect(result.journal.some((entry) => entry.kind === "cleanup_entry" && entry.root === result.synopsis.root)).toBe(true)
  expect(result.journal.some((entry) => entry.kind === "removal_entry" && entry.root === result.synopsis.root)).toBe(true)
  expect(result.journal.some((entry) => entry.kind === "completion" && entry.root === result.synopsis.root)).toBe(true)
  expect(kinds).not.toContain("removal_error")
  await rm(result.synopsis.sibling, { recursive: true, force: true })
}, 30_000)

test("cooperative abort cancels and drains fixture children before removal", async () => {
  const result = await runCase("cooperative-abort")
  expect(result.exitCode).toBe(0)
  const forRoot = result.journal.filter((entry) => entry.root === result.synopsis.root)
  expect(forRoot.map((entry) => entry.kind)).toContain("child_cancel")
  expect(forRoot.map((entry) => entry.kind)).toContain("child_drained")
  expect(await pathExists(result.synopsis.root)).toBe(false)
  await expectProcessGone(result.synopsis.childPid, "cooperative-abort")
  await rm(result.synopsis.sibling, { recursive: true, force: true })
}, 30_000)

test("the timeout path removes the root through the armed deadline without a release call", async () => {
  const result = await runCase("timeout")
  expect(result.exitCode).toBe(1)
  expect(await pathExists(result.synopsis.root)).toBe(false)
  const completion = result.journal.find((entry) => entry.kind === "completion" && entry.root === result.synopsis.root)
  expect(completion?.detail).toMatch(/^deadline_after_/)
  await rm(result.synopsis.sibling, { recursive: true, force: true })
}, 30_000)

test("a teardown exception still removes the root through the process-exit sweep", async () => {
  const result = await runCase("teardown-exception")
  expect(result.exitCode).toBe(1)
  expect(result.stderr).toContain("synthetic teardown exception")
  expect(await pathExists(result.synopsis.root)).toBe(false)
  await rm(result.synopsis.sibling, { recursive: true, force: true })
}, 30_000)

test("a removal failure is visible and retains the root for identification", async () => {
  const result = await runCase("removal-error")
  expect(result.exitCode).toBe(1)
  // Visible: the recorded error reaches stderr, not only the in-memory journal.
  expect(result.stderr).toContain("removal_error")
  expect(result.journal.some((entry) => entry.kind === "removal_error" && entry.root === result.synopsis.root)).toBe(true)
  // Retained: the unremovable root and its collected evidence stay in place.
  expect(await pathExists(join(result.synopsis.root, "evidence.txt"))).toBe(true)
  // Confine the known leftover: restore access and remove it here. The module
  // itself claimed nothing and deleted nothing else.
  await chmod(result.synopsis.root, 0o700)
  await rm(result.synopsis.root, { recursive: true, force: true })
  await rm(result.synopsis.sibling, { recursive: true, force: true })
}, 30_000)

test("SIGKILL cannot run in-process cleanup and leaves an identifiable owned leftover", async () => {
  const result = await runCase("sigkill")
  // 137 is the conventional status of a SIGKILL death; no handler ran.
  expect(result.exitCode).toBe(137)
  // The leftover survives: this is the one path with no in-process cleanup,
  // and the module does not pretend otherwise. The ownership marker
  // identifies it as this run's owned fixture root.
  expect(await pathExists(result.synopsis.root)).toBe(true)
  const marker = JSON.parse(await readFile(join(result.synopsis.root, OWNED_ROOT_MARKER), "utf8")) as { pid?: number }
  expect(marker.pid).toBe(result.synopsis.pid)
  // Confine the identified leftover manually.
  await rm(result.synopsis.root, { recursive: true, force: true })
  await rm(result.synopsis.sibling, { recursive: true, force: true })
}, 30_000)

test("no owned fixture root or sibling remains under the matrix prefix after the full run", async () => {
  const names = await readdir(tmpdir())
  const remaining = names.filter((name) => name.startsWith("concord-lifecycle-") && !name.startsWith(FOREIGN_PREFIX))
  expect(remaining).toEqual([])
})

async function mkdtempForeign(): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), FOREIGN_PREFIX))
  await Bun.write(join(root, "active-run.txt"), "another active run owns this root\n")
  return root
}
