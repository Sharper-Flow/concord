// Load each actual availability declaration without running its route bodies.
// Recorded child inputs, not an empty successful response, prove isolation.
import { expect, test } from "bun:test"
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"

const gateFiles = [
  "project_handoff_end_to_end.test.ts",
  "project_handoff_end_to_end.case.ts",
]

const envQueryArgv = ["env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"]

const sentinelName = "child-environment-sentinel"

// The owner supplies [shim, test, case]. Load that case before the sentinel:
// Bun 1.4 rejects a zero-match filter, so one real assertion must be selected.
const ownerBunShim = `#!/bin/sh
exec "$TEST_REAL_BUN" test --preload "$TEST_PRELOAD" --test-name-pattern '^${sentinelName}$' "$2" "$TEST_CONCORD_CHILD_ENV_SENTINEL"
`

const sentinelTest = `import { expect, test } from "bun:test"
import { readFileSync } from "node:fs"

test("${sentinelName}", () => {
  const report = readFileSync(process.env.TEST_CONCORD_CHILD_ENV_REPORT!, "utf8").trim().split("\\n")
  expect(report).toHaveLength(2)
})
`

const fixtureGo = `#!/usr/bin/env bun
import { appendFileSync, readFileSync } from "node:fs"

const reportPath = process.env.TEST_CONCORD_CHILD_ENV_REPORT
if (!reportPath) {
  console.error("fixture go: no TEST_CONCORD_CHILD_ENV_REPORT in child env")
  process.exit(8)
}
let telemetryMode = null
try {
  telemetryMode = readFileSync(process.env.TEST_TELEMETRY_DIR + "/mode", "utf8")
} catch {
  telemetryMode = null
}
appendFileSync(reportPath, JSON.stringify({
  argv: process.argv.slice(2),
  home: process.env.HOME ?? null,
  xdg_data_home: process.env.XDG_DATA_HOME ?? null,
  xdg_config_home: process.env.XDG_CONFIG_HOME ?? null,
  xdg_state_home: process.env.XDG_STATE_HOME ?? null,
  telemetry_dir: process.env.TEST_TELEMETRY_DIR ?? null,
  telemetry_mode: telemetryMode,
  concord_keys: Object.keys(process.env).filter((key) => key.startsWith("CONCORD_")).sort(),
}) + "\\n")

const argv = process.argv.slice(2)
if (JSON.stringify(argv) === JSON.stringify(${JSON.stringify(envQueryArgv)})) {
  const home = process.env.HOME ?? ""
  process.stdout.write(JSON.stringify({
    GOPATH: home + "/go",
    GOMODCACHE: home + "/go/pkg/mod",
    GOCACHE: home + "/.cache/go-build",
    GOENV: "off",
  }))
  process.exit(0)
}
if (JSON.stringify(argv) === JSON.stringify(["version"])) {
  process.stdout.write("go version go1.26.0 child-environment-fixture linux/amd64\\n")
  process.exit(0)
}
console.error("fixture go: unexpected argv " + JSON.stringify(argv))
process.exit(9)
`

