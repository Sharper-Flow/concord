---
name: concord-diagram-writing
description: Decide when to draw a diagram and write or review Mermaid that renders and reads well. Use for visual explanations of flows, interactions, states, data relationships, or architecture.
---

# Diagram writing

This portable method adds no authority, approval, or workflow gate under
[method admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0246-concord-ships-portable-engineering-methods.md).

## Choose the view

Start with the reader's question and the relationships that answer it. Draw a
diagram when a flow, sequence, state change, relationship, or boundary is hard
to follow in prose. Prefer prose for a simple explanation, a list for a short
sequence or inventory, and a table for comparing attributes. Keep a text
explanation beside any diagram so its point survives a render failure.

Choose the view for the question, not for decoration:

- Flowchart: steps, decisions, or directed flows.
- Sequence: messages between participants over time.
- State: a lifecycle and its transitions.
- ER: data entities, relationships, and cardinality.
- Class: types and their structural relationships.
- C4 context: the system, its users, and external systems.
- C4 container: the applications and data stores that must run for the system
  to work, their responsibilities, and how they communicate. A C4 container is
  not a Docker container.

## Make the diagram legible

Show only the elements and relationships needed for the question. Use short,
quoted labels, explicit flow direction, and labelled edges that say what moves
or how elements relate. Split a crowded diagram into focused views rather than
shrinking its text. Match each subgraph with its closing end and use distinct
identifiers for elements. Check syntax against the destination's supported
Mermaid version, especially for labels and grouping; where the destination
renders it, a Mermaid `info` block reports that version.

Do not convey meaning by colour alone: use labels or other visible distinctions.
Add an accessible title and description with Mermaid's `accTitle` and
`accDescr`, and make the accompanying text explain the essential relationships.

Render at the destination and inspect the result for parse errors, clipping,
overlap, readable labels, correct arrows and relationships. Correct the source
and check again. If rendering or accessibility support is unavailable, report
the limitation rather than claiming the diagram was verified.

## References

Check the chosen diagram against Mermaid's syntax and accessibility
documentation and the destination's own diagram guidance, such as GitHub's.
Use the C4 model's diagram definitions for C4 zoom levels.
