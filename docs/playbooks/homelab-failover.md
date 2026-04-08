# Keep your homelab up when hardware fails

**Status:** Planned

This playbook watches Uptime Kuma for outages, stands the affected service back up on a different host using Docker, and updates the DNS entry so users never notice the move. It runs on a poll trigger against Uptime Kuma and uses MCP servers for Docker host management and DNS.

This playbook is not yet written. Track progress in the issue linked from the project roadmap.
