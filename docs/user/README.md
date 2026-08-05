# User Documentation

For people running a Gleipnir instance. If you're looking for how to *build* Gleipnir or contribute code, see [`docs/developer/`](../developer/) instead.

## Contents

- [Setup](setup.md) — first-run walkthrough: key generation, starting the stack, adding a provider, your first policy.
- [Policies](policies.md) — trigger types, capability grants, run states, concurrency modes.
- [Roles](roles.md) — what each role (admin, operator, approver, auditor) can and cannot do.
- [Human-in-the-loop](human-in-the-loop.md) — the three ways a run stops to ask a person, permission vs information, channel assurance and fall-through, the three deadline clocks, and the limits a misbehaving tool server hits.
- [Plugins](plugins.md) — installing, signing, approving, and key-rotating plugins; the unsigned escape hatch.
- [MCP protocol migration](mcp-protocol-migration.md) — protocol version pinning and the Tools-page badge, what changes on a 2026-07-28 server, the "Simplified for …" notice, and `x-mcp-header` behavior.
- [Operations](operations.md) — upgrading, environment variables, database backups, viewing logs, resetting stuck runs.
- [Troubleshooting](troubleshooting.md) — first-run failures and known issues.
