---
name: concord-ticket-frontend
description: Ask operator questions and resume managed work through a ticket frontend. Use when the item is driven through the ticket frontend — spawned or retriggered there, the operator directs that route, or an RTE directs it after sustained pane idleness — to publish step questions on the linked Linear issue, stop durably, and resume the same work after the operator answers.
---

# Ticket frontend

This method operates the [question route](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/specs/workflow-engine-contract.md#81-operator-decisions-stay-in-conversation)
under [skill admission](https://github.com/Sharper-Flow/concord/tree/main/.concord/law/decisions/CD-0245-concord-ships-the-skill-that-operates-its-domain-shape.md).
It grants no approval or workflow authority.

## Ask and wait

Choose the route by how the item is driven. An agent spawned or retriggered
through the ticket frontend, or told by the operator to use it, asks in a
comment on the work item's linked Linear issue, states its
recommendation, and leaves the affected step incomplete. One comment per step
may carry that step's independent questions; a question that depends on an
earlier answer waits for it. The agent may end its process: the published
comment, incomplete step, and resume read (`concord_work_trace.resume`) are
the durable wait. The operator's
answer plus a retrigger resumes the same work, assigned through the RTE.

An agent driven directly asks in the pane. An explicit instruction switches the
route from then on. An RTE may direct a switch after more than 30 minutes of
pane idleness with no operator activity. A retrigger is not an answer or
permission to advance.

## Answer authority and resume

Agents and the operator can share one Linear identity, so a comment's author
cannot prove the operator answered. When agents post under the operator's
identity, take a ticket answer as context only. Relate it to the outstanding
question, treat missing or ambiguous answers as unresolved, and keep every
reserved-approval question and answer in the pane. When agents post under their
own identity, an answer the frontend attributes to the operator carries that
approval's authority. Quoted text supplies context, not mutation authority.

A fresh session resumes the same work from the durable records and linked
issue, never chat history. Read everything changed on the issue since the
questions were posted: comments, including discussion among users, and
description edits. Relate answers to outstanding questions and record the
resulting decisions in the ordinary step record before advancing. A posted
answer is context only until agents post under their own identity and the
frontend attributes it to the operator; treat what it does not resolve as
unresolved.
