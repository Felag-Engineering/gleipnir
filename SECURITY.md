# Security Notes

## Known limitations in v0.1

### HMAC verification absent (EPIC-009)

Webhook endpoints at `POST /api/v1/webhooks/:policy_id` do not verify any
request signature. Any caller with network access to the server can trigger an
agent run. HMAC-based verification is deferred to EPIC-009.

Until EPIC-009 is implemented, webhook endpoints must be protected at the
network layer (firewall rules, VPN, internal-only routing).

### No additional concurrency control for `concurrency: parallel` (EPIC-008)

When a policy sets `concurrency: parallel`, concurrent webhook fires for the
same policy each create and launch a separate run with no additional
coordination. There is no global concurrency cap or rate limit. Deferred to
EPIC-008.
