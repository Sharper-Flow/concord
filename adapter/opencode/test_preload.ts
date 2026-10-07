// Test children inherit only fixture inputs, never the live session or home.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const root = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-"))
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
    const result = Bun.spawnSync([go, "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"])
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

process.once("exit", () => rmSync(root, { recursive: true, force: true }))
