# CD-0152: Product membership grants Project authority

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Agent authority derived from Product and Project membership
- **Approval:** The operator approved this Product authority clarification.
- **Preserves:** Explicit Product selection for shared Projects, current membership, and fail-closed mutation boundaries

## Context

An agent session resolves one Project from its signed repository location. A
trusted client may authorize a Product while its policy names only the session
Project. That policy must also authorize current sibling Projects in the same
Product. Otherwise, a valid operator approval cannot authorize a membership
mutation that names a sibling Project.

## Decision

### D1. Product membership grants Project authority

When a trusted client authorizes a Product, Concord derives the authority's
Project set from the current Product↔Project membership. The set includes every
current Project in the selected Product. A repository locator identifies the
ambient Project. It does not limit authority to that Project.

### D2. Shared Projects require Product selection

A Project that belongs to more than one Product remains ambiguous without an
explicit Product selection. Concord refuses the invocation and returns the
current Product candidates. A selected Product must belong to the trusted
client's Product scope and must own the ambient Project.

### D3. Membership changes invalidate derived authority

The scope watermark covers all Product↔Project edges for Products that own the
ambient Project. Adding, removing, or changing a sibling membership changes the
watermark. Approval challenge creation and consumption re-read current Product
authority before they permit an effect.

### D4. Refusal before effect

A Project outside the authorized Product scope is refused before an approval
challenge is created or a mutation effect runs. A removed sibling loses
authority on the next invocation.

## Consequences

Product policy is the authority source for its current Project set. Explicit
Project policy entries remain compatible with the stored policy shape, but they
do not replace current Product membership. Cross-Product mutations keep their
existing explicit selection and capability requirements.

## Verification

The store test proves the scope watermark changes when Product membership
changes. Agent tests prove sibling approval, outside-Product refusal, shared
Project ambiguity, and removal on the next invocation.
