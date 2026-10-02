// The adapter's branch_freshness validator is held to the owning contract.
// contracts/branch-freshness.fixtures.json is generated from
// contracts/branch-freshness.v1.json, so a fixture can never drift from the
// declaration it derives from: any divergence this suite can observe is a
// divergence in the validator under test or in a projection the generator
// re-derives. The Go projection applies the same fixtures in
// internal/store/branch_freshness_contract_test.go, so the two validators
// cannot disagree with the contract, or with each other, silently.

import { expect, test } from "bun:test"
import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

import { validateBranchFreshness } from "./concord"

const suiteDirectory = dirname(fileURLToPath(import.meta.url))
const fixturesPath = join(suiteDirectory, "..", "..", "contracts", "branch-freshness.fixtures.json")

interface BranchFreshnessFixtures {
  positive: unknown[]
  negative: unknown[]
}

const fixtures = JSON.parse(readFileSync(fixturesPath, "utf8")) as BranchFreshnessFixtures

test("the owning contract derives both positive and negative fixtures", () => {
  expect(fixtures.positive.length).toBeGreaterThan(0)
  expect(fixtures.negative.length).toBeGreaterThan(0)
})

for (const [index, value] of fixtures.positive.entries()) {
  test(`positive fixture ${index} validates against the owning contract`, () => {
    expect(validateBranchFreshness(value)).toBe(true)
  })
}

for (const [index, value] of fixtures.negative.entries()) {
  test(`negative fixture ${index} refuses against the owning contract`, () => {
    expect(validateBranchFreshness(value)).toBe(false)
  })
}
