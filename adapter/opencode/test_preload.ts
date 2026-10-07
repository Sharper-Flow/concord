// Test children inherit only fixture inputs, never the live session or home.
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

// Real-core fixtures build Go children. Keep the host's fetched modules and
// build cache when HOME changes, as the Go TestMain helper does.
const go = Bun.which("go")
if (go) {
  const result = Bun.spawnSync([go, "env", "-json", "GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"])
  if (result.exitCode !== 0) throw new Error(`testenv: go env failed: ${result.stderr.toString()}`)
  Object.assign(process.env, JSON.parse(result.stdout.toString()))
}

for (const key of Object.keys(process.env)) {
  if (key.startsWith("CONCORD_")) delete process.env[key]
}

const root = mkdtempSync(join(tmpdir(), "concord-adapter-testenv-"))
for (const key of ["HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"]) {
  process.env[key] = root
}

process.once("exit", () => rmSync(root, { recursive: true, force: true }))
