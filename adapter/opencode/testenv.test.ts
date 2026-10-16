// The preload must quarantine Go telemetry for every child Go command the
// adapter tests spawn: a private TEST_TELEMETRY_DIR whose mode file holds
// the exact bytes "off", so counters never run and no uploader starts.
import { expect, test } from "bun:test"
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs"
import { randomUUID } from "node:crypto"
import { tmpdir } from "node:os"
import { join } from "node:path"

const go = Bun.which("go")
if (!go) throw new Error("testenv: the go binary is required for adapter telemetry tests")

function tree(dir: string): string[] {
  if (!existsSync(dir)) return []
  const paths: string[] = []
  const walk = (current: string, prefix: string) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name
      paths.push(rel)
      if (entry.isDirectory()) walk(join(current, entry.name), rel)
    }
  }
  walk(dir, "")
  return paths.sort()
}

function requireTelemetryDir(): string {
  const dir = process.env.TEST_TELEMETRY_DIR
  if (!dir) {
    throw new Error("preload must set TEST_TELEMETRY_DIR to a private telemetry directory")
  }
  return dir
}

test("preload quarantines Go telemetry in a private off directory", () => {
  const telemetryDir = requireTelemetryDir()
  expect(readdirSync(telemetryDir).sort()).toEqual(["mode"])
  expect(readFileSync(join(telemetryDir, "mode"), "utf8")).toBe("off")
})

test("go env reports telemetry off and writes nothing under the preload home", () => {
  const home = process.env.HOME ?? ""
  const before = tree(home)
  const child = Bun.spawnSync([go, "env", "-json", "GOTELEMETRY", "GOTELEMETRYDIR"], { env: process.env })
  expect(child.exitCode).toBe(0)
  const reported = JSON.parse(child.stdout.toString())
  expect(reported.GOTELEMETRY).toBe("off")
  expect(reported.GOTELEMETRYDIR).toBe(process.env.TEST_TELEMETRY_DIR)
  expect(tree(home)).toEqual(before)
})

test("go version writes nothing under the preload home", () => {
  const home = process.env.HOME ?? ""
  const before = tree(home)
  const child = Bun.spawnSync([go, "version"], { env: process.env })
  expect(child.exitCode).toBe(0)
  expect(tree(home)).toEqual(before)
})

test("preload fails without a fallback when no run root can be reserved", () => {
  const missingTmp = join(tmpdir(), `concord-adapter-testenv-unresolvable-${randomUUID()}`)
  const child = Bun.spawnSync(
    [process.execPath, "--preload", join(import.meta.dir, "test_preload.ts"), "-e", "process.exitCode = 0"],
    { env: { ...process.env, TMPDIR: missingTmp } },
  )
  expect(child.exitCode).not.toBe(0)
})

test.each([false, true])("preload removes its run root when a child bun test run ends: directed failure=%s", (directedFailure) => {
  const runTmp = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-proof-"))
  try {
    const childSeesLiveEnv = `import { expect, test } from "bun:test"
import { existsSync } from "node:fs"
import { dirname } from "node:path"
test("sees the preload root alive inside TMPDIR", () => {
  expect(dirname(dirname(process.env.HOME!))).toBe(process.env.TMPDIR)
  expect(dirname(process.env.HOME!)).toBe(dirname(process.env.TEST_TELEMETRY_DIR!))
  expect(existsSync(process.env.HOME!) && existsSync(process.env.TEST_TELEMETRY_DIR!)).toBe(true)
})
`
    const childDirectedFailure = 'test("fails as directed", () => {\n  expect(true).toBe(false)\n})\n'
    writeFileSync(join(runTmp, "one.test.ts"), childSeesLiveEnv)
    writeFileSync(join(runTmp, "two.test.ts"), directedFailure ? childSeesLiveEnv + childDirectedFailure : childSeesLiveEnv)
    const child = Bun.spawnSync(
      [process.execPath, "test", "--preload", join(import.meta.dir, "test_preload.ts"), "one.test.ts", "two.test.ts"],
      { cwd: runTmp, env: { ...process.env, TMPDIR: runTmp } },
    )
    expect(child.exitCode, child.stderr.toString()).toBe(directedFailure ? 1 : 0)
    expect(child.stdout.toString() + child.stderr.toString()).toContain(directedFailure ? "1 fail" : "0 fail")
    expect(readdirSync(runTmp).sort()).toEqual(["one.test.ts", "two.test.ts"])
  } finally {
    rmSync(runTmp, { recursive: true, force: true })
  }
})

test("preload cleans its run root when the go env probe fails", () => {
  const runTmp = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-proof-"))
  let binDir: string
  try {
    binDir = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-bin-"))
  } catch (error) {
    rmSync(runTmp, { recursive: true, force: true })
    throw error
  }
  try {
    // A fake go that always fails, visible only to this child through PATH.
    writeFileSync(join(binDir, "go"), "#!/bin/sh\nexit 7\n", { mode: 0o755 })
    const child = Bun.spawnSync(
      [process.execPath, "--preload", join(import.meta.dir, "test_preload.ts"), "-e", "process.exitCode = 0"],
      { env: { ...process.env, TMPDIR: runTmp, PATH: `${binDir}:${process.env.PATH}` } },
    )
    expect(child.exitCode).not.toBe(0)
    expect(child.stderr.toString()).toContain("testenv: go env failed")
    expect(readdirSync(runTmp)).toEqual([])
  } finally {
    rmSync(runTmp, { recursive: true, force: true })
    rmSync(binDir, { recursive: true, force: true })
  }
})
