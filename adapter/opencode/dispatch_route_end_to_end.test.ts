// Thin launcher for the dispatch-route end-to-end suite. The assertion-
// bearing suite lives in dispatch_route_end_to_end.case.ts and runs as a
// child `bun test` process under the external owner
// (fixture-root-owner.py): the owner allocates one short private run root,
// forwards the child's assertion output, and removes that exact root only
// after the child cannot write again and every owned descendant is killed
// and reaped (the kernel's ECHILD boundary). This launcher holds the owner's
// stdin open for its whole life, so its death by any path asks the owner to
// cancel and clean up.
import { expect, test as bunTest } from "bun:test"
import { join } from "node:path"
import { runOwnedSuite } from "./owned-suite"

// Same availability gate the case file holds: a clean checkout has no
// `concord` binary on PATH, so without a Go toolchain the
// suite reports a skip, never a silent pass.
const test =
  Bun.spawnSync(["go", "version"], { env: { ...process.env } }).exitCode === 0
    ? bunTest
    : bunTest.skip

test("runs the protected dispatch journeys and retained recovery witnesses", async () => {
  const result = await runOwnedSuite(join(import.meta.dir, "dispatch_route_end_to_end.case.ts"))
  expect(result.exitCode, `owned dispatch-route suite failed:\n${result.stdout}\n${result.stderr}`).toBe(0)
  const selected = process.env.TEST_CONCORD_ROUTE_CASE
  for (const kind of ["ordinary", "revised", "final-proof"]) {
    const name = `required Review leaves the ${kind} job ready for implementation in Main's session`
    if (!selected || new RegExp(selected).test(name)) expect(result.stdout).toContain(`[pass] ${name}`)
  }
  const preparation = "Review of the ready job freezes a later qualified preparation in Main's session"
  if (!selected || new RegExp(selected).test(preparation)) expect(result.stdout).toContain(`[pass] ${preparation}`)
  if (!selected || new RegExp(selected).test("G01 composes concurrent coordinators")) expect(result.stdout).toContain("[pass] G01 composes concurrent coordinators")
  if (!selected || new RegExp(selected).test("G07 retains sourced research")) expect(result.stdout).toContain("[pass] G07 retains sourced research")
}, 600_000)
