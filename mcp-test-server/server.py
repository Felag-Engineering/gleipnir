"""
Gleipnir test MCP server.

Exposes a small set of tools covering all three Gleipnir capability roles:
  sensor    — read-only, called freely
  actuator  — world-affecting (tagged in Gleipnir's tool registry, not here)
  feedback  — human-in-the-loop channel (tagged in Gleipnir's tool registry)

Run with:  python server.py
Endpoint:  http://localhost:8090/mcp
"""

import datetime
import json
import os
import random

from mcp.server.fastmcp import FastMCP

mcp = FastMCP("gleipnir-test-server")


# ---------------------------------------------------------------------------
# Sensor tools — read-only, safe to call freely
# ---------------------------------------------------------------------------

@mcp.tool()
def get_current_time() -> str:
    """Return the current UTC time as an ISO 8601 string."""
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


@mcp.tool()
def get_system_status() -> dict:
    """Return a fake system health snapshot (CPU, memory, disk)."""
    return {
        "cpu_percent": round(random.uniform(5.0, 85.0), 1),
        "memory_percent": round(random.uniform(20.0, 75.0), 1),
        "disk_free_gb": round(random.uniform(10.0, 200.0), 1),
        "uptime_seconds": random.randint(3600, 864000),
    }


@mcp.tool()
def list_items() -> list[dict]:
    """Return a static list of demo items from the 'database'."""
    return [
        {"id": "item-1", "name": "Widget A", "stock": 42},
        {"id": "item-2", "name": "Gadget B", "stock": 7},
        {"id": "item-3", "name": "Doohickey C", "stock": 0},
    ]


@mcp.tool()
def echo(message: str) -> str:
    """Echo the provided message back unchanged. Useful for smoke-testing tool calls."""
    return message


# ---------------------------------------------------------------------------
# Actuator tools — world-affecting; tag these as 'actuator' in Gleipnir
# ---------------------------------------------------------------------------

@mcp.tool()
def send_notification(channel: str, message: str) -> dict:
    """
    Simulate sending a notification to a channel (Slack, email, etc.).
    In this test server the message is only logged — nothing is actually sent.
    Tag as 'actuator' in Gleipnir.
    """
    print(f"[notify] channel={channel!r} message={message!r}")
    return {"ok": True, "channel": channel, "queued_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}


@mcp.tool()
def update_item_stock(item_id: str, new_stock: int) -> dict:
    """
    Simulate updating the stock count for an item.
    In this test server the update is only logged — no state is persisted.
    Tag as 'actuator' in Gleipnir.
    """
    if new_stock < 0:
        raise ValueError(f"stock must be >= 0, got {new_stock}")
    print(f"[update_stock] item_id={item_id!r} new_stock={new_stock}")
    return {"ok": True, "item_id": item_id, "new_stock": new_stock}


@mcp.tool()
def write_file(path: str, content: str) -> dict:
    """
    Write content to a file under /tmp/gleipnir-test/ (sandboxed).
    Tag as 'actuator' in Gleipnir.
    """
    base = "/tmp/gleipnir-test"
    os.makedirs(base, exist_ok=True)
    # Restrict writes to the sandbox directory
    full_path = os.path.realpath(os.path.join(base, os.path.basename(path)))
    if not full_path.startswith(base):
        raise ValueError("path traversal not allowed")
    with open(full_path, "w") as f:
        f.write(content)
    return {"ok": True, "path": full_path, "bytes_written": len(content)}


# ---------------------------------------------------------------------------
# Feedback tool — human-in-the-loop; tag as 'feedback' in Gleipnir
# ---------------------------------------------------------------------------

@mcp.tool()
def request_human_approval(reason: str, context: str = "") -> dict:
    """
    Signal that the agent wants explicit human approval before proceeding.
    In Gleipnir this tool should be tagged as 'feedback' so the runtime routes
    it through the approval channel.
    Returns a placeholder — the real response comes from the operator.
    """
    print(f"[feedback] reason={reason!r} context={context!r}")
    return {
        "status": "pending",
        "reason": reason,
        "context": context,
        "submitted_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    }


if __name__ == "__main__":
    port = int(os.environ.get("MCP_PORT", 8090))
    mcp.settings.host = "0.0.0.0"
    mcp.settings.port = port
    mcp.run(transport="streamable-http")
