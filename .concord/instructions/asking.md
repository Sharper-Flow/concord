# Asking the operator

Ask one question at a time, and wait for the answer before asking the next. A
second question in the same breath forces two decisions into one reply and gets
a worse answer to both.

Choose the asking route by how the item is driven. An agent spawned or
retriggered through the ticket frontend, or told by the operator to use it,
asks in a comment on the work item's linked Linear issue; load
`concord-ticket-frontend` for the asking procedure, answer authority, durable
stop and fresh-session resume. An agent driven directly asks in the pane; an
explicit instruction switches the route, and an RTE may direct that switch
after more than 30 minutes of pane idleness with no operator activity. A
retrigger is not an answer and not permission to advance.

For a Linear question comment, follow the new-record standard in `records.md`.
Name the event with a heading such as "Your decision needed". Keep the question,
recommendation, options and consequences visible outside collapsed detail.

Write the question so it stands alone. Say what is being decided and what turns
on it. The operator should not have to read anything else to answer.

Use plain words. Leave out identifiers, record numbers, schema field names, and
internal vocabulary. A question that needs the codebase to parse is one the
operator cannot answer, and asking it wastes the exchange.

Options must not carry more context than the question they answer. If the
question is thin and the options are thick, the question is wrong.

Ask when the answer changes the outcome and cannot be established from evidence.
Do not ask for permission to continue work already agreed, do not ask the
operator to choose between things you have not investigated, and do not ask for
a fact you can look up. Investigate first, then ask about the genuine fork.

When a standing rule or approved plan already settles a case, apply it. Record
the decision and its evidence in the existing work record, then continue.
A settled case does not waive an approval, safety rule, scope limit, or mandatory
refusal.

Before deciding a direction question, read the Product's vision document at
`.concord/docs/product-vision.md` (CD-0254). When it settles the question,
decide by it instead of asking the operator. When you settle a direction
question without asking, cite the vision in the work record.

Ask the operator directly for the six approval classes:
product direction, UI/UX, production application data, security, increased
spending, and legal obligations. Apply their stated boundaries: ordinary
coordination writes, ordinary use of an existing paid service, routine technical preferences, minor
UI corrections, and security repairs restoring existing protections do not
independently require approval. Another class may still apply. Changing the
operator's approval boundary always requires approval of its exact wording.

For Concord's deletion exemption, apply CD-0243 D3 as stated in
`change.md`.

For preview or early visual-design approval, show the inspectable material and the
version being approved. Ask an ordinary question and record the answer in the
existing work record.

For every Linear UI/UX approval comment, embed screenshots of the exact material
and version being approved, and link the running development preview whenever
reachable. Screenshots only in issue Resources or a link alone do not satisfy
this rule. Use synthetic or redacted development material with no credentials
or private details.

Every operator approval question names the applicable class above or the existing
operator-only reservation, and the concrete consequence that requires it. Existing
reservations include decisions accepted law expressly leaves to
the operator, such as release cutover. Law status,
seriousness, size, Domain count, and exact wording alone do not require approval.
When a law change falls in a class, obtain its required approval when proposed,
before implementation. Never batch these approvals at the end.

State the choice with your recommendation and material risks. For an action
that requires approval and risks permanent loss, explain what will be lost and
any missing recovery route. The operator may explicitly approve that loss.
Never risk irreversible customer-data loss without informed operator approval.

Without an applicable class or operator-only reservation, decide within your scope
and record the reason instead of asking for discretionary approval. Use an advisor
subagent for a second opinion and a review subagent for external code review.
Coordinate dependencies with the RTE;
neither an RTE nor a Planner supplies permission or operator approval. Missing
information, credentials, host permissions, or a tool refusal may still block
execution; follow admitted recovery routes, never bypass a gate.
