# Roles

Gleipnir has four roles. Every user holds one or more roles; admins bypass all role checks and inherit every permission automatically.

## Quick reference

| Action | Admin | Operator | Approver | Auditor |
|---|:---:|:---:|:---:|:---:|
| View dashboard stats and attention items | ✓ | ✓ | ✓ | ✓ |
| List and view runs, view reasoning trace | ✓ | ✓ | ✓ | ✓ |
| List and view policies | ✓ | ✓ | — | ✓ |
| List models | ✓ | ✓ | — | ✓ |
| List MCP servers and their tools | ✓ | ✓ | — | ✓ |
| Submit approval decisions | ✓ | — | ✓ | — |
| Submit feedback responses | ✓ | ✓ | ✓ | — |
| Trigger manual runs | ✓ | ✓ | — | — |
| Cancel runs | ✓ | ✓ | — | — |
| Create, update, pause, resume, delete policies | ✓ | ✓ | — | — |
| Rotate and reveal webhook secrets | ✓ | ✓ | — | — |
| Add, delete, and discover MCP servers | ✓ | ✓ | — | — |
| Refresh available models list | ✓ | ✓ | — | — |
| Manage users | ✓ | — | — | — |
| Configure LLM provider API keys | ✓ | — | — | — |
| Enable or disable models | ✓ | — | — | — |
| Edit system settings | ✓ | — | — | — |
| Add or remove OpenAI-compatible providers | ✓ | — | — | — |

## Role descriptions

### Admin

Full access. Admins can do everything in the table above and are the only role that can manage users and provider credentials. The first user created during the setup wizard is always an admin.

### Operator

Day-to-day management of policies and runs. Operators build and maintain policies, fire manual runs, and cancel or monitor active runs. They can respond to feedback requests but cannot submit approval decisions — a separate approver role exists so that the person requesting a sensitive action cannot also approve it.

### Approver

Reviews and acts on approval requests. Approvers can see the full run list and reasoning trace so they have the context needed to make a decision, and can also respond to feedback requests. They cannot view policies, models, or MCP server configuration — and cannot create, modify, or trigger runs. The role is intentionally narrow: the person who sets up a policy that requests approval should not be the same person who grants it.

### Auditor

Read-only access. Auditors can see everything that happened — runs, steps, policies, MCP server configurations — but cannot modify any state. Useful for compliance review or handing someone access to investigate an incident without giving them write access.

## Assigning roles

Users and their roles are managed at **Users** (`/admin/users`) in the Admin section of the sidebar (admin only). A user can hold multiple roles simultaneously — for example, a small team might give one person both `operator` and `approver`.
