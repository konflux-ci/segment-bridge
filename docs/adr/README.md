# Architecture Decision Records

This directory holds Architecture Decision Records (ADRs) in the
[Nygard format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).
See [ADR-0001](0001-record-architecture-decisions.md) for the
process.

## Index

- [ADR-0001 — Record Architecture Decisions](0001-record-architecture-decisions.md)
- [ADR-0002 — Use mise for Toolchain Pinning](0002-use-mise-for-toolchain-pinning.md)
- [ADR-0003 — Use Go-Idiomatic Layout Instead of src/ and tests/](0003-go-idiomatic-layout-over-src-tests.md)
- [ADR-0004 — Use gitlint Instead of commitlint+husky for Conventional Commits](0004-gitlint-vs-commitlint.md)

## Adding a new ADR

1. Copy an existing file as `NNNN-short-kebab-title.md` (next number).
2. Set `Status: Proposed` until accepted.
3. Keep it short (15-30 lines). Link to code with relative paths.
4. Add an entry to the index above in the same commit.
