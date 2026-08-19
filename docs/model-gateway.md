# P — model gateway

How sessions reach hosted and local models without receiving upstream
credentials or general access to the host network.

> **Status: design.** This document is the authority for P's model-gateway
> boundary. [communication-boundaries.md](communication-boundaries.md) owns the
> channel and [technology-stack.md](technology-stack.md) owns implementation
> choices.
>
> **Convention:** **Decided** — settled semantics. **Direction** — proposed
> implementation shape. **Open** — unresolved implementation detail.
> [session-lifecycle.md](session-lifecycle.md) owns when session principals are
> retained, repaired, revoked, or left for cleanup, while
> [runtime-isolation.md](runtime-isolation.md) owns how the scoped route and key
> enter a runtime.

---

## The rule

**Decided.** P integrates an existing model gateway. P does not implement,
translate, normalize, or version model APIs.

Bifrost owns:

- inference API paths and protocol compatibility;
- streaming, cancellation, and request/response handling;
- provider credentials and upstream connections;
- model alias resolution, routing, limits, and usage accounting.

P owns:

- the configured Bifrost endpoint and authenticated management connection;
- whether a project receives model access;
- one persisted gateway principal per session UUID;
- exposing only inference and filtered model discovery to sessions;
- principal lifecycle and session attribution.

Provider credentials, model catalogs, aliases, routing, limits, MCP, skills,
and virtual-key policy are configured through Bifrost's native service. P does
not duplicate them in its own configuration. Repository-controlled
configuration cannot select or widen model access.

## Phases

**Decided.** Phases describe which existing Bifrost interface P exposes and
tests. They do not define an API implemented by P.

| Phase | Exposed Bifrost interface | Acceptance client |
|---|---|---|
| **1 — v1** | OpenAI-compatible | Codex |
| **2** | Anthropic-compatible, in addition to phase one | Claude Code |

If Bifrost adds, removes, or changes endpoints inside one interface, that is a
Bifrost compatibility matter. P's integration verifies that its acceptance
clients work; P does not promise endpoint-by-endpoint emulation.

Until phase two, Claude Code uses an in-session subscription login or an
explicit secret-bearing filesystem grant from trusted project policy. Its
observability hooks remain a v1 integration and are independent of model
transport.

## First implementation: Bifrost

**Decided.** Bifrost is the model gateway for a
workstation, laptop, or Kubernetes-hosted P instance. One logical gateway
serves one P instance; running that service in Kubernetes does not by itself
change P's instance boundary or require Envoy AI Gateway.

It runs as an independently configured persistent service. Bifrost's dashboard,
management API, configuration store, and provider credentials remain Bifrost
state. P connects to its authenticated management API only for session virtual-
key lifecycle and validation. Sessions can reach only inference and their
filtered model catalog; Bifrost administration, dashboard, raw logs, and
provider credentials are unreachable from a session.

P's management seam is deliberately small:

```go
type BifrostIntegration interface {
    EnsureSessionKey(ctx context.Context, sessionUUID UUID, policyRef string) (VirtualKey, error)
    RevokeSessionKey(ctx context.Context, sessionUUID UUID) error
    VerifyInference(ctx context.Context, key VirtualKey) error
}
```

Inference forwarding is not a method on this interface. Clients speak directly
to Bifrost; P manages policy and lifecycle around that data plane.

## Bifrost capability map

Bifrost is broader than the small management seam P needs. Its native open
source surface includes:

