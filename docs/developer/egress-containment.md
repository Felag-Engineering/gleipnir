# Egress containment — design and mechanism

**Spec:** `mcp-realignment-spec.md` §7 networking (ADR-056) · **Issue:** #812 ·
**Status:** decided and implemented

"This plugin cannot reach the internet" is supposed to become runtime-enforced —
ADR-001 extended to the network layer. Default deny is already free: #811 puts
every instance on its own `Internal: true` network, so a container reaches
nothing until something lets it. This document is about the *grant*: how a
manifest-declared destination like `slack.com` becomes reachable, and nothing
else does.

## The three candidates

### (a) Host-side forward proxy on the instance's network

The plugin is handed `HTTPS_PROXY`; the host runs a CONNECT proxy and decides
per connection whether the requested **hostname** is on that instance's
allowlist. TLS is tunnelled, not terminated.

### (b) nftables/iptables rules keyed on container labels

The host firewall gets per-container rules permitting the resolved IPs of
granted domains.

### (c) DNS-filtered egress network

The container's resolver only answers for granted domains; everything else
NXDOMAINs.

## Weighing them

**Rootless Podman.** This is the deployment posture the spec recommends, and it
is where (b) dies. Host firewall rules need `CAP_NET_ADMIN` in the host network
namespace. Gleipnir runs *in a container*, so implementing (b) means asking a
homelab operator to grant Gleipnir `--cap-add=NET_ADMIN --network=host` — a
privilege escalation, to enforce a containment policy. Under rootless Podman the
container's traffic is additionally rewritten by `slirp4netns`/`pasta` in a user
namespace, so the host-side rules do not even see the container's addresses.
Labels are a runtime concept; nftables matches packets. There is no join.

(a) and (c) both run entirely in userspace as an ordinary process. No new
privilege.

**TLS passthrough.** Non-negotiable — a proxy that terminated TLS would need its
CA in every plugin image, which is a man-in-the-middle by construction and worse
than the problem. (a) satisfies this: `CONNECT host:443` is decided *before* the
tunnel opens, from the plaintext request line, and after that the proxy copies
bytes without looking at them. (b) and (c) never see TLS at all.

**What "domain" means under rotating IPs.** This is where (b) fails a second
time and (c) fails outright.

(b) must resolve `slack.com` to addresses and write rules. Those addresses rotate,
are geo- and anycast-dependent, and are shared with tenants you did not consent
to; the rules are stale the moment they are written, and a CDN address permits
every other customer on it. (a) matches the name the plugin *asked for*, which is
the thing the admin actually consented to, and does not care what it resolves to.

(c) is not enforcement. A plugin that wants to bypass DNS filtering connects to
an IP literal. That is one line of code, needs no privilege, and leaves the
filter reporting success. A control an untrusted party can opt out of is advice.

**Debuggability.** "Silent denial is undebuggable" is an explicit requirement.
(a) gets it free: the proxy already knows the instance, the hostname, and the
verdict, so a denial is a log line, a metric, and an audit event naming the host
that was refused. (b) denies invisibly unless you also write logging rules, and
what you get is a packet, not a name. (c) denies as an NXDOMAIN — indistinguishable
from a typo or an outage, which is the worst possible error message.

| | rootless Podman | TLS passthrough | rotating IPs | deny visibility | actually enforcing? |
|---|---|---|---|---|---|
| (a) forward proxy | ✅ no privilege | ✅ CONNECT tunnel | ✅ matches the name | ✅ names the host | ✅ |
| (b) nftables | ❌ needs NET_ADMIN + host netns | n/a | ❌ stale/shared IPs | ⚠️ packets, not names | ✅ |
| (c) DNS filter | ✅ | n/a | ✅ | ❌ NXDOMAIN | ❌ trivially bypassed |

## Decision: (a), the host-side forward proxy

It is the only candidate that enforces, needs no new privilege, matches on the
thing the admin consented to, and can explain a denial.

The cost is honest and worth stating: **a plugin that ignores `HTTPS_PROXY` gets
nothing.** That is not a weakness of the design — the network is internal-only,
so ignoring the proxy means reaching nothing at all rather than bypassing the
control. The failure mode is a plugin that does not work, not a plugin that
escapes.

### Simplification: no second network

