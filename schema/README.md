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

New telemetry events must not introduce personally identifiable information
(PII). Use anonymous, session-scoped identifiers and route patterns rather than
user identifiers, stable installation identifiers, resolved URLs, or resource
names. Do not use schema extensions or obfuscation as a way to permit PII in
telemetry.

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
