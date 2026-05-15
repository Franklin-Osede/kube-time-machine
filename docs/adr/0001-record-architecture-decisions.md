# 1. Record architecture decisions

Date: 2026-05-15

## Status

Accepted.

## Context

This project will make a number of non-trivial architectural calls (delta vs full snapshots, informers vs polling, local vs cloud storage, single- vs multi-resource rollback, etc.). Decisions made now will be revisited when the project gains contributors or when the author returns to the code months later. Without a written record, the *why* behind a decision evaporates within weeks.

## Decision

We will use Architecture Decision Records (ADRs), as described by Michael Nygard, to capture significant architectural decisions made on this project.

Each ADR is a short Markdown file in `docs/adr/`, numbered sequentially (`NNNN-title.md`), and following this template:

```
# N. Title

Date: YYYY-MM-DD

## Status
Proposed | Accepted | Deprecated | Superseded by ADR-XXXX

## Context
What is the issue we're seeing that motivates this decision?

## Decision
What is the change that we're proposing or have agreed to?

## Consequences
What becomes easier or harder as a result?
```

## Consequences

- Every significant architectural choice is documented before or shortly after implementation.
- Future contributors (including future-Franklin) can reconstruct the *reasoning*, not just the *outcome*.
- Decisions that turn out to be wrong are not erased — they are superseded by a new ADR that links to them. The trail is part of the value.
