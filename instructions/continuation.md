# Continuing work

Continue an agreed task while a permitted next action exists and no operator
decision, permission, credential, or action is required. Do not ask for general
permission to continue, and do not treat a progress report as a blocker.

Keep Concord state authority separate from host and repository authority. Inspect
the owning surface before you describe a route, refusal, repair, or capability.
Do not create a second policy, mutation route, approval record, or evidence path.

Reuse an approval when the task, scope, and consequence are unchanged. Do not
request approval again because a bookkeeping or checkpoint write failed. Ask one
specific question when a new consequence or a changed scope needs a decision.

Before you say that an operation is unavailable, inspect its owning surface and
each declared route that can reach it. Do not infer that a native operation is
missing from a partial catalog or from one failed lookup. For worker dispatch,
inspect `concord_work_transition.workflow_action` with `action_id: dispatch_worker`
and `fields.lane_id`; route discovery does not prove current-step admission.

Classify a refusal before you stop. Keep these classes distinct:

- missing capability: inspect the owning native surface before making this claim;
- workflow-step restriction: report the declared route and current-step limit;
- missing approval: preserve an existing approval and ask only for a new decision;
- missing credential: name the credential surface without requesting its secret;
- authorization denial: stop the denied route and do not substitute another one;
- unverified diagnosis: state what remains unknown and perform only permitted reads.

- Correct an agent input and continue only when the governing rule admits the
  corrected request through a declared route.
- Perform a permitted read when the refusal does not prohibit that read.
- Stop the refused route when correction, retry, or substitution is prohibited.
- Reconcile a possible committed effect before you report that no effect exists.
- Ask the operator only when the missing decision, permission, credential, or
  action is specific and outside your authority.

Do not retry an identical refused request. Do not bypass a refusal, invent an
approval, reclassify the work, restart a worker, or use an unauthorized route.

When you stop, state the failed action and boundary, the known cause or limit of
diagnosis, the actual effect state, the recovery owner, and the exact operator
action. State why that action is outside your authority. If no operator action
can help, say so and identify the maintainer follow-up instead of asking the
operator to debug the system.

After the operator reports a repair, verify the relevant authoritative state by
an allowed route before you say that the repair worked. Separate confirmed
repairs from remaining unknowns. A changed host directory or status message is
not proof that Concord accepted the repair.

When you report an operator intervention, state the failed action, the verified
cause or uncertainty, the effect state, the exact operator action, the recovery
owner, and why the action is outside your authority. Do not write only
`contact_operator`, request a generic restart, or assign an internal repair to
the operator.

Suggest a restart only when evidence shows that a reload is required. State what
the reload repairs. An unexplained error does not establish a restart requirement.

Keep mandatory refusal stops, approval boundaries, scope limits, and the ban on
unauthorized execution. Do not open a fresh attempt to simulate a restart.
