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
| `x-pia` | Marks a field as containing personally identifiable attributes | `true` |
| `x-obfuscation` | Required obfuscation method for PIA fields | `"sha256"` |
| `tsType` | TypeScript type override used by `json-schema-to-typescript` | `"SHA256Hash"` |

### PIA Fields

Fields marked with `"x-pia": true` contain personally identifiable attributes and **must be obfuscated before sending to Segment**. The `x-obfuscation` field specifies the method — currently only `"sha256"` is supported. The `tsType` extension enforces this at build time by emitting a branded type (e.g. `SHA256Hash`) that prevents raw strings from being passed.

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

3. For PIA fields that need obfuscation, add all three markers:

```json
"userId": {
  "type": "string",
  "description": "...",
  "x-pia": true,
  "x-obfuscation": "sha256",
  "tsType": "SHA256Hash"
}
```

4. In downstream consumers (e.g. `konflux-ui`), regenerate types from the updated schema.
