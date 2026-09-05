# AgentFlow Proposal Format

AgentFlow proposals are JSON documents that apply explicit changes to the one current architecture.
The client controls navigation; AgentFlow controls page and flow-node types.

IDs use lowercase letters and numbers separated by `.`, `_`, or `-`. Keep IDs stable because diffs,
comments, plans, and implementation evidence reference them.

## Envelope And Operations

Do not set `baseArchitectureHash` when creating or revising a proposal. The CLI pins it to the
current architecture hash and supplies `createdAt` when omitted.

```json
{
  "schemaVersion": 3,
  "id": "invite-delivery",
  "title": "Add audited invitation delivery",
  "summary": "Queue invitation email and record each provider attempt.",
  "changes": [
    {"op": "set-navigation", "navigation": {}},
    {"op": "upsert-page", "page": {}},
    {"op": "delete-page", "pageId": "old.invitation-flow"}
  ]
}
```

- `set-navigation` replaces the complete navigation tree. Use at most once per proposal.
- `upsert-page` adds or replaces one complete page. Change a page at most once per proposal.
- `delete-page` explicitly removes an existing page.
- Omission never deletes anything.
- The complete candidate must validate after all operations are applied.

## Client-Owned Navigation

Navigation is an ordered tree of `folder` and `page` items. Names and nesting are entirely chosen by
the client. A folder must have children. Every page must appear exactly once. IDs are globally
unique and nesting is limited to eight levels.

```json
{
  "schemaVersion": 3,
  "items": [
    {
      "id": "area.workspace-access",
      "type": "folder",
      "title": "Workspace access",
      "children": [
        {
          "id": "section.data",
          "type": "folder",
          "title": "Data",
          "children": [
            {
              "id": "nav.invitation",
              "type": "page",
              "title": "Invitation",
              "pageId": "tenancy.invitation"
            }
          ]
        }
      ]
    }
  ]
}
```

## Shared Page Fields

All pages require `schemaVersion`, `id`, `type`, and `title`. `summary` and `details` are optional.
Details appear in the inspector and use stable IDs:

```json
{
  "id": "handler",
  "label": "Handler",
  "value": "InviteMemberAPIView.post"
}
```

The only page types are `table`, `flow`, and `document`.

## Table Page

Use tables for models, fields, enums, permissions, configuration, and other structured definitions.
Clients choose the columns they need. Column formats are `text`, `code`, `badge`, `boolean`, and
`reference`. Cells may be strings, numbers, booleans, null, or arrays of strings.

```json
{
  "schemaVersion": 3,
  "id": "tenancy.invitation",
  "type": "table",
  "title": "Invitation",
  "summary": "A pending invitation to a workspace.",
  "details": [
    {"id": "table", "label": "PostgreSQL table", "value": "tenancy_invite"}
  ],
  "columns": [
    {"id": "field", "label": "Field", "format": "code"},
    {"id": "type", "label": "Type", "format": "code"},
    {"id": "required", "label": "Required", "format": "boolean"},
    {"id": "rules", "label": "Rules"},
    {"id": "relation", "label": "Relation", "format": "reference"}
  ],
  "rows": [
    {
      "id": "workspace-id",
      "cells": {
        "field": "workspace_id",
        "type": "bigint",
        "required": true,
        "rules": "Indexed",
        "relation": "Workspace.id / CASCADE"
      },
      "details": [
        {"id": "accessor", "label": "Reverse accessor", "value": "invitations"}
      ]
    }
  ]
}
```

A table supports 1 to 16 columns and up to 2,000 rows. Row cells may omit columns when the value is
not applicable. Keep constraints and indexes as rows when that makes the complete model easier to
review, or as dedicated columns/details when the client prefers that structure.

## Flow Page

Use flows for user journeys and technical orchestration. They are intentionally simple, but must
show all meaningful boundaries and outcomes.

