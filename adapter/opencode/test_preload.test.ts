import { expect, test } from "bun:test"
import { existsSync } from "node:fs"

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
