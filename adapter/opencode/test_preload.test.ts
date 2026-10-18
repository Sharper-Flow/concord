import { expect, test } from "bun:test"
import { existsSync, readFileSync } from "node:fs"
import { join } from "node:path"

test("preload isolates session variables and default user state in children", () => {
  const child = Bun.spawnSync([process.execPath, "-e", `
    console.log(JSON.stringify({
      concord: Object.keys(process.env).filter(key => key.startsWith("CONCORD_")),
      homes: ["HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"].map(key => process.env[key]),
    }))
  `], { env: process.env })
  expect(child.exitCode).toBe(0)
  const state = JSON.parse(child.stdout.toString())
  expect(state.concord).toEqual([])
  expect(new Set(state.homes).size).toBe(1)
  expect(state.homes[0]).toContain("concord-adapter-testenv-")
  expect(existsSync(state.homes[0])).toBe(true)
})

// The preload removes every CONCORD_* variable before a test module loads, so
// a test that reads CONCORD_BIN as a binary override never sees a value: the
// override is dead and only hides which binary the test really runs.
test("no adapter test reads a CONCORD_BIN override the preload removes", () => {
  const readers = [...new Bun.Glob("**/*.ts").scanSync(import.meta.dir)]
    .filter((path) => !path.startsWith("node_modules/") && path !== "test_preload.test.ts")
    .filter((path) => readFileSync(join(import.meta.dir, path), "utf8").includes("process.env.CONCORD_BIN"))
    .sort()
  expect(readers).toEqual([])
})
