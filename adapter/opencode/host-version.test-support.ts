import { afterAll, beforeAll } from "bun:test"
import { mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

// Refusal diagnostics must not start the operator's installed OpenCode during
// a synthetic host test. Register hooks per caller, not at module load, so each
// test file owns its executable and restores the environment it received.
export function syntheticHostVersionFixture(): void {
  let original: string | undefined
  let root = ""
  beforeAll(() => {
    original = process.env.OPENCODE_BIN
    root = mkdtempSync(join(tmpdir(), "concord-host-version-"))
    const binary = join(root, "opencode")
    writeFileSync(binary, "#!/bin/sh\nprintf '%s\\n' 'synthetic-test-host'\n", { mode: 0o700 })
    process.env.OPENCODE_BIN = binary
  })
  afterAll(() => {
    if (original === undefined) delete process.env.OPENCODE_BIN
    else process.env.OPENCODE_BIN = original
    if (root) rmSync(root, { recursive: true, force: true })
  })
}
