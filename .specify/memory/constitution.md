# Segment Bridge Spec Kit Constitution

## Core Principles

### I. Specify the intended change

Use Spec Kit for substantial changes to behavior, data contracts, data sources,
or multiple parts of the pipeline. A feature specification describes the
proposed change and its acceptance criteria; it is not a retrospective
inventory of the existing system.

### II. Ground decisions in repository evidence

Base specifications and plans on `AGENTS.md`, `CONTRIBUTING.md`, applicable
ADRs, and the existing implementation. Call out conflicts or missing
requirements before planning implementation. Do not invent project constraints
to fill a template.

### III. Keep the work reviewable

Define observable outcomes, preserve existing interfaces unless the approved
specification explicitly changes them, and break implementation into tasks a
reviewer can assess. Use the repository's existing implementation and
verification patterns rather than introducing parallel tooling.

## Application Scope

Use the core Spec Kit workflow for substantial feature work. Routine fixes,
dependency updates, lint repairs, and documentation-only maintenance can use
the existing lightweight contribution workflow. Do not create a baseline
specification for the entire repository.

## Governance

`AGENTS.md` remains the single source of truth for repository-wide agent
instructions and conventions. `CONTRIBUTING.md` remains the contributor
reference, and accepted ADRs remain the record of architectural decisions.
This constitution guides Spec Kit artifacts and must not duplicate or
supersede those documents. When artifacts conflict with them, resolve the
conflict before implementation.

**Version**: 1.0.0 | **Ratified**: 2026-10-06 | **Last Amended**: 2026-10-06
