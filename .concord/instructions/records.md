# Records

A record is anything durable that someone opens later: a work item, a ticket, a
commit message, a pull request body, a review comment, a decision note. A
conversation ends with the session. A record outlives it, and the reader who
opens it does not hold the context you hold now.

Name what the reader can open. Give the path, the symbol, the command, the
identifier, or the error text. "The validator rejected the change" sends nobody
anywhere. Name the validator and quote what it printed, and the reader goes
straight to it.

Give delegated work durable context. Keep standing rules in the files that own
them, and link those files instead of repeating their rules in an assignment.
Put the task identity, scope, and stop conditions in the assignment. A delegation
prompt names the agent's role and points to that assignment instead of restating
it. Move added context into its owning file rather than expanding the prompt.

A pull request body names the planning record it serves. Give the issue key or
the work identifier, so a reader who arrives from the repository reaches the
planning record without the conversation that produced the change.

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

Record each distinct inefficiency you encounter, even when a workaround lets
the task continue. Examples include repeated discovery, unnecessary tool calls,
slow or unavailable tools, unclear instructions, and avoidable handoffs.

Name the affected tool, command, file, or step. State what happened, cite the
evidence, and describe the observed cost or risk. Include any workaround and
what remains unresolved. Separate observations from suspected causes. Do not
invent timings, counts, or savings.

Use the existing work record or assigned report, not a private scratch note or
a repository backlog. Workers and utilities use their permitted return format;
do not add unsupported report fields. The coordinator preserves their findings
in the work record. Reuse an existing finding for the same inefficiency and add
new evidence instead of creating duplicates. If no permitted durable record
exists, report that gap rather than claiming the finding is saved.

Raise every finding to the coordinator, or directly to the user when no
coordinator exists. Report blockers and safety risks at the next permitted
reporting point. Include other findings in the next handoff or final response.
Give the record reference when one exists.

The coordinator turns each unresolved finding about a production tool into a
ticket in the planning system, so that an optimized solution can be built.
Production tools are what agents use to deliver work: Concord, its adapters and
CLIs, the host launch and session tools, and the repository's CI. Do not file a
ticket for friction in an operation's own coordination, such as its status
updates, runbook, rules file, or coordinator procedure. Record that friction in
the work record and the handoff only. Reuse an existing ticket for the same
inefficiency and add the new evidence. Put the evidence, observed cost,
workaround, and open questions in the ticket, and report the ticket reference to
the user. An agent without a coordinator gives the user the ticket text instead.

Keep safe work moving within the current role and approved scope. Recording an
inefficiency or filing a ticket does not authorize a repair, a bypass of a
refusal, or a new task.

A filed ticket goes to the backlog. It does not block the current work, and it
joins a milestone only when the operator places it there.
