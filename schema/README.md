# Konflux Analytics Event Schema

This directory contains JSON Schema definitions for analytics events sent to Segment from Konflux components.

## Files

| File | Description |
|------|-------------|
| `ui.json` | Events emitted by the Konflux UI |

## Schema Structure

Each schema file follows this structure:

```
$defs/
  CommonFields     — base fields inherited by every event
  <event>_event    — individual event definitions (allOf → CommonFields + own properties)

oneOf              — references all event defs (enables full type generation in one compile pass)
```

### Custom Extensions

| Extension | Purpose | Example |
|-----------|---------|---------|
| `x-event-name` | The event name string passed to Segment `track()` | `"user_login"` |

### Privacy

New telemetry events must not include raw personally identifiable information
(PII). Authenticated UI events may include `userId` only as a narrowly defined,
cluster-scoped pseudonymous identifier: the UI hashes the authenticated user
identifier with the local cluster identifier as a salt before sending the
payload to Segment. The raw user identifier and raw cluster identifier must
never be serialized in an event payload.

Hashing makes `userId` pseudonymous, not anonymous. It remains linkable across
sessions for the same user on the same cluster and therefore requires the
corresponding privacy review, access controls, and retention classification.
Use `sessionId` for short-lived per-tab correlation, and use route patterns
rather than resolved URLs, resource names, or other identifiers from actual
paths. Do not restore `clusterId` as a transmitted common event property.

## Adding a New Event

1. Add a new entry under `$defs` in the appropriate schema file:

```json
"my_new_event": {
  "x-event-name": "my_new_event",
  "description": "Fired when ...",
  "allOf": [
    { "$ref": "#/$defs/CommonFields" },
    {
      "type": "object",
      "properties": {
        "myField": { "type": "string", "description": "..." }
      },
      "required": ["myField"]
    }
  ]
}
```

2. Add a `$ref` to the root `oneOf` array:

```json
"oneOf": [
  ...existing refs,
  { "$ref": "#/$defs/my_new_event" }
]
```

3. In downstream consumers (e.g. `konflux-ui`), regenerate types from the updated schema.
