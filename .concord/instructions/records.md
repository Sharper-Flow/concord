# Records

A record is anything durable that someone opens later: a work item, a ticket, a
commit message, a pull request body, a review comment, a decision note. A
conversation ends with the session. A record outlives it, and the reader who
opens it does not hold the context you hold now.

Record each fact with its owner and link to it elsewhere. Keep decisions,
evidence, unresolved risks, and remaining scope in the work record, not a
transcript of commands and status changes. Do not mirror routine progress in
chat, receipts, and planning comments. Required workflow transitions and planning
state changes still belong in their owning systems.

List every law change in the pull request and completion report with its record,
its change in meaning or an explicit statement of none, and its reason.

## State the problem and the end state

Every Linear issue you create or rewrite, and every work item you capture,
states three things before any plan:

- the problem, with the evidence you observed: the error, the path, the
  measurement, or the operator's words;
- who or what the problem affects;
- the end state that closes the item, written so a check or a reviewer can
  decide it when the change is delivered.

A goal, a feature request, or a law to implement is not yet a problem. Say what
fails, is missing, or costs too much today without it.

A defect record reports the failure, not its diagnosis. Its title and problem
name what failed as the affected party saw it. Put a suspected cause only
under a heading that marks it unverified, with the evidence for and against
it. Write the end state as the outcome the affected party needs, never as a
change to a suspected mechanism. The agent that takes the item reproduces the
failure and confirms the cause before it designs a fix.

## Write for the later reader

Name what the reader can open. Give the path, the symbol, the command, the
identifier, or the error text. "The validator rejected the change" sends nobody
anywhere. Name the validator and quote what it printed, and the reader goes
straight to it.

Give delegated work durable context. Keep standing rules in the files that own
them, and link those files instead of repeating their rules in an assignment.
Put the task identity, scope, and stop conditions in the assignment. A delegation
prompt names the agent's role and points to that assignment instead of restating
it. Move added context into its owning file rather than expanding the prompt.

State what you observed before you state what it means. An observation is
checkable and a conclusion is not. A reader who doubts your conclusion can still
use your observation, so the record keeps its worth even when you were wrong.

Prefer the verb to the noun built from it. Write "the check refused the request"
rather than "refusal of the request occurred at the check". A noun made from a
verb drops the actor, and the reader cannot tell who did what to what.

Keep each sentence under about forty words, and give each thing one name
throughout. A record that calls the same file two names reads as two files.

Report the cost, the risk, and your own error in the record itself, not only in
the conversation where you found them. The reader who needs that warning most is
the one who arrives after the conversation is gone.

Leave quoted material exactly as it came: errors, log lines, commands, paths,
identifiers, and code. A paraphrased error string is no longer searchable, and
the reader cannot match it against what they see.

## Encountered inefficiencies

Record actionable defects and recurring delivery costs once in the existing work
record or assigned report. Name the affected surface, cite the evidence, and
state the impact, workaround, and unresolved scope. Reuse an existing finding.
Separate observations from suspected causes; do not invent timings or savings.

Resolved, incidental friction needs no separate log, ticket, or handoff entry.
Raise blockers and material safety risks when they need attention. Include other
unresolved findings by reference in the next handoff or completion report, not
in a status update for each occurrence. Workers keep their permitted report
format; no extra bookkeeping fields are required.

File a planning follow-up when an actionable defect needs work outside the
approved scope. Routine coordination does not require a ticket.

Keep safe work moving within the current role and approved scope. Recording an
inefficiency or filing a ticket does not authorize a repair, a bypass of a
refusal, or a new task.

A filed ticket goes to the backlog. It does not block the current work, and it
joins a milestone only when the operator places it there.
