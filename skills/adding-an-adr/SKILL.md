---
name: adding-an-adr
description: >-
  Guides creation of Architecture Decision Records in docs/adr/. Use when the
  user asks to document a design decision, add an ADR, or record an
  architectural choice.
---

# Adding an ADR

## Template

Follow the format in `docs/adr/0003-go-idiomatic-layout-over-src-tests.md`:

```markdown
# N. Title of Decision

Date: YYYY-MM-DD

## Status

Proposed | Accepted | Deprecated | Superseded by [N](link)

## Context

Why the decision is needed. What constraints or trade-offs exist.

## Decision

What was decided.

## Consequences

What follows from the decision — both positive and negative.
```

## Steps

1. Find the next number: `ls docs/adr/ | sort -n | tail -1`
2. Create `docs/adr/NNNN-short-kebab-title.md`
3. Fill in the template above
4. Set status to **Proposed** if seeking review, or **Accepted** if already
   agreed upon

## Conventions

- Number sequentially (0001, 0002, ...)
- Keep titles concise — the filename is the index
- Reference related ADRs by number when superseding or amending
- ADRs are append-only: don't edit accepted ADRs; write a new one that
  supersedes
