// Thin launcher for the Project-session handoff acceptance suite. The
// assertion-bearing suite lives in project_handoff_end_to_end.case.ts and
// runs as a child `bun test` process under the external owner
// (fixture-root-owner.py): the owner allocates one short private run root,
// forwards the child's assertion output, and removes that exact root only
// after the child cannot write again and every owned descendant is killed
// and reaped (the kernel's ECHILD boundary). This launcher holds the owner's
// stdin open for its whole life, so its death by any path asks the owner to
// cancel and clean up.
import { expect, test } from "bun:test"
import { join } from "node:path"
import { runOwnedSuite } from "./owned-suite"

// Same availability gate the case file holds: a clean checkout has no
// `concord` binary on PATH, so without a Go toolchain the
// suite reports a skip, never a silent pass.
const routeDeclaration =
  Bun.spawnSync(["go", "version"], { env: { ...process.env } }).exitCode === 0
    ? test
    : test.skip

async function runHandoffCases(pattern: string) {
  const previous = process.env.TEST_CONCORD_HANDOFF_CASE
  process.env.TEST_CONCORD_HANDOFF_CASE = pattern
  try {
    return await runOwnedSuite(join(import.meta.dir, "project_handoff_end_to_end.case.ts"))
  } finally {
    if (previous === undefined) delete process.env.TEST_CONCORD_HANDOFF_CASE
    else process.env.TEST_CONCORD_HANDOFF_CASE = previous
  }
}

routeDeclaration("capture replays real core effects without duplicate identity", async () => {
  const result = await runHandoffCases("^capture replays real core effects without duplicate identity$")
  expect(result.exitCode, `owned capture suite failed:\n${result.stdout}\n${result.stderr}`).toBe(0)
}, 240_000)

routeDeclaration("boots, consumes, retires, and resumes after host restart through the real core routes", async () => {
  const result = await runHandoffCases("^(boots, consumes|restarts the handoff)")
  expect(result.exitCode, `owned handoff suite failed:\n${result.stdout}\n${result.stderr}`).toBe(0)
}, 450_000)

test("host-started session receives real core context without out-of-surface writes", async () => {
  const result = await runHandoffCases("^host-started session receives real core context without out-of-surface writes$")
  expect(result.exitCode, `owned session context witness failed:\n${result.stdout}\n${result.stderr}`).toBe(0)
  expect(result.stdout).toContain("[pass] host-started session receives real core context without out-of-surface writes")
}, 120_000)