| Area | Native capability | P posture |
|---|---|---|
| Inference protocols | Unified, OpenAI-compatible, Anthropic-compatible, and Gemini-compatible APIs, including streaming and model discovery | Use OpenAI-compatible first and Anthropic-compatible second; expose only the operations required by acceptance clients |
| Inference operations | Responses, chat and text completions, embeddings, images, audio, files, batches, and provider-dependent operations | Allow only tested agent operations; the remainder is not session-reachable merely because Bifrost implements it |
| Providers | Hosted providers including OpenAI, Anthropic, OpenRouter, Bedrock, Azure, and Vertex, plus OpenAI-compatible and local endpoints such as Ollama and vLLM | Configure only in Bifrost; P treats the result as opaque broker policy |
| Models and routing | Model catalog, pricing, static and conditional aliases, weighted routing, retries, provider/key load balancing, and fallback chains | Configure only in Bifrost; P does not reproduce an alias or fallback catalog |
| Governance | Virtual keys, provider/model/key allowlists, expiry, budgets, token/request limits, and optional team/customer hierarchy | Use one mandatory persisted virtual key per model-enabled session; reference a Bifrost-owned project policy |
| Administration | Configuration API, provider/key management, governance API, dashboard, and file- or database-backed configuration | Host/control-plane only; never expose to a session |
| Observability | Request metadata, token and cost accounting, latency, retries, routing decisions, logs, Prometheus, and OpenTelemetry | Retain metadata with content logging disabled by default; raw logs remain host-only |
| MCP gateway | MCP client and server, HTTP/SSE/stdio upstreams, authentication, tool aggregation, filtering, execution, and per-virtual-key tool restrictions | Valuable adjacent capability; expose a separately filtered MCP route, never the administrative API or unrestricted host-side stdio tools |
| MCP Agent Mode | A gateway-owned loop that automatically executes explicitly approved tools and calls the model again until completion or a depth limit | Keep out of interactive coding sessions initially because Codex and Claude Code already own their agent and approval loops; evaluate only as a separately designed future workflow |
| MCP Code Mode | Four discovery/execution meta-tools and a restricted Starlark environment for orchestrating many MCP tools without sending every schema and intermediate result through the model | Later opt-in for large tool catalogs; generated code does not make the underlying tools safe, so normal allowlists and isolation still apply |
| Skills Repository | `SKILL.md` plus supporting files, immutable SemVer versions, a selected served version, APIs, and Codex/Claude Code marketplace output | Strong candidate for a Git-backed, instance-local skills distribution service; Git remains source of truth and Bifrost serves synchronized immutable versions |
| Prompt and extension features | Prompt repository/playground, semantic cache, Go/WASM plugins, mocker, and external observability integrations | Not part of the initial P integration; adopt only behind an explicit design and privacy review |
| Deployment | Standalone process/container, persistent SQLite or PostgreSQL stores, Helm/Kubernetes deployment, and an embeddable Go SDK | Use an external service boundary; standalone locally and Helm/ordinary Kubernetes service when the P instance runs there |

