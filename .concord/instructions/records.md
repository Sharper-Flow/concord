# Records

A record is anything durable that someone opens later: a work item, a ticket, a
commit message, a pull request body, a review comment, a decision note. It
outlives the session, and its later reader does not hold the context you hold now.

Record each fact with its owner and link to it elsewhere. Keep decisions, evidence,
unresolved risks, and remaining scope in the work record, not a command and status
transcript. Do not mirror routine progress in chat, receipts, or planning comments;
required workflow transitions and planning state changes belong in their owning
systems. The step and event comments a ticket-frontend item requires are
work-record updates, not routine progress.

Public repository content excludes private Product or customer data, credentials,
and inaccessible private-source citations. Keep planning backlogs in the owning
Product team's external planning system, never a repository, even for work
discovered for another Product.

- Every Linear issue you create or rewrite and every work item you capture
  states the problem with observed evidence, who or what it affects, and a
  checkable end state before any plan.
- A defect record reports the failure as the affected party saw it, not its
  diagnosis.
- A Linear issue's description holds its current problem, scope, end state and
  decisions; a comment holds one event the description cannot.
- List every law change in the pull request and completion report with its
  record, its change in meaning or an explicit statement of none, and its reason.
- A delegation prompt names the agent's role and points to its assignment;
  standing rules stay in the files that own them.

## Write for the later reader

Name what the reader can open: the path, symbol, command, identifier, or quoted
error text. State what you observed before what it means.

Keep sentences under about forty words. Report cost, risk, and your own error in
the record itself, not only in the conversation where you found them.

Leave quoted material exactly as it came, including errors, log lines, commands,
paths, identifiers, and code. A paraphrased error is not searchable.

## Writing methods

- Load `concord-ticket-writing` when writing a Linear issue, a Linear comment or
  a captured work item; it owns the step and event comments a ticket-frontend
  item posts on its linked issue.
- Load `concord-project-updates` when writing a Linear project description or
  status update.
- Load `concord-pr-writing` when writing a pull request body or completion report.
- Load `concord-research-writing` when writing research findings or a research
  note.
- Load `concord-handoff-writing` when writing an assignment brief, session
  handoff or subagent prompt.

## Encountered inefficiencies

Record actionable defects and recurring delivery costs once in the existing work
record or assigned report; reuse an existing finding. Name the affected surface,
evidence, impact, workaround, and unresolved scope. Separate observations from
suspected causes; do not invent timings or savings.

Resolved incidental friction needs no separate log, ticket, or handoff entry.
Raise blockers and material safety risks when they need attention. Include other
unresolved findings by reference in the next handoff or completion report, not
in a status update for each occurrence. Subagents return their assigned report;
no extra bookkeeping fields are required.

File a planning follow-up for an actionable defect outside approved scope;
routine coordination needs no ticket. A filed ticket goes to the backlog, does
not block current work, and joins a milestone only when the operator places it.
Keep safe work moving within your role and approved scope. Recording an
inefficiency or filing a ticket authorizes no repair, bypass of a refusal, or
new task.
