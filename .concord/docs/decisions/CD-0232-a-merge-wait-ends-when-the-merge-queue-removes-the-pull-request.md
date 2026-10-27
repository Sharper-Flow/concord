# CD-0232: A merge wait ends when the merge queue removes the pull request

- **Status:** Accepted
- **Date:** 2026-10-17
- **Scope:** Merge-mode classification in `concord ci-wait`; the verb's
  `gh api` read boundary and its `repo` input
- **Preserves:** Head supersession, merged and closed precedence,
  checks mode, bounded slices,
  carried deadlines, and the closed report statuses
- **Related:** [CD-0168](CD-0168-the-ci-wait-pr-population-follows-the-rollup-page-boundary.md),
  [CD-0233](CD-0233-a-checks-wait-waits-for-every-required-context.md)
- **Approval:** The requesting coordinator accepted the design and its
  decisions on 2026-10-17: a removal that predates the wait stays pending,
  and a manual dequeue reports failure. It ruled the record not serious under
  P12, because it adds a terminal case inside one Domain with no new status,
  input, or report field. The D5 `repo` check came after that ruling; it
  refuses no value the `concord_ci_watch` input schema admits.

## Context

A repository with a merge queue tests a merge-group commit, not the pull
request head. A failed queue run therefore never reaches the head's
status-check rollup. When the queue removes the pull request, a merge wait
sees an open pull request with passing head checks. It stays pending until
the deadline, although the queue has dropped the pull request.

`gh pr view` exports no merge-queue field. The pull request timeline does
record the queue. GitHub's
[GraphQL pulls reference](https://docs.github.com/en/graphql/reference/pulls)
defines `AddedToMergeQueueEvent` and `RemovedFromMergeQueueEvent`. A
removal carries `reason`, "The reason this pull request was removed from the
queue", and `beforeCommit`, the before commit of the event. In an observed
removal, `beforeCommit` was the merge-group commit that the failed queue run
tested. The reason is a free string, not an enumeration.

## Decision

### D1. The newest queue event ends a merge wait when it is a fresh drop

After head supersession, merged, and closed classification, every merge poll
reads the newest merge-queue event. The first answer records its event ID as
the wait's baseline, or an empty baseline when the timeline holds no event.
The state file carries the baseline, so a resumed slice keeps it.

The wait ends as `failure` when one answer shows all of these facts:

- the newest event is a removal whose ID differs from the baseline;
- its reason is not `merged`;
- the pull request state is `OPEN`;
- the pull request head is the watched head.

Otherwise the poll continues to the check classification. A removal on a
closed, merged, or changed head is left to the next `gh pr view`, which
reports `closed`, `merged`, or `superseded`. A newest event that is an
enqueue is not a drop, so a re-enqueue after a removal does not end the
wait.

A removal that predates the wait never ends it. An older removal can precede
a fresh enqueue that the wait has not observed yet. Such a wait stays pending
until a later event or the deadline. A manual dequeue is a removal like any
other, and it reports `failure`.

### D2. The removal precedes check failures

A queue removal is classified before the check failures of
[CD-0228](CD-0228-merge-waits-report-observed-check-failures-unless-github-reports-unstable.md)
D1's common classification and reporting rule. `UNSTABLE` delays neither
the removal nor an observed relevant failure. The report reason
names the removal and GitHub's reason string, for example
`the merge queue removed the pull request without merging (failed_checks)`.

A merge wait can report `failure` without a failed check.
The report keeps `checks` from the head rollup, and it can carry no
failure entry.

### D3. Naming the failed queue run is best-effort detail

After a removal is established, the verb lists the workflow runs of the
removal's `beforeCommit` when it is a full 40-character SHA. Each completed
run whose conclusion is not success, skipped, or neutral becomes a failure
entry with its name, URL, class, and failed-log excerpt under CD-0160 D4. The
first entry supplies `run_url`.

A failed run-list read, a missing commit, or no
failed run leaves the failure entries empty. The removal remains the verdict.
The run list is report detail, like the log excerpt, and never a second
verdict.

### D4. The merge-queue read is part of the verdict

A `gh` error or a GraphQL error on the merge-queue read is an `error` report
under CD-0160 D7. A stalled read is `pending` under CD-0168 D3. An answer
without the pull request is an error, never an absent queue.

### D5. Every gh api call is a fixed query

The verb calls `gh api` only for fixed REST
endpoints read with `GET` and for the one constant GraphQL query document of
D1. Owner, name, and number travel as GraphQL variables. Every `gh`
invocation stays a query.

The `repo` input names API paths, so it must be exactly two plain
segments: an owner and a repository name joined by one slash. Each segment
holds only ASCII letters, digits, `.`, `_`, and `-`, and is never `.` or
`..`. A request with any other value is `refused`. Every poll repeats the
`repo` and selector checks, so a resumed state file with an invalid subject
ends as `error`. Neither case reaches a `gh` call.

## Alternatives considered

- End the wait on any removal in the timeline. A removal from before the wait
  would end a wait whose pull request was enqueued again.
- Infer the drop from merge-group runs alone. A manual dequeue leaves no
  failed run, and the removal event is the queue's own decision.
- Report a new status for a dequeue. The closed status set would grow for a
  case `failure` already answers, and every caller would need a change.
- Make the run list part of the verdict. A transient run-list failure would
  hide an established removal.

## Consequences

Each merge poll includes one GraphQL queue request. A merge wait on a repository
without a merge queue reads an empty timeline; a failed queue read is an `error`.

Only the reasons `failed_checks` and `merged` have been observed. Every other
reason counts as a drop. GitHub's auto-merge state after a removal was not
probed, so a removal that GitHub enqueues again later still ends the wait.
The queue run list names runs of the merge-group commit. It cannot prove that
a listed failure caused the removal.

A `repo` value with other characters, such as a host prefix, is refused.
The `concord_ci_watch` input schema already refuses every such value.

## Verification

- `cmd/concord.TestCiWaitPRMergeModeQueueRemovalEndsTheWait` proves a fresh
  removal ends the wait and names the failed merge-group run.
- `cmd/concord.TestCiWaitPRMergeModeQueueRemovalPrecedesCheckFailures` proves
  D2: the removal wins over a failed head check, including with `UNSTABLE`.
- `cmd/concord.TestCiWaitPRMergeModeQueueEventsThatKeepTheWaitOpen` proves a
  merge removal, a closed, merged, or changed head, the baseline removal, a
  re-enqueue, and no event stay pending.
- `cmd/concord.TestCiWaitPRMergeModeQueueBaselineIsTheFirstRead` proves an
  older removal stays pending and an empty baseline survives the state file.
- `cmd/concord.TestCiWaitPRMergeModeQueueRemovalWithoutRunDetail` proves a
  manual dequeue reports `failure` and run naming stays best effort.
- `cmd/concord.TestCiWaitPRMergeModeQueueReadFailureIsError` and
  `cmd/concord.TestCiWaitPRMergeModeStalledQueueReadIsPending` prove D4.
- `cmd/concord.TestCiWaitRepoMustBeOwnerName` and
  `cmd/concord.TestCiWaitResumedStateMustNameAValidSubject` prove the D5
  subject checks for a request and for a resumed state file.
