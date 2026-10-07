// Thin launcher for the dispatch-route end-to-end suite. The assertion-
// bearing suite lives in dispatch_route_end_to_end.case.ts and runs as a
// child `bun test` process under the external owner
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
// `concord` binary on PATH, so without CONCORD_BIN or a Go toolchain the
// suite reports a skip, never a silent pass.
const routeDeclaration =
  process.env.CONCORD_BIN || Bun.spawnSync(["go", "version"]).exitCode === 0
    ? test
    : test.skip

routeDeclaration("dispatches a real store route through Task completion and workflow gates", async () => {
  const result = await runOwnedSuite(join(import.meta.dir, "dispatch_route_end_to_end.case.ts"))
  expect(result.exitCode, `owned dispatch-route suite failed:\n${result.stdout}\n${result.stderr}`).toBe(0)
}, 600_000)
