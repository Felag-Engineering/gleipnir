# Security

This document describes the v0.1 trust model and its known limitations. These are not edge cases — they are accepted constraints that every operator must understand before deploying Gleipnir.

## MCP Server Trust

Gleipnir fully trusts every registered MCP server. A compromised or malicious MCP server has full control over any tool it implements. Capability policy controls which tools the agent can call — it does not control what the MCP server does when those tools are invoked or what it returns.

Specific risks:

- A rogue server can silently change the behavior of any tool it exposes that is granted to a policy. The tool name stays the same; the implementation does whatever the attacker wants.
- During re-discovery (`POST /api/v1/mcp/servers/:id/discover`), a rogue server can advertise new tool names into Gleipnir's DB. An operator who later grants one of those tools in a policy is handing control of that capability to the attacker without realizing the server controls the implementation.
- Tool results from granted tools can be fabricated to misrepresent world state, feeding false information into the agent's reasoning.
- Gleipnir does not validate tool behavior or cross-check results against any external source.

Note: a rogue server cannot inject tools that were never granted — `ResolveForPolicy` only resolves tools explicitly listed in the policy's capability grants, and unrecognized tools are never passed to the agent. The risks above apply specifically to tools the server is already trusted to implement.

Operators must treat MCP server containers and their host environments as part of their security trust boundary. Compromise of an MCP server is equivalent to full compromise of every capability it implements.

## Webhook Exposure

`POST /api/v1/webhooks/:policy_id` has no signature verification in v0.1. Any caller with network access to the server and knowledge of the URL can trigger an agent run. There is no authentication, no HMAC check, and no rate limit on webhook ingestion.

The webhook URL is a secret. Treat it as a credential:

- Do not log it.
- Do not share it in plaintext (e.g., in Slack, email, or source code).
- Rotate it if you believe it has been exposed (by deleting and recreating the policy).

Secondary defense: protect webhook endpoints at the network layer using firewall rules, VPN, or internal-only routing. This is a requirement, not a recommendation, until HMAC verification is implemented.

HMAC-based signature verification is planned for v0.4 (EPIC-009).

## Prompt Injection

Tool results returned by MCP servers enter the agent's context window without sanitization or escaping. A malicious or compromised MCP server — or an attacker who controls data that feeds into tool results — can craft responses that manipulate the agent's reasoning or cause it to use granted tools in unintended ways.

Hard capability enforcement limits the blast radius: the agent cannot call tools that were not granted to the run, regardless of what the injected content instructs. However, injection can still influence how the agent uses the tools it does have — including the order of operations, the arguments it passes, and whether it invokes feedback or actuator tools.

Structured result wrapping to reduce injection surface is a deferred enhancement with no assigned epic yet.

## Concurrency

When a policy sets `concurrency: parallel`, each concurrent webhook fire for the same policy creates and launches a separate run with no additional coordination between runs. There is no global concurrency cap, no per-policy rate limit, and no backpressure mechanism in v0.1. A burst of webhook calls can result in an unbounded number of parallel agent runs.

This is a known resource exhaustion risk. Deferred to EPIC-008.

## Accepted Advisories

The table below lists known vulnerability advisories that have been reviewed and accepted. Each entry documents why the risk is acceptable and when it should be revisited.

| ID | Package | Severity | Reason accepted | Revisit |
|----|---------|----------|-----------------|---------|
| — | — | — | No accepted advisories at this time | — |

Advisories are tracked here rather than suppressed silently in scanner config. To accept a new advisory, add a row and reference the PR where the decision was made.