The [Bifrost overview](https://docs.getbifrost.ai/overview),
[configuration schema](https://docs.getbifrost.ai/deployment-guides/config-json/schema-reference),
[MCP overview](https://docs.getbifrost.ai/mcp/overview), and
[Skills Repository](https://docs.getbifrost.ai/features/skills-repository)
are the upstream capability references. Their existence does not make every
route part of P's session-facing contract.

### Adjacent skills and MCP direction

**Direction.** Bifrost can be more than P's inference gateway without becoming
P's control plane:

```text
Git-managed skill sources ──sync/publish──► Bifrost Skills Repository
                                               │ versioned marketplace
session harness ◄──────────────────────────────┘

session harness ──filtered MCP──► Bifrost MCP Gateway ──► approved MCP servers
```

Skills and MCP are separate capabilities and routes. A session's permission to
perform inference does not imply permission to download every skill or call
every tool. Any future P integration references Bifrost-owned policy from
trusted project configuration, and the sandbox-facing service exposes only the
associated serving and MCP routes. Git remains authoritative for P-managed
skills; the Bifrost repository is a local versioned publisher, not a
replacement for Git.

Agent Mode and Code Mode are not required to use the MCP gateway. The initial
shape leaves the interactive harness in control of tool selection, approval,
conversation state, and streaming.

## Gateway principals

**Decided.** Every session in a model-enabled project receives one Bifrost
virtual key. A project without model access receives no key and does not depend
on Bifrost availability.

The virtual key is a local, revocable capability, not an upstream credential.
It supplies model restrictions, attribution, limits, and individual revocation
without making P an HTTP proxy. A process inside the session may read it, but
it grants only the authority intentionally assigned to that runtime.

The principal is bound to the immutable session UUID:

- branch rename keeps it because session identity is unchanged;
- stop and later start keep it;
- discard and delete revoke it;
- startup reconciliation repairs or reports missing/orphaned principals.

P persists the virtual-key ID and token with the session UUID. Bifrost remains
authoritative for the key's policy and validity. P redacts the token from logs,
diagnostics, operation records, and ordinary RPC responses, delivers it only to
that session runtime, requests revocation when the session ends, and removes
its stored copy.

The session never receives the OpenRouter, OpenAI, or local-provider credential
held by Bifrost.

## Models and configuration

**Decided.** Sessions see the catalog allowed by their Bifrost virtual key.
Bifrost configuration is authoritative for providers, models, aliases, routes,
limits, and fallbacks.

```text
# Trusted host configuration; concrete file syntax is implementation-owned.
bifrost.endpoint = "http://127.0.0.1:8080"
projects["lgvo/p"].modelAccess.enabled = true
projects["lgvo/p"].modelAccess.policy = "billing-agents"
```

V1 grants model access only at project scope. Every session in the project uses
the same referenced Bifrost policy but receives its own key. The general grant
namespace reserves `{project}/{branch}` for future granularity; P does not
implement branch-specific model grants in v1.

How the pinned Bifrost release issues a virtual key from an existing policy
reference is an integration validation, not a second P model schema. If the
native API cannot preserve that ownership boundary, model access remains gated
until the adapter can do so without duplicating broker configuration.

## OpenRouter and local models

**Decided.** OpenRouter is the default hosted Bifrost upstream. Bifrost resolves
the permitted alias to an OpenRouter model; OpenRouter owns provider selection
and provider-level failover. P does not enable cross-model fallback by default
because it can change behavior, capabilities, and cost.

Ollama, vLLM, and equivalent trusted endpoints can sit behind the same gateway.
Bifrost reaches the configured local endpoint; sessions do not gain general
host/LAN access or authority to select a URL. A keyless upstream still uses a
runtime virtual key at Bifrost for identity, policy, and attribution.

## Usage and privacy

P retains the session-to-virtual-key association required for lifecycle. Usage,
routing, model, token, latency, and cost records remain Bifrost-owned. P does
not retain prompts, responses, tool contents, authorization headers, or raw
request bodies. Bifrost content logging is disabled by default; enabling it is
an explicit departure from P's privacy posture.

## Failure posture

- Principal creation and revocation are idempotent by immutable session UUID.
- A model-enabled session is not ready until its key is persisted and inference
  access is verified.
- A session without model access is independent of Bifrost readiness.
- P does not replay an inference request after ambiguous delivery.
- Gateway failure affects model access, not Git, attachment, RPC, or runtime
  existence.
- Routing and fallback behavior are whatever the referenced Bifrost policy
  explicitly defines; P adds none.

## Envoy AI Gateway capability map

Envoy AI Gateway overlaps with Bifrost's inference and MCP gateway surfaces,
but is optimized for a different deployment problem: a horizontally scaled,
Kubernetes-native traffic data plane built on Envoy Gateway.

| Area | Native capability | Relevance to P |
|---|---|---|
| Protocol and providers | OpenAI- and Anthropic-compatible endpoints, provider translation, hosted cloud backends, and OpenAI-compatible services | Can replace Bifrost's inference data plane if a later deployment needs its infrastructure model; it is not needed for protocol compatibility alone |
| Kubernetes control plane | `AIGatewayRoute`, `AIServiceBackend`, `BackendSecurityPolicy`, `GatewayConfig`, `MCPRoute`, and `QuotaPolicy` resources reconciled through Gateway API | Strong fit when model access must be operated with the rest of a Kubernetes network and policy stack |
| Traffic management | Model virtualization, weighted routing, fallback, retries, header/body mutation, hostname/tenant routing, and Envoy connection/circuit-breaking behavior | Stronger infrastructure primitive for shared multi-tenant or high-availability ingress; overlaps with Bifrost routing and must not be enabled twice without explicit ownership |
| Self-hosted inference | Gateway API Inference Extension and `InferencePool` endpoint selection using model availability and live serving signals such as queue or KV-cache state | Primary reason for P to prefer Envoy later when routing across a fleet of vLLM or other GPU-serving endpoints |
| Security | TLS/mTLS, JWT, OIDC, API keys, IP policy, cloud workload identity, and external authorization inherited from or integrated with Envoy Gateway | Useful for shared Kubernetes ingress and identity integration; more infrastructure-oriented than Bifrost virtual keys |
| Limits | Distributed request/token rate limiting and token quota policies, commonly backed by Redis, with CEL-based cost expressions | Useful for limits shared across replicas; Bifrost remains simpler for per-session dollar accounting and local administration |
| Observability | Prometheus, OpenTelemetry/OpenInference tracing, structured access logs, AI/MCP metadata, and body redaction | Fits an existing cluster observability stack; unlike Bifrost, it is not primarily a turnkey local usage dashboard |
| MCP | MCP server multiplexing, tool routing/filtering, OAuth, session handling, CEL/external authorization, and traffic observability | Strong policy gateway; it does not replace Bifrost Skills Repository or its Agent/Code Mode execution features |
| Local operation | `aigw run` standalone mode on Linux/macOS | Useful for development and configuration testing, but Bifrost remains the selected workstation product |

See Envoy's [capability index](https://aigateway.envoyproxy.io/docs/capabilities/),
[architecture](https://aigateway.envoyproxy.io/docs/concepts/architecture/system-architecture/),
and [standalone mode](https://aigateway.envoyproxy.io/docs/cli/aigwrun/).

## Kubernetes evolution

The stable session-facing inference contract remains gateway endpoint +
principal + allowed aliases. Kubernetes changes placement and operations, not
that contract.

### Stage K1: Bifrost on Kubernetes

**Direction and preferred first Kubernetes shape.** Bifrost is capable of
running in Kubernetes and has an official Helm deployment. A P instance can
move there without changing gateway implementations:

```text
P daemon/control service
    │ management API, trusted network
    ▼
Bifrost service ───────────────► OpenRouter / OpenAI / local model service
    ▲
    │ restricted inference, model discovery, skills, and/or MCP routes
session runtime pods
```

- One logical Bifrost service belongs to the P instance.
- Provider credentials live in cluster secret facilities and are readable by
  Bifrost, not session pods.
- Bifrost configuration and logs use persistent storage; PostgreSQL is the
  natural choice if the deployment outgrows a single SQLite-backed replica.
- Kubernetes `NetworkPolicy` and a restricted service/proxy expose only the
  selected inference, skill-serving, and MCP routes to session namespaces.
- Bifrost's dashboard and `/api/*` management surface are reachable only from
  the trusted P control plane or an explicit administrator path.
- Bifrost continues to own aliases, virtual keys, routing, limits, and usage;
  Kubernetes does not duplicate them.

This stage is sufficient for a Kubernetes execution backend that still has
one P instance and moderate gateway load. Bifrost's Helm support does not imply
that P instances federate or that runtime state starts moving between them.

### Stage K2: Envoy in front of Bifrost

**Direction, only when justified by cluster requirements.** Envoy AI Gateway
may become the sandbox-facing ingress while Bifrost remains the product-level
gateway and repository:

```text
session pods
    │
    ▼
Envoy AI Gateway ──restricted AI route──► Bifrost ──► hosted/local providers
    │                                         ├── virtual keys and accounting
    │                                         └── skills repository
    └── cluster TLS/auth, distributed limits, telemetry
```

This is appropriate for shared ingress, cluster identity, distributed limits,
or Envoy-standard observability. Envoy must forward the runtime capability and
must not broaden Bifrost's filtered catalog. Exactly one layer owns each retry,
fallback, alias rewrite, and quota to prevent duplicate attempts and confusing
accounting. The default split is Envoy for transport/edge policy and Bifrost
for model selection, session principals, cost accounting, skills, and any
Bifrost-hosted MCP surface.

### Stage K3: Envoy-native inference data plane

**Direction, not an assumed destination.** If P operates a horizontally scaled
self-hosted inference fleet, Envoy may replace Bifrost for inference while
Bifrost remains an optional adjacent skills service:

```text
session pods ──► Envoy AI Gateway ──► InferencePool / cloud providers

session pods ──► Bifrost serving route     # optional skills repository
```

At that point an Envoy integration must implement P's principal,
filtered-catalog, revocation, and usage seam using Envoy/cluster identity and
policy resources. This is a deliberate implementation change, not a reason to
design v1 around Envoy. MCP aggregation must also have one owner; P should not
present two overlapping tool catalogs to the same session.

Portkey remains a possible managed/hybrid implementation. Neither Envoy nor
Portkey is a v1 dependency.

## Acceptance

Phase one must prove:

1. P can obtain, persist, use, and revoke one Bifrost virtual key per
   model-enabled session UUID without copying Bifrost policy;
2. a session sees only the catalog allowed by that key;
3. Codex works through Bifrost's OpenAI-compatible interface to OpenRouter and
   one local model endpoint;
4. sessions cannot reach Bifrost administration or upstream credentials;
5. usage metadata works with content logging disabled;
6. branch rename, stop, discard/delete, and restart reconciliation preserve the
   principal lifecycle described above;
7. no unconfigured cross-model fallback occurs.

Phase two applies the same requirements to Bifrost's Anthropic-compatible
interface and Claude Code. Protocol correctness remains Bifrost's
responsibility.
