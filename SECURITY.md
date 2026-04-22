# Security

Last reviewed: 2026-04-21

This document describes Gleipnir's security model, the controls it implements, the risks it explicitly accepts, and what it expects the operator to own. These are not edge cases — they are the constraints every operator must understand before deploying Gleipnir.

## 1. Scope and Threat Model

Gleipnir is a self-hosted agent orchestrator intended for homelab and small-team deployments. This document reflects the current `main` branch behavior; it is revisited on each release.

**In scope — threats Gleipnir reasons about:**

- An unauthenticated internet attacker reaching the HTTP API.
- A caller who can deliver arbitrary content into an LLM tool result (prompt injection).
- A compromised or rogue MCP server attempting to escalate capability beyond what a policy grants.
- An authenticated user attempting to exceed their role's authority.
- Accidental credential exposure through logs, HTTP responses, or disk.

**Out of scope — Gleipnir does not defend against:**

- Physical access to the host or its storage.
- Compromise of the host OS, container runtime, or hypervisor.
- Compromise of the LLM provider (Anthropic, Google, OpenAI, or any OpenAI-compatible backend).
- Compromise of any MCP server registered with Gleipnir. Capability grants limit which tools an agent may invoke, but a compromised server controls what its tools do and return. See §4.1.
- Denial-of-service attacks against the public HTTP surface. Front Gleipnir with a reverse proxy or CDN capable of absorbing floods.
- Side-channel attacks on cryptographic primitives.
- Filtering, redaction, or classification of data sent to LLM providers. See §4.5.
- Browser vulnerabilities or XSS payloads embedded in operator-authored policy YAML. Operators are trusted to review content they paste into policies; policy authoring is an operator-level privilege.

## 2. Trust Boundaries

| Boundary | Who owns it |
|---|---|
| Gleipnir binary, SQLite DB, embedded frontend | Gleipnir |
| Session cookies, encrypted secrets at rest, role-based access control | Gleipnir |
| MCP server containers and what they do when called | **Operator** |
| LLM provider selection and the compliance posture of data sent there | **Operator** |
| TLS termination, network isolation, firewall rules | **Operator** |
| Host OS, Docker daemon, kernel patching | **Operator** |
| Backup of `GLEIPNIR_ENCRYPTION_KEY` | **Operator** — loss is permanent (see §4.6) |
| Backup of the SQLite database | **Operator** |

## 3. Security Controls Implemented

The following controls are in place on `main` and are part of the product's guarantees, not the operator's responsibility.

**Capability enforcement (ADR-001).** Tools not granted by a policy are never registered with the agent. There is no prompt-based restriction to bypass. A run whose policy references an unresolvable tool fails before entering the `running` state.

**Approval gating (ADR-008 / ADR-029).** Tools marked `approval: required` are intercepted by the runtime *before* the tool call is written to the audit log and *before* any MCP dispatch. Bypassing this via prompt manipulation is structurally impossible.

**Webhook authentication.** HMAC-SHA256 or bearer token verification per policy. Constant-time comparison via `crypto/hmac.Equal`. Full details in §4.2.

**Secrets at rest.** Provider API keys and webhook shared secrets are encrypted with AES-256-GCM under `GLEIPNIR_ENCRYPTION_KEY`. The key is required at startup; the server refuses to run without it.

**Password hashing.** User passwords are bcrypt-hashed with cost 12.

**Session cookies.** `HttpOnly`, `SameSite=Lax`, and `Secure` when the request is over HTTPS.

**Role-based access control.** Four roles enforced by middleware: `admin`, `operator`, `approver`, `auditor`. Rotate/reveal of webhook secrets is restricted to `admin` and `operator` (ADR-034). Auditors see policy configuration (including auth mode) but never webhook secrets.

**Run budgets.** Policies declare `max_tokens_per_run` (default 20,000) and `max_tool_calls_per_run` (default 50). A runaway or looping agent is bounded by both.

**Request body limits and throttling.** All write endpoints enforce a maximum request body size. The webhook endpoint has a global in-flight throttle of 10.

**Audit trail.** Every agent step — capability snapshot, reasoning, tool call, tool result, approval, feedback, error, completion — is persisted to `run_steps`. Writes are serialized through an application-layer queue.

**Fail-fast configuration.** `docker-compose.yml` refuses to start if `GLEIPNIR_ENCRYPTION_KEY` is unset, with a message pointing to the generator command.

## 4. Known Gaps and Accepted Risks

