# 1. Record Architecture Decisions

Date: 2026-04-16

## Status

Accepted

## Context

We need to capture significant architectural and tooling decisions in a
durable, version-controlled form so future contributors and AI coding
assistants can recover the *why* behind the codebase without trawling
git history or chat logs.

## Decision

We use Architecture Decision Records (ADRs) as described by Michael
Nygard in
[Documenting Architecture Decisions](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).

ADRs live under `docs/adr/`, are numbered sequentially (`NNNN-*.md`),
and use the Nygard template:

- Title (`# N. Title`)
- Date
- Status (`Proposed`, `Accepted`, `Deprecated`, `Superseded by ADR-NNNN`)
- Context
- Decision
- Consequences

Each ADR documents one decision in roughly 15-30 lines.

## Consequences

- New contributors can read the ADR index and understand the rationale
  for major choices in minutes.
- Future AI coding agents can ground their suggestions in recorded
  decisions instead of re-deriving them.
- Decisions that are revisited produce a new ADR that supersedes the
  prior one; the prior ADR is kept and marked `Superseded`.
- ADRs are intentionally lightweight; they are not full design
  documents.
