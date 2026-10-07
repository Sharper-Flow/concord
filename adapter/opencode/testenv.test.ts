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

test("preload removes its run root on normal process exit", () => {
  const runTmp = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-proof-"))
  try {
    const child = Bun.spawnSync(
      [process.execPath, "--preload", join(import.meta.dir, "test_preload.ts"), "-e", `
        console.log(process.env.HOME)
        console.log(process.env.TEST_TELEMETRY_DIR)
      `],
      { env: { ...process.env, TMPDIR: runTmp } },
    )
    expect(child.exitCode).toBe(0)
    const [home, telemetryDir] = child.stdout.toString().trim().split("\n")
    expect(home.startsWith(runTmp)).toBe(true)
    expect(telemetryDir?.startsWith(runTmp)).toBe(true)
    expect(existsSync(home)).toBe(false)
    expect(existsSync(telemetryDir!)).toBe(false)
    expect(readdirSync(runTmp)).toEqual([])
  } finally {
    rmSync(runTmp, { recursive: true, force: true })
  }
})

test("preload cleans its run root when the go env probe fails", () => {
  const runTmp = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-proof-"))
  const binDir = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-bin-"))
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