```json
{
  "schemaVersion": 3,
  "id": "tenancy.invite-member",
  "type": "flow",
  "title": "Invite a workspace member",
  "summary": "Create membership or queue a new-user invitation.",
  "layout": {"direction": "down"},
  "groups": [
    {
      "id": "request",
      "title": "Request transaction",
      "nodeIds": ["create-api", "user-exists", "write-invite"]
    }
  ],
  "nodes": [
    {"id": "admin", "type": "actor", "title": "Workspace admin"},
    {
      "id": "create-api",
      "type": "api",
      "title": "Create member request",
      "subtitle": "POST /api/v1/workspaces/{id}/members",
      "details": [
        {"id": "handler", "label": "Handler", "value": "InviteMemberAPIView.post"}
      ]
    },
    {"id": "user-exists", "type": "decision", "title": "Does the user exist?"},
    {"id": "write-invite", "type": "database", "title": "Create invitation", "subtitle": "tenancy_invite"},
    {"id": "done", "type": "terminal", "title": "Invitation queued"},
    {"id": "member", "type": "terminal", "title": "Member added"}
  ],
  "edges": [
    {"id": "admin-api", "from": "admin", "to": "create-api"},
    {"id": "api-check", "from": "create-api", "to": "user-exists"},
    {"id": "new-user", "from": "user-exists", "to": "write-invite", "label": "No"},
    {"id": "existing-user", "from": "user-exists", "to": "member", "label": "Yes"},
    {"id": "invite-done", "from": "write-invite", "to": "done", "style": "async"}
  ]
}
```

AgentFlow renders flows vertically. Omit `layout` or set its direction to `down`; parallel branches
are placed side-by-side. Edge styles are `normal`, `async`, and `error`. Decision edges require
labels. Edges may include `description` for inspector-only context. Terminal and error nodes cannot
have outgoing edges. A `job` may be a leaf when the caller intentionally does not wait for task
execution; do not add a fake outcome or edge merely to make the chart terminate.

### Fixed Node Palette

| Type | Meaning | Shape | Color family |
| --- | --- | --- | --- |
| `actor` | Person or initiating role | Capsule | Stone |
| `interface` | Screen, form, dialog, client boundary | Window | Blue |
| `action` | User or system action | Rounded box | Blue |
| `api` | HTTP, RPC, CLI, public handler | Endpoint box | Cyan |
| `decision` | Labeled branch | Diamond | Amber |
| `database` | Persistent read/write | Database | Green |
| `cache` | Cache read/write | Database | Teal |
| `queue` | Queued handoff or broker | Queue | Orange |
| `job` | Asynchronous worker, Celery task, or cron execution | Dashed job box | Orange |
| `event` | Domain event publish/consume | Event | Rose |
| `external` | Third-party or separate service | Cloud | Cyan |
| `subprocess` | Flow documented on another page | Subprocess | Slate |
| `terminal` | Successful or neutral outcome | Capsule | Green |
| `error` | Rejected or failed outcome | Capsule | Red |

Nodes require `id`, `type`, and `title`; they may include `subtitle`, `description`, and `details`.
Clients cannot add node types because consistent shapes make large architecture sets easier to scan.
Do not use one node as a container for an entire function. Split architecture-significant behavior
inside a function into actions, decisions, storage operations, queues, jobs, and other concrete
steps. Function or method names may remain in a step's subtitle or details for traceability.

## Document Page

Use documents sparingly for compact context that cannot be expressed well as a table or flow.
Documents contain sections with stable blocks.

```json
{
  "schemaVersion": 3,
  "id": "tenancy.access-rules",
  "type": "document",
  "title": "Access rules",
  "sections": [
    {
      "id": "scope",
      "title": "Scope boundaries",
      "blocks": [
        {
          "id": "rules",
          "type": "list",
          "items": [
            "A team must belong to the selected workspace.",
            "Only owners and admins can invite members."
          ]
        },
        {
          "id": "token-warning",
          "type": "callout",
          "tone": "warning",
          "text": "Store only invitation token hashes."
        }
      ]
    }
  ]
}
```

Block types:

| Type | Fields |
| --- | --- |
| `paragraph` | `text` |
| `list` | non-empty string `items` |
| `callout` | `text`; optional tone `note`, `warning`, or `success` |
| `code` | `text`; optional `language` |
| `key-value` | `values` containing `label` and `value` |

Block IDs are unique across the complete page so comments and plan references stay unambiguous.