The issue frames (a) as "second network + host-side forward proxy". The second
network turns out to be unnecessary. Gleipnir is already attached to every
per-instance network (#811, so it can reach each plugin's MCP endpoint), which
means the plugin can already reach Gleipnir at that network's **gateway
address**. The proxy listens there. Adding a second network would add a subnet
allocation, a lifecycle, and a failure mode to reach a place we can already
stand.

## How instance identity is established

The proxy must apply *this instance's* allowlist, so it has to know which
instance is calling — and it must not be tellable.

It reads `conn.LocalAddr()`: the gateway address the connection arrived on. Each
instance has its own `/24` and its own bridge, so that address identifies the
instance exactly. Crucially the peer does not choose it — the kernel does, from
which interface the packet arrived on. A plugin cannot make its traffic appear on
a network it is not attached to, so it cannot claim another instance's grants.

The alternative — trusting the source address — would be weaker for no benefit,
and a token in a `Proxy-Authorization` header would only be as good as the
plugin's willingness to send an honest one.

## The east-west trap

This is the part of the design that has to be right, because getting it wrong
silently undoes #811.

The proxy sits on every instance network *and* has ordinary egress. A naive
implementation would happily accept `CONNECT 10.83.4.5:8080` from instance A and
dial instance B's MCP endpoint — reopening east-west traffic through the very
component that was supposed to contain it, with Gleipnir's own address as the
source.

So the proxy refuses, before dialing:

- **IP-literal targets.** Grants are domains; an address literal can never match
  one, and accepting them at all only creates a way to ask.
- **Any target that resolves into private, loopback, link-local, unique-local, or
  unspecified space.** Checked on the *resolved* addresses, so a public name
  pointing at `10.83.4.5` (DNS rebinding, deliberate or accidental) is refused
  too.

Gleipnir's own API is inside that same refusal — a plugin cannot use the proxy to
reach the host it is running under.

## Where the allowlist comes from

The **admin-consented** manifest, not the manifest on disk: `plugins.manifest_snapshot`,
which only changes when an admin accepts a new one. ADR-045's material-change
rule already blocks a hot-reload that widens capabilities, so a plugin cannot
grant itself a new destination by shipping an update — the update sits in
`pending_review` and the running instance keeps the consented list.

`egress: []` is a real value meaning "reaches nothing", not a missing one. A
plugin with no grants gets a proxy that refuses everything, which is the same
outcome as having no proxy — stated explicitly because "empty means unrestricted"
is the classic way an allowlist becomes decorative.

Matching is exact host or a single leading `*.` wildcard, case-insensitive, on
the hostname alone. `*.slack.com` matches `api.slack.com` but **not** `slack.com`
itself and not `evil-slack.com`; a grant for a parent domain does not imply its
children. Ports are not part of a grant — a destination an admin consents to is a
host, not a socket.

## Deny-path observability

Every refusal produces:

- a `WARN` log with instance, host, and reason;
- `gleipnir_plugin_egress_denied_total{instance, reason}`;
- a `plugin_egress_denied` audit event at `warning` severity.

Allows are counted but not audited per-connection — an audit row per HTTP request
would bury the denials, which are the events worth reading.

## Manual mode

In manual mode Gleipnir writes nothing to the socket, so it cannot create the
networks or set the proxy variables. **Egress containment is the operator's
responsibility there**, and the compose file has to implement the same posture by
hand:

```yaml
# Manual-mode equivalent of what the reconciler does automatically.
networks:
  plugin-acme:
    internal: true        # <- the default deny. Without this line there is none.

services:
  plugin-acme:
    image: ghcr.io/example/acme@sha256:...
    networks: [plugin-acme]
    environment:
      # Gleipnir's proxy, reachable at the network's gateway address.
      HTTPS_PROXY: "http://172.30.0.1:8118"
      HTTP_PROXY:  "http://172.30.0.1:8118"
      NO_PROXY:    ""     # <- empty on purpose: nothing bypasses the proxy.
    labels:
      gleipnir.managed: "false"
      gleipnir.instance: "<instance-id>"
```

Two things go wrong most often, both silent:

- **Omitting `internal: true`.** The container gets normal egress and the proxy
  becomes decorative — every request still succeeds, just not through it.
- **Setting `NO_PROXY`** to anything. Each entry is a hole; `NO_PROXY=*` disables
  containment entirely while leaving every other line looking correct.

The proxy still applies the consented allowlist for whichever instance the
gateway address maps to, so a manual-mode container that *does* route through it
is contained identically. What manual mode cannot do is force it to.

## What this does not cover

- **Per-run or per-policy egress.** Not in the spec. A grant belongs to an
  instance, consented once at review.
- **External MCP servers.** They are not managed by Gleipnir and never had a
  network to contain.
- **Non-HTTP protocols.** A plugin needing raw TCP to a granted host is not
  served by a CONNECT proxy. No plugin needs it today; the day one does, the
  design question is whether to widen the proxy or to reach for a mechanism that
  was rejected above for other reasons.
