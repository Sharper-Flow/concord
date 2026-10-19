// Test children inherit only fixture inputs, never the live session or home.
import { afterAll } from "bun:test"
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { ownedFixtureRunRoot } from "./fixture-temp-root"

// Owned case children nest below the nonce-validated owner run root so the
// external owner's post-drain removal sweeps them; others use TMPDIR.
const ownedRoot = process.env.TEST_CONCORD_FIXTURE_RUN_ROOT
const root = mkdtempSync(join(ownedRoot ? ownedFixtureRunRoot() : tmpdir(), "concord-adapter-testenv-"))
try {
  const home = join(root, "home")
  const telemetry = join(root, "go-telemetry")
  mkdirSync(home, { mode: 0o700 })
  mkdirSync(telemetry, { mode: 0o700 })
  // A mode file with the exact bytes "off" makes every child Go command skip
  // counters and uploaders. Arm it before the first Go child runs.
  writeFileSync(join(telemetry, "mode"), "off", { mode: 0o600 })
  process.env.TEST_TELEMETRY_DIR = telemetry

  // Real-core fixtures build Go children. Keep the host's fetched modules and
  // build cache when HOME changes, as the Go TestMain helper does. Resolution
  // runs with the original HOME; telemetry is already private and off.
  const go = Bun.which("go")
  if (go) {
    const result = Bun.spawnSync([go, "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"], { env: { ...process.env } })
    if (result.exitCode !== 0) throw new Error(`testenv: go env failed: ${result.stderr.toString()}`)
    Object.assign(process.env, JSON.parse(result.stdout.toString()))
  }

  for (const key of Object.keys(process.env)) {
    if (key.startsWith("CONCORD_")) delete process.env[key]
  }

  for (const key of ["HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"]) {
    process.env[key] = home
  }
} catch (error) {
  rmSync(root, { recursive: true, force: true })
  throw error
}

// Suite cleanup runs through Bun's afterAll, not process exit: a failed bun
// run fires afterAll but no exit/beforeExit listeners, across the whole run.
// Owned children leave removal to their external owner instead.
if (!ownedRoot) afterAll(() => rmSync(root, { recursive: true, force: true }))
