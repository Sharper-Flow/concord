# R1: Workflow Composition — Research Findings

> **Status:** Research complete; accepted by CD-0006 R1 on 2026-08-06.
> **Decision slot:** CD-0006 R1.
> **Question:** How should Concord's full coordination engine compose when
> research, analysis, investigation, or incidents produce new work?

## Summary

Three composition archetypes exist in durable-workflow and agent-orchestration
systems. Forward-linked successors is the recommended primary pattern; bounded
parent/child is a possible extension if measured evidence requires it.

## Archetype comparison

| Aspect | Forward-linked successors | Bounded parent/child | Arbitrary graph |
|---|---|---|---|
| Mechanism | Workflow completes, emits typed successor link, successor starts independently | Parent spawns child, optionally waits, aggregates completion | General DAG/recursive nesting |
| Authority | Each workflow independent | Parent owns child lifecycle | Shared/mutable |
| Recovery | Each workflow recovers independently | Parent must handle child failure/timeout/cancellation | Cascade risk; infinite recursion |
| Real examples | Temporal Continue-As-New, OpenAI handoffs, LangGraph subgraphs, architecture-spike forward-only rule | Temporal child workflows, Argo subworkflows | Explicitly rejected by real systems |
| Failure modes | Manual state handoff (manageable) | Netflix Conductor stack overflow, Argo infinite recursion, Flyte depth guard at 50 | Token cost ~15× (Anthropic), unbounded recovery, orchestration complexity |

## Key findings

1. **Temporal Continue-As-New** completes the current execution and atomically
   starts a new one with the same Workflow ID. Logical continuity is preserved;
   state handoff is manual. This is the canonical forward-linked pattern.
   Source: <https://docs.temporal.io/design-patterns/continue-as-new>

2. **OpenAI Agents SDK** uses handoffs (one agent transfers control to another)
   and agents-as-tools (one agent calls another as a sub-task). The handoff model
   is forward-linked: the caller finishes its turn and the callee picks up.
   Source: <https://openai.github.io/openai-agents-python/handoffs/>

3. **LangGraph** subgraphs compose by linking artifact outputs, not by nested
   execution. A `Command.PARENT` mechanism exists but is reserved for specific
   patterns. Source: <https://docs.langchain.com/oss/python/langchain/multi-agent/handoffs>

4. **The architecture-spike** already composes forward only
   (spike → change / break-fix / superseding spike) and never nests into
   sub-spikes. This rule generalizes cleanly.

5. **Arbitrary nesting is explicitly rejected** by production systems:
   - Argo Workflows: infinite recursion bugs
     (<https://github.com/argoproj/argo-workflows/issues/11499>)
   - Netflix Conductor: recursive subworkflow stack overflow
     (<https://github.com/Netflix/conductor/issues/2110>)
   - Flyte: depth guard at 50
     (<https://github.com/flyteorg/flytekit/pull/3441>)
   - Multi-agent token cost ~15× per Anthropic handbook

## Recommendation

**Adopt forward-linked successors as the primary composition pattern.**

- A workflow may create/link the next workflow or work item, then finish.
- Each workflow keeps independent durable authority and recovery.
- No nested child execution; no parent waiting on child completion.
- Bounded parent/child is a future extension only if a named scenario proves
  forward-linking is insufficient and measured evidence justifies the complexity.

This matches the architecture-spike's existing forward-only rule and the
canonical pattern in Temporal, OpenAI, and LangGraph.

## Sources

- Temporal Continue-As-New: <https://docs.temporal.io/design-patterns/continue-as-new>
- OpenAI Agents SDK handoffs: <https://openai.github.io/openai-agents-python/handoffs/>
- LangGraph multi-agent: <https://docs.langchain.com/oss/python/langchain/multi-agent/handoffs>
- Argo infinite recursion: <https://github.com/argoproj/argo-workflows/issues/11499>
- Netflix Conductor stack overflow: <https://github.com/Netflix/conductor/issues/2110>
- Flyte depth guard: <https://github.com/flyteorg/flytekit/pull/3441>
- Multi-agent token cost: <https://hld.handbook.academy/curriculum/ai-ml-system-design/multi-agent-orchestration/>