### 4.1 MCP Server Trust

Gleipnir fully trusts every registered MCP server. A compromised or malicious MCP server has full control over any tool it implements. Capability policy controls which tools the agent can call — it does not control what the MCP server does when those tools are invoked or what it returns.

Specific risks:

- A rogue server can silently change the behavior of any tool it exposes that is granted to a policy. The tool name stays the same; the implementation does whatever the attacker wants.
- During re-discovery (`POST /api/v1/mcp/servers/:id/discover`), a rogue server can advertise new tool names into Gleipnir's DB. An operator who later grants one of those tools in a policy is handing control of that capability to the attacker without realizing the server controls the implementation.
- Tool results from granted tools can be fabricated to misrepresent world state, feeding false information into the agent's reasoning.
- Gleipnir does not validate tool behavior or cross-check results against any external source.

A rogue server *cannot* inject tools that were never granted — `ResolveForPolicy` only resolves tools explicitly listed in a policy's capability grants, and unrecognized tools are never passed to the agent. The risks above apply specifically to tools the server is already trusted to implement.

Operators must treat MCP server containers and their host environments as part of their security trust boundary. Compromise of an MCP server is equivalent to full compromise of every capability it implements.

### 4.2 Webhook Authentication

`POST /api/v1/webhooks/{policy_id}` supports three authentication modes, chosen per-policy in the policy YAML's `trigger.auth` field:

- **`hmac`** — the caller must send `X-Gleipnir-Signature: sha256=<hex>` where the value is HMAC-SHA256 of the raw request body using the policy's shared secret. Comparison is constant-time via `crypto/hmac.Equal`.
- **`bearer`** — the caller must send `Authorization: Bearer <token>`. The token is compared constant-time against the policy's stored secret.
- **`none`** — no authentication. The policy ID (a ULID) is effectively the credential.

Shared secrets are stored in the `webhook_secret_encrypted` column of the `policies` table, encrypted with AES-256-GCM under `GLEIPNIR_ENCRYPTION_KEY`. They are never returned by `GET /api/v1/policies/:id`. Rotation and reveal are restricted to `admin` and `operator` roles via `POST /api/v1/policies/:id/webhook/rotate` and `GET /api/v1/policies/:id/webhook/secret`.

The endpoint has a global in-flight request cap (Throttle(10)) and a request body size limit.

**Known limitations:**

- **No replay protection.** A captured valid signed request remains valid indefinitely. There is no timestamp header, nonce, or sequence check. Protect against replay at the network layer (TLS, VPN) and by rotating secrets after suspected exposure.
- **No per-policy rate limit.** A noisy policy can consume the global throttle budget and delay others.
- **`auth: none` is permitted — use with caution.** This mode exists deliberately to support fully-trusted internal enclaves where triggers come from sources that cannot sign (legacy scripts, home automation, LAN-only services). It is not recommended for any policy reachable from outside a trusted network. When a policy uses `none`, its URL is the credential: do not log it, do not share it in plaintext, and rotate the policy ID (by recreating the policy) if it is exposed. More granular controls over when `none` is allowed are planned.

### 4.3 Prompt Injection

Tool results returned by MCP servers enter the agent's context window without sanitization or escaping. A malicious or compromised MCP server — or an attacker who controls data that feeds into tool results — can craft responses that manipulate the agent's reasoning or cause it to use granted tools in unintended ways.

Hard capability enforcement limits the blast radius: the agent cannot call tools that were not granted to the run, regardless of what the injected content instructs. However, injection can still influence how the agent uses the tools it does have — including the order of operations, the arguments it passes, and whether it invokes feedback or actuator tools.

Structured result wrapping to reduce injection surface is a deferred enhancement.

### 4.4 Concurrency

When a policy sets `concurrency: parallel`, each concurrent trigger for the same policy creates and launches a separate run with no additional coordination. There is no global concurrency cap, no per-policy rate limit, and no backpressure mechanism. A burst of webhook calls can result in an unbounded number of parallel agent runs — and a corresponding spike in LLM spend and MCP server load.

The `skip`, `queue`, and `replace` concurrency modes provide alternatives for policies where parallel execution is undesirable.

### 4.5 Data Flow to LLM Providers

Gleipnir does not filter, redact, classify, or scrub data before it is sent to the configured LLM provider. Specifically:

- Trigger payloads are delivered to the agent verbatim as the first user turn.
- Tool results from MCP servers are appended to the conversation verbatim.
- Policy task prompts, system prompts, and capability snapshots are included in every LLM request.

