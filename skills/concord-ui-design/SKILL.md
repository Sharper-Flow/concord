---
name: concord-ui-design
description: Make a visual UI or UX change, wireframe, mock or preview in any Product — match its existing design system, hold an accessibility floor with linked WCAG criteria, produce an inspectable preview with its exact version before implementation, route UI/UX approval to the operator, and verify the result in a browser with screenshots. Use for visual design work, an interface-rule check of UI code, or preparing UI material for operator approval.
---

# UI design

Work on one agreed visual change, preview or check, in one Product. This skill
carries a portable design method: it names no host tool, adds no workflow step,
and substitutes for no check. UI/UX is an operator approval class; this method
decides nothing that belongs to the operator.

## Canonical references

- [Method admission and portability](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0246-concord-ships-portable-engineering-methods.md):
  a shipped method names no host tool, machine, model or private source, and
  grants no authority.
- [Operator approval classes](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0243-four-roles-and-six-operator-approval-classes.md):
  UI/UX approval belongs to the operator; ask for it under the shared asking
  instructions.

## Read the existing design system first

Before designing, read the Product's shipped UI: its components, color and
spacing tokens, type scale, corner and elevation treatments, icons, and the
voice of its words. The existing system wins. A screen that matches it is
correct; a novel one that quietly drifts from it is a defect even when it looks
good in isolation. A deliberate deviation is a named decision with a reason,
made once and reused.

Where the Product has no design system, derive the minimum from its strongest
existing screens — palette, type scale, spacing, component shapes — and say the
system is derived, not shipped. Fill the design with real content: real names,
real states, real numbers, in the words the person using the screen would use.
Placeholder words hide the layout problems the preview exists to find.

## Make deliberate choices

Ground the design in the subject: who uses this screen, what job it does, and
what state it must make legible. Spend boldness in one place and keep the rest
quiet; cut decoration that does not serve the job. Typography carries the
personality: one family, or two that are clearly distinct, on a deliberate
scale with a readable line length.

Learn to recognize generic default looks — identical cards with one corner
radius everywhere, the same soft shadow under everything, gradient washes as
decoration, tracked-out all-caps labels, an arrow appended to every action.
Each is legitimate for some brief; all are defaults rather than choices. Where
the brief pins a direction, follow it exactly. Where it leaves an axis free,
spend that freedom on the subject instead of a default.

## Accessibility is a floor

Accessibility is part of the quality of the result, not a feature to add.

- Prefer semantic HTML — native buttons, links, labels, tables — before any
  ARIA. Every form control has a label; icon-only controls carry an accessible
  name; decorative icons are hidden from assistive technology.
- Every interactive element is reachable and operable by keyboard, with a
  visible focus indicator that is never removed without a replacement.
- Announce asynchronous updates that change meaning, through a polite live
  region that does not interrupt.
- Respect the reduced-motion preference: provide a calmer variant or disable
  non-essential motion.
- Check the change against the WCAG 2.2 success criteria it touches, at
  least: contrast for text (1.4.3), contrast for controls and graphics
  (1.4.11), visible focus (2.4.7), focus not obscured (2.4.11), target size
  (2.5.8) and animation from interactions (2.3.3). The W3C criteria links are
  pinned in the skills README; this skill names them and copies nothing.

Empty, error and loading states are design content, not afterthoughts. Name
what happened and the next step, in the interface's own voice; an error that
only apologizes, or an empty screen that explains nothing, is unfinished
design.

## Interface rules worth checking

A checkable set, adapted to the Product's stack and its own conventions:

- Actions are buttons and navigation is links; a clickable element that is
  neither is a defect.
- Forms use the right input types, keep paste working, keep the submit control
  enabled until the request starts, show errors inline beside their field, and
  move focus to the first error.
- Motion animates transform and opacity, names the properties it transitions
  instead of transitioning everything, stays interruptible, and gives
  autoplaying motion a pause.
- Containers survive long content through truncation or wrapping; images carry
  dimensions so the layout does not shift; numeric columns use tabular
  figures.
- The URL reflects meaningful UI state, so a view can be linked and recovered;
  destructive actions confirm first or offer undo.
- Touch targets are large enough, and every gesture has a tap and a keyboard
  equivalent.
- Hover and pressed states exist and increase prominence; a dark theme
  declares its color scheme so native controls follow it.

## Preview before implementation

Before changing Product code, produce an inspectable preview that answers the
question at hand: a static mock, a throwaway route, or the change running on
a branch. The question decides the shape — a look-and-feel question needs
variants to compare side by side, a flow question needs the flow clickable,
a state question needs the hard cases visible. One preview that answers
nothing is worse than one crude one that answers the question.

A mock or throwaway route is throwaway from the start: trivially runnable,
holding no persistence, surfacing the state it demonstrates. When it settles a
decision, fold that decision into the real work; throwaway preview code never
lands on the default branch. A preview that runs the real change on a branch
is an exception: it exists to become the change, under the normal review and
approval route.

Identify the exact version shown: the base commit plus the diff or snapshot
identity. Preview material without a version cannot be reviewed, compared or
approved.

## Operator approval

UI/UX approval belongs to the operator. For UI/UX approval, follow `asking.md`
in the shared instructions, including its screenshot and preview rule for
Linear approval comments. The agent that holds the operator conversation
presents the inspectable material and its exact version, then asks and records
the answer in the work record. A delegated helper returns the material and its
version to its caller and does not ask the operator itself. A minor correction
that introduces no new design or interaction needs no fresh approval
(CD-0243).

A request to prepare a preview, a passing browser check, an approved plan, or
a design the agent itself likes is never approval. Preparing material is not
asking; asking is not approval.

## Verify in a browser

Verify what the change actually renders, with a browser automation tool the
host exposes. Do not name a provider, assume one is installed, or install one.

Reconnaissance before action: open the affected view, wait for it to settle,
screenshot, then interact through the real controls. Capture the affected
states and widths — including the empty and error states — and read the
console for errors the screenshots do not show.

A visual result without visual verification is a gap, not a pass. When no
browser tool is exposed, say so plainly and report the gap with the rest of
the result.

## What a good result looks like

- The change matches the Product's design system, or names each deliberate
  deviation with its reason.
- The accessibility floor holds, and the result says which criteria were
  checked.
- Preview material exists, is inspectable, and carries its exact version.
- Screenshots show the affected states; console errors are fixed or named.
- Operator approval is recorded, explicitly pending, or stated as not
  required; gaps are reported as gaps.