test.each(gateFiles)("%s availability probe inherits the preload isolation, never the startup environment", async (file) => {
  const tempRoot = mkdtempSync(join(tmpdir(), "concord-adapter-child-env-"))
  try {
    const binDir = join(tempRoot, "fixture-bin")
    const startupHome = join(tempRoot, "startup-home")
    const startupTelemetry = join(tempRoot, "startup-telemetry")
    const reportPath = join(tempRoot, "child-env-report.jsonl")
    const sentinelPath = join(tempRoot, "sentinel.test.ts")
    mkdirSync(binDir, { mode: 0o700 })
    mkdirSync(startupHome, { mode: 0o700 })
    mkdirSync(startupTelemetry, { mode: 0o700 })
    writeFileSync(join(startupTelemetry, "mode"), "local", { mode: 0o600 })
    writeFileSync(sentinelPath, sentinelTest)
    writeFileSync(join(binDir, "owner-bun"), ownerBunShim, { mode: 0o755 })
    writeFileSync(join(binDir, "go"), fixtureGo, { mode: 0o755 })

    const env: Record<string, string | undefined> = { ...process.env }
    delete env.TEST_CONCORD_FIXTURE_RUN_ROOT
    delete env.TEST_CONCORD_FIXTURE_RUN_NONCE
    delete env.TEST_CONCORD_OWNER_BUN
    Object.assign(env, {
      TMPDIR: tempRoot,
      HOME: startupHome,
      XDG_DATA_HOME: startupHome,
      XDG_CONFIG_HOME: startupHome,
      XDG_STATE_HOME: startupHome,
      TEST_TELEMETRY_DIR: startupTelemetry,
      CONCORD_SELECTED_PRODUCT_ID: "child-environment-poison-product",
      TEST_CONCORD_CHILD_ENV_REPORT: reportPath,
      TEST_CONCORD_CHILD_ENV_SENTINEL: sentinelPath,
      TEST_REAL_BUN: process.execPath,
      TEST_PRELOAD: join(import.meta.dir, "test_preload.ts"),
      TEST_CONCORD_OWNER_BUN: join(binDir, "owner-bun"),
      PATH: `${binDir}:${dirname(process.execPath)}:${process.env.PATH ?? ""}`,
    })

    // Keep stdin open so parent death still triggers the canonical owner's
    // EOF cancellation and drain. The report is outside the owner's root.
    const owner = Bun.spawn(
      ["python3", join(import.meta.dir, "fixture-root-owner.py"), join(import.meta.dir, file)],
      { cwd: tempRoot, env, stdin: "pipe", stdout: "pipe", stderr: "pipe" },
    )
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(owner.stdout).text(),
      new Response(owner.stderr).text(),
      owner.exited,
    ])
    expect(exitCode, `${file} owner failed:\nstdout:\n${stdout}\nstderr:\n${stderr}`).toBe(0)
    expect(stdout + stderr, `${file} inner run summary`).toContain("1 pass")
    expect(stdout + stderr, `${file} inner run summary`).toContain("0 fail")

    const report = existsSync(reportPath) ? readFileSync(reportPath, "utf8") : ""
    expect(report.trim(), `${file} recorded no fixture go calls; owner stdout/stderr:\n${stdout}\n${stderr}`).not.toBe("")
    const entries = report.trim().split("\n").map((line) => JSON.parse(line))
    expect(entries, `${file} expected exactly two fixture go calls, got:\n${JSON.stringify(entries, null, 2)}`).toHaveLength(2)
    const [query, version] = entries

    // Cache resolution precedes HOME/XDG replacement and CONCORD scrubbing.
    expect(query.argv).toEqual(envQueryArgv)
    expect(query.home).toBe(startupHome)
    expect(query.xdg_data_home).toBe(startupHome)
    expect(query.xdg_config_home).toBe(startupHome)
    expect(query.xdg_state_home).toBe(startupHome)
    expect(query.telemetry_dir).not.toBe(startupTelemetry)
    expect(query.telemetry_mode).toBe("off")
    expect(query.concord_keys).toContain("CONCORD_SELECTED_PRODUCT_ID")

    expect(version.argv).toEqual(["version"])
    expect(version.telemetry_dir).toBe(query.telemetry_dir)
    expect(version.telemetry_dir).not.toBe(startupTelemetry)
    expect(version.telemetry_mode).toBe("off")
    expect(version.concord_keys).toEqual([])
    expect(version.home).not.toBe(startupHome)
    expect(version.xdg_data_home).toBe(version.home)
    expect(version.xdg_config_home).toBe(version.home)
    expect(version.xdg_state_home).toBe(version.home)
    expect(dirname(version.home)).toBe(dirname(version.telemetry_dir))
  } finally {
    rmSync(tempRoot, { recursive: true, force: true })
  }
}, 120_000)
