import { expect, test } from "bun:test"
import { toolAllowedByCapabilities } from "./lane_capabilities"

test("recorded execution capabilities grant the published check helper, not an inert alias", () => {
  for (const capability of ["run_tests", "run_targeted_checks", "run_validators"]) {
    expect(toolAllowedByCapabilities([capability], "concord_worker_check")).toBe(true)
    expect(toolAllowedByCapabilities([capability], "worker_check")).toBe(false)
    expect(toolAllowedByCapabilities([capability], "bash")).toBe(false)
  }
  expect(toolAllowedByCapabilities(["read_repository"], "concord_worker_check")).toBe(false)
})
