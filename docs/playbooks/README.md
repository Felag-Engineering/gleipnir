# Playbooks

End-to-end setups for common Gleipnir automations. Each playbook includes the trigger, the MCP servers it depends on, the policy YAML (inline or as files), and the steps to wire it up in the UI.

## Available playbooks

- [Connect to Arcade (hosted MCP runtime)](arcade/README.md) — required prerequisite for any playbook that uses Google services
- [Plan the week's meals](meal-planning/README.md)
- [Research your own todo list](todoist-research/README.md)
- [Homelab DevOps operations](devops/README.md) — auto-remediate outages from an Uptime Kuma webhook
- [Drive a Relay fleet (fleet-ops)](fleet-ops/README.md) — ask a Relay-managed fleet questions in natural language, and turn Uptime Kuma alerts into scoped fixes that Relay puts to a Gleipnir approver
