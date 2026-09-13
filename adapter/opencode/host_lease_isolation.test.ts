// `bun test` runs every test file in one process, so a module-level seam one
// file sets outlives it. The host lease is the sharpest case: the plugin
// factory claims a lease at load, the claim fails against the unstamped
// repository placeholder, and the recorded fault closes the adapter transport
// for every file that runs afterwards. Those files then refuse each typed call
// with `host_lease_missing` before reaching the behavior under test, so the
// failure surfaces in whichever file runs next and never in the file that
// caused it.
//
// Five files had each hand-rolled the cleanup, three of them with near
// identical comments. Two files that call the factory had not, and the result
// was three manifest-pin tests that failed under one invocation and passed
// under another, because the outcome depended on the order bun happened to
// discover files in.
//
// This test reads the suite's own source and holds every factory-calling file
// to that cleanup. A new file that calls the factory and forgets fails here,
// naming itself, rather than breaking a file it never mentions.
//
// It reads source rather than running the files because the leak it guards is
// cross-file: a runtime assertion could only observe contamination after some
// other file had already caused it, and only when the order put the two in
// that sequence. The source is order-independent.

import { describe, expect, test } from "bun:test"
import { readdirSync, readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const suiteDirectory = dirname(fileURLToPath(import.meta.url))

// FACTORY marks a file that loads the plugin, and RESET marks the cleanup that
// returns the lease to its unclaimed state. A file that carries the first and
// not the second leaks its fault into the rest of the run.
const FACTORY = "ConcordAdapterPlugin"
const RESET = "configureHostLease({ reset: true })"

function suiteFiles(): string[] {
  return readdirSync(suiteDirectory)
    .filter((name) => name.endsWith(".test.ts"))
    .sort()
}

describe("a file that claims a host lease leaves the lease state clean", () => {
  const factoryFiles = suiteFiles().filter((name) => {
    if (name === "host_lease_isolation.test.ts") return false
    return readFileSync(join(suiteDirectory, name), "utf8").includes(FACTORY)
  })

  test("the suite still has files that load the plugin factory", () => {
    // Without this the loop below would pass by covering nothing.
    expect(factoryFiles.length).toBeGreaterThan(0)
  })

  for (const name of factoryFiles) {
    test(`${name} resets the host lease`, () => {
      const source = readFileSync(join(suiteDirectory, name), "utf8")
      expect(
        source.includes(RESET),
        `${name} loads the plugin factory, which records a host-lease fault against the unstamped repository placeholder. ` +
          `The suite shares one process, so the fault closes the adapter transport for every file that runs after this one. ` +
          `Add \`afterAll(() => configureHostLease({ reset: true }))\` to this file.`,
      ).toBe(true)
    })
  }
})