The operator chooses which provider to use and which tools a policy may invoke. Any data those tools can access will, in the normal course of a run, be sent to the chosen provider. Compliance with HIPAA, GDPR, SOC 2, data residency laws, or internal data-classification policies is the operator's responsibility — Gleipnir neither enforces nor validates such constraints.

### 4.6 Single Encryption Key

All encrypted-at-rest secrets (provider API keys, webhook secrets) are protected by a single symmetric key, `GLEIPNIR_ENCRYPTION_KEY`. The consequences:

- **Loss is permanent.** There is no fallback decryption path. A lost key means every encrypted value in the DB becomes unrecoverable.
- **No in-place rotation.** Rotating the key in v1 requires re-entering every provider API key and every webhook secret after setting the new key. Tooling for seamless rotation is tracked in an open issue.

Back up the key immediately after generation. Store it in a password manager or secrets vault separate from the Gleipnir host.

## 5. Operator Responsibilities

Gleipnir delegates the following to the operator. These are not suggestions — the system cannot function safely without them.

1. **TLS termination.** Deploy Gleipnir behind a reverse proxy (Caddy, nginx, Traefik, Cloudflare Tunnel) that terminates TLS. Gleipnir itself serves plain HTTP.
2. **Network exposure.** Decide which endpoints are reachable from where. In particular, webhook endpoints using `auth: none` must not be routable from untrusted networks.
3. **Host and container hygiene.** Keep the host OS, kernel, and Docker daemon patched. Gleipnir cannot protect against a compromised host.
4. **Encryption key backup.** Back up `GLEIPNIR_ENCRYPTION_KEY` to a location independent of the Gleipnir host. Losing it means losing every stored provider API key and webhook secret.
5. **Database backup.** Back up the SQLite file at `/data/gleipnir.db`. It contains every policy, run, audit step, and encrypted secret.
6. **MCP server vetting.** Only register MCP servers you trust at the same level as the capabilities you plan to grant them. Run them in isolated containers with minimal host access.
7. **Data flow to LLM providers.** Evaluate what data your policies will expose to Anthropic / Google / OpenAI / your chosen OpenAI-compatible backend. Any data reachable by a granted tool can end up in the provider's context. See §4.5.
8. **User account hygiene.** Use strong passwords. Revoke sessions (`DELETE /api/v1/auth/sessions/:id`) when a device is lost or a user leaves.
9. **Policy review.** Policy authoring is an operator-level privilege. Operators are trusted to review the YAML they save, including any content pasted from third parties.
10. **Secret rotation.** Rotate webhook secrets if you suspect exposure. Rotate provider API keys in the upstream console and re-enter them in `/admin/models`.
11. **Audit monitoring.** Review the runs list and reasoning traces periodically. Gleipnir records everything an agent does; nothing reviews those records for you.

## 6. Reporting a Vulnerability

Report suspected vulnerabilities through **[GitHub Security Advisories](https://github.com/Felag-Engineering/gleipnir/security/advisories/new)**. Do not open a public issue.

Please include:

- A description of the issue and its impact.
- Steps to reproduce.
- Affected version / commit.
- Any proof-of-concept code or logs (sanitized).

**Response commitment:**

- Acknowledgment within 5 business days.
- Initial triage and severity assessment within 10 business days.
- **Coordinated disclosure within 90 days** of the acknowledgment, or sooner by mutual agreement for critical issues.

We appreciate reports that give us time to fix issues before public disclosure.

## 7. Security Update Process

Security fixes are released as part of regular versioned releases. Subscribe to [GitHub Releases](https://github.com/Felag-Engineering/gleipnir/releases) to be notified. Security-relevant changes are flagged in `CHANGELOG.md` under a `Security` heading.

Operators should:

- Track the `main` branch or release tags.
- Review `CHANGELOG.md` for each release before upgrading.
- Rebuild or re-pull Docker images promptly after a security release.

## 8. Accepted Advisories

The table below lists known vulnerability advisories that have been reviewed and accepted. Each entry documents why the risk is acceptable and when it should be revisited.

| ID | Package | Severity | Reason accepted | Revisit |
|----|---------|----------|-----------------|---------|
| — | — | — | No accepted advisories at this time | — |

Advisories are tracked here rather than suppressed silently in scanner config. To accept a new advisory, add a row and reference the PR where the decision was made.
