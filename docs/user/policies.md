# Policies

A policy defines what an agent does, what triggers it, and what it is allowed to touch. This page covers every configurable part of a policy.

## Trigger types

Every policy has exactly one trigger.

### `manual`

Fired on demand from the UI (**Run now** button on the agent detail page, or the play icon on the agents list) or via the API. No additional configuration required. The trigger payload is empty unless you supply one through the API.

```yaml
trigger:
  type: manual
```

### `webhook`

Fired by an HTTP POST to `/api/v1/webhooks/{policyID}`. The request body (any JSON) is delivered to the agent as its first message.

```yaml
trigger:
  type: webhook
  auth: hmac   # hmac | bearer | none
```

The webhook URL and shared secret are shown on the policy detail page. Operators can rotate the secret from the UI or via `POST /api/v1/policies/{id}/webhook/rotate`. The current secret can be retrieved via `GET /api/v1/policies/{id}/webhook/secret` (operator and admin only).

Authentication modes:
- `hmac` — the caller signs the request body with the shared secret and sends the result in the `X-Gleipnir-Signature` header. The header value must be formatted as `sha256=<hex-encoded-HMAC-SHA256-of-body>`.
- `bearer` — the shared secret is sent as a `Bearer` token in the `Authorization` header.
- `none` — no authentication. Only use this behind a network boundary you control.

### `scheduled`

Fires once at each timestamp in `fire_at`. After all timestamps are consumed the policy is automatically paused.

```yaml
trigger:
  type: scheduled
  fire_at:
    - "2026-06-01T09:00:00Z"
    - "2026-07-01T09:00:00Z"
```

Timestamps must be ISO-8601 UTC. The runtime fires the run as soon as the timestamp passes (within the scheduler tick interval); there is no sub-second precision guarantee.

### `poll`

Runs on a recurring interval. Before starting the agent, Gleipnir calls one or more MCP tools and evaluates conditions on their responses. If the conditions are met, the agent runs; if not, the poll cycle ends silently.

```yaml
trigger:
  type: poll
  interval: 15m    # minimum 1m, Go duration format
  match: all       # all (AND) | any (OR)
  checks:
    - tool: monitoring.get_service_status
      input:
        service: api
      path: "$.status"
      equals: "degraded"
```

Comparators: `equals`, `not_equals`, `greater_than`, `less_than`, `contains`. Exactly one comparator is required per check. `greater_than` and `less_than` require numeric values; `contains` requires strings. Types must match the JSONPath extraction result or the check will fail.

The matching tool responses are included in the agent's first message so it has full context on what triggered it.

## Capabilities

Capabilities define the agent's permission boundary. Tools not listed here are never registered with the agent — they do not exist from the agent's perspective. The feedback channel is similarly absent unless explicitly enabled in the `feedback` block.

### Tools

```yaml
capabilities:
  tools:
    - tool: server_name.tool_name
```

Tool references use dot notation: `server_name` is the name of a registered MCP server, `tool_name` is the tool name as discovered from that server.

**With approval gate:**

```yaml
capabilities:
  tools:
    - tool: files.write_file
      approval: required
      timeout: 30m
      on_timeout: reject
```

When a tool has `approval: required`, the agent's attempt to call it pauses the run in `waiting_for_approval`. A user with the **Approver** role sees the request in the UI with the full proposed call (tool name and arguments), and can approve or reject it. Nothing executes until a decision is made. If the timeout expires without a decision, the tool call is rejected and the run fails.

### Feedback

The feedback capability adds a special `gleipnir.ask_operator` tool to the agent's tool list. The agent can call it to pause the run and ask the operator a question. A user with the **Operator** or **Approver** role (or Admin) responds from the UI; the agent resumes with the freeform text response as its next message.

```yaml
capabilities:
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail
```

**`gleipnir.ask_operator` arguments:**

| Field | Required | Description |
|---|---|---|
| `reason` | Yes | Why the agent is asking. Displayed as the headline in the feedback request UI. |
| `context` | No | Supporting detail the operator might need to decide or respond. |

The operator sees `reason` and `context`, types a freeform response, and clicks submit. The agent receives that text as its next user message and resumes.

## Run states

| State | Meaning |
|---|---|
| `pending` | Run is queued, waiting for a worker slot. |
| `running` | Agent is actively executing. |
| `waiting_for_approval` | Agent called a tool with `approval: required`; waiting for a human decision. |
| `waiting_for_feedback` | Agent called `gleipnir.ask_operator`; waiting for an operator response. |
| `complete` | Agent finished and produced a result. |
| `failed` | Run ended with an error (LLM error, tool error, timeout, rejection, token/call budget exceeded, or operator cancellation). |
| `interrupted` | Run was in progress when the server restarted. On restart, Gleipnir marks these automatically so they do not appear stuck. |

A run in any active state can be cancelled by an operator from the UI. Cancellation transitions the run to `failed` with a reason indicating it was cancelled — there is no distinct `cancelled` state.

## Agent settings

### Task

The `task` field is the core instruction for the agent — what to do, what success looks like, any constraints. The trigger payload (webhook body, poll filter results) is delivered as the agent's first user message; reference it from the task as needed.

### Preamble

`preamble` is prepended to the system prompt before `task`. If omitted, Gleipnir uses its default BoundAgent preamble. Override it only if you need fundamentally different behavioral instructions. The runtime appends the capability list (tools and feedback status) after the preamble automatically — do not duplicate tool names in the preamble.

### Limits

```yaml
agent:
  limits:
    max_tokens_per_run: 20000    # default: 20000
    max_tool_calls_per_run: 50   # default: 50
```

Both limits are hard caps. When either is exceeded the run fails immediately with an error step in the trace.

### Concurrency

Controls what happens when a trigger fires while a run for this policy is already active.

| Mode | Behavior |
|---|---|
| `skip` | Discard the new trigger. Safe default — nothing runs twice. |
| `queue` | Hold the trigger and start a new run when the current one completes. |
| `parallel` | Start a new run immediately alongside the active one. |
| `replace` | Cancel the active run and start fresh with the new trigger. Not valid if any tool has `approval: required` — a run paused waiting for an approval decision cannot be safely discarded. |

`queue_depth` (default: 10) sets the maximum number of queued triggers for the `queue` mode. Excess triggers are rejected with HTTP 429.

## Model selection

Omit the `model` block to use the instance default. Override it to pin a specific provider or model for a policy:

```yaml
model:
  provider: anthropic
  name: claude-opus-4-7
```

Supported providers: `anthropic`, `google`, `openai`, `openaicompat`. Available model names depend on which providers have API keys configured in **Admin → Models**.

## Folders

Set `folder` to group policies in the UI. Policies with the same `folder` value appear together in a collapsible row on the policies list. Purely cosmetic — no effect on routing or execution.

```yaml
folder: homelab
```
