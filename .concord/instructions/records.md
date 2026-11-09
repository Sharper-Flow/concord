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

## New Linear records

Linear is for people. Concord and git hold agent state. For each new Linear
record, keep visible prose to about 600 characters at most, for someone who has
not read the issue. Count all visible text outside every collapse, including text
after a collapse. Lead with one sentence saying what happened or what is needed.
Use one event heading; add subheadings only for distinct parts. Use a
few short bullets, one fact each and none over about two lines. Use tables only
for real comparisons, with 2–3 columns, and bold only for the one thing a reader
must not miss. Use code formatting only for things a reader copies. Cut restated
rules, history, hedging and content already linked.

Preserve full operator answers word-for-word in Linear, with the question and
what the answer decides; only surrounding prose is capped. A collapsed Agent
detail section, opened with +++ Agent detail and closed with +++, holds the
paths, IDs, commands, counts and other detail agents need on the ticket, and
has no few-line cap. Keep collapsed content scannable. Prefer links for long
evidence, transcripts and check output with a better home. This standard
applies to new records; do not rewrite existing comments to conform.

Keep one event per record: each required step or event comment states that event's
result and consequence and links earlier diagnosis and evidence. Keep assignments
as current assignments, not investigation or review journals. RTE and project
updates add cross-stream changes or waits, not a duplicate of the owner's completion
report. Do not omit required ticket-frontend step or event comments.

Keep unresolved questions, known limitations and the difference between merged,
deployed and verified visible. Preserve exact approval and quoted wording, timers,
notification owners and safe-resume detail where their consumer can use them.
For Linear question comments, keep the question, recommendation, options and
consequences visible and concise, as the shared asking instructions require.

### Placement and consumers

- Keep every new-session assignment complete in the delivery issue assignment
  comment's collapsed Agent detail, before or after capture, never only a host
  file or chat. Include the objective, write set, exclusions, checks, stop
  conditions and return point; link standing rules and longer evidence. No Concord
  item is required first.
- Once captured, Concord step notes, review or bound evidence supplement execution
  and resume context; they do not replace the complete assignment comment.
  Ticket-frontend resume remains Linear comments plus `concord_work_trace.resume`;
  link moved detail from the comments.
- Put evidence, test runs, CI, rehearsals, counts and digests in a PR body,
  release or Concord work record linked from Linear.
- For host or toolbox work with no Concord item, link its PR or assignment
  record, or keep its complete task-specific detail in collapsed Agent detail.
- Summarize blockers and waits in Linear, linking the blocking records and naming
  what unblocks the work. Put drain acknowledgements in a coordination project
  update or Concord record, not per-session issue comments.
- Keep research in its canonical knowledge home and copy development-time
  findings into a linked Linear Document; use `concord-research-writing` for
  the placement route. Keep routine coordination in coordination project updates.
- Keep readiness and completion comments concise and link their owning records.
  Ordinary issue progress comments are rare: start, blocker and done; a
  ticket-frontend item follows its required step and event comments.

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
