// Thin launcher for the synthetic non-Go native dispatch end-to-end suite.
// The assertion-bearing suite lives in
// native_target_dispatch_end_to_end.case.ts and runs as a child `bun test`
// process under the external owner (fixture-root-owner.py): the owner
// allocates one short private run root, forwards the child's assertion
// output, and removes that exact root only after the child cannot write again
// and every owned descendant is killed and reaped (the kernel's ECHILD
// boundary). This launcher holds the owner's stdin open for its whole life,
// so its death by any path asks the owner to cancel and clean up.
import { expect, test } from "bun:test"
import { join } from "node:path"
import { runOwnedSuite } from "./owned-suite"

// Same availability gate the case file holds: a clean checkout has no
// `concord` binary on PATH, so without CONCORD_BIN or a Go toolchain the
// suite reports a skip, never a silent pass.
const routeDeclaration =
  process.env.CONCORD_BIN || Bun.spawnSync(["go", "version"]).exitCode === 0
    ? test
    : test.skip

const NATIVE_CASE_NAME = "N01 drives one synthetic non-Go target check through native preparation dispatch authorization and host observed execution"

routeDeclaration("runs the protected non-Go native target dispatch journey", async () => {
  const result = await runOwnedSuite(join(import.meta.dir, "native_target_dispatch_end_to_end.case.ts"))
  expect(result.exitCode, `owned native-target dispatch suite failed:\n${result.stdout}\n${result.stderr}`).toBe(0)
  const selected = process.env.TEST_CONCORD_ROUTE_CASE
  if (!selected || new RegExp(selected).test(NATIVE_CASE_NAME)) expect(result.stdout).toContain(`[pass] ${NATIVE_CASE_NAME}`)
}, 600_000)
