# Nexora codebase reference

This document describes the implementation that is currently in this repository. It is intended to answer four questions for a new engineer:

1. What runs in each process and container?
2. How does a request move through the system?
3. Which database, Redis, and HTTP contracts connect the parts?
4. Where are the important operational and security boundaries?

The high-level design is in [architecture.md](architecture.md), and the short public API list is in [api.md](api.md). This file is the implementation-level companion to those documents.

## 1. Product and execution model

Nexora is a multi-tenant operation coordinator. A tenant submits a JSON operation containing a task type, target URL, and payload. Nexora stores the operation, places it on one logical Redis Stream, and executes it in one of two modes:

| Mode | Executor | Network location | How it receives work |
|---|---|---|---|
| `PRIVATE` | `enterprise-worker` | Customer network or the packaged demo network | Outbound HTTP poll through the gateway |
| `CLOUD` | `worker` | Nexora control-plane network | Redis consumer group |

The default UI and demo path use `PRIVATE`. The cloud worker is optional and is enabled by the `cloud-worker` Compose profile.

The control plane has three domain services:

- Customer & Access: users, tenants, roles, JWTs, and API keys.
- Operations & Processing: operation records, DBOS publication, Redis dispatch, leases, retries, cancellation, redrive, and event outbox creation.
- Usage & Webhooks: usage ledger, quotas, SSE events, webhook endpoints, signed webhook delivery, and webhook retries.

The gateway is an edge process around those services. It authenticates requests, applies the shared Redis rate limit, proxies tenant-scoped routes, and hosts the AI assistant endpoint.

## 2. Repository map

### Runtime entry points

| Path | Binary/process | Responsibility |
|---|---|---|
| `cmd/gateway/main.go` | `gateway` | Public HTTP API, authentication delegation, rate limiting, proxying, readiness, AI assistant |
| `cmd/access/main.go` | `access` | Login, JWT/API-key verification, API-key creation, tenant lookup, tenant administration, bootstrap seeding |
| `cmd/operations/main.go` | `operations` | Operation CRUD, DBOS workflow, Redis stream, private/cloud execution protocols, leases, retries, operation events |
| `cmd/usage/main.go` | `usage` | Monthly usage, usage event ingestion, webhooks, SSE, webhook outbox delivery |
| `cmd/worker/main.go` | `worker` | Optional cloud executor using Redis Streams and the internal operations API |
| `cmd/enterprise-worker/main.go` | `enterprise-worker` | Outbound-only private executor using the public gateway and a tenant API key |
| `cmd/acme/main.go` | `acme`/`lorem` demo server | Simulated customer job endpoint, webhook receiver, and demo traffic generators |

### Shared packages

| Path | Contents |
|---|---|
| `internal/domain/state.go` | Operation status constants, allowed transition map, terminal-state check, bounded retry delay |
| `internal/contracts/contracts.go` | JSON contracts shared by services and workers: principals, queue envelopes, operation events, and problem responses |
| `internal/platform/config.go` | Environment-backed configuration and defaults |
| `internal/platform/http.go` | Echo server defaults, CORS, request IDs, panic recovery, problem responses, internal/tenant middleware, server start |
| `internal/platform/resources.go` | PostgreSQL pool and Redis client creation plus health checks |
| `internal/platform/security.go` | SHA-256 and HMAC helpers |

### Persistence and deployment

| Path | Purpose |
|---|---|
| `migrations/001_init.sql` | `access`, `operations`, and `usage` schemas and primary business tables |
| `migrations/002_operation_event_outbox.sql` | Durable operations-to-usage event outbox |
| `docker-compose.yml` | PostgreSQL, Redis, Go services, workers, demo servers, and both Next.js UIs |
| `.env.example` | Local configuration template |
| `Makefile` | Development, tests, formatting, linting, migration, and smoke-test commands |
| `scripts/smoke.sh` | HTTP smoke test from gateway readiness through operation submission |
| `go.mod`, `go.sum` | Go module and dependency lock data |

### Web applications

| Path | Purpose |
|---|---|
| `web/customer/app/page.tsx` | Customer login, tenant summary, operation submission, filtering, pagination, cancellation, redrive, and AI assistant |
| `web/customer/app/layout.tsx` | Customer document shell and metadata |
| `web/customer/app/styles.css` | Customer console layout, controls, table, status badges, responsive rules |
| `web/customer/app/assistant.css` | Assistant panel styling and animated loading dots |
| `web/admin/app/page.tsx` | Admin login, overview metrics, tenant directory, operation table, usage table, and tenant creation |
| `web/admin/app/layout.tsx` | Admin document shell and metadata |
| `web/admin/app/styles.css` | Admin console layout and responsive rules |
| `web/*/package.json` | Next.js, React, TypeScript, ESLint, build, dev, and test scripts |
| `web/*/Dockerfile` | Production image build for each UI |

## 3. Process topology and ports

Compose puts all services on the `nexora` network. Only selected demo/UI and gateway ports are published to the host.

| Container | Internal port | Host port | Notes |
|---|---:|---:|---|
| `postgres` | 5432 | not published | PostgreSQL 17; initialization scripts are mounted read-only |
| `redis` | 6379 | not published | Redis 8 with append-only persistence |
| `access` | 8081 | not published | Access service |
| `operations` | 8082 | not published | Operations service; calls `usage:8083` for event delivery |
| `usage` | 8083 | not published | Usage and webhooks service |
| `gateway` | 8080 | `127.0.0.1:8080` | Public API and readiness endpoint |
| `customer-web` | 3000 | `127.0.0.1:3000` | Customer console |
| `admin-web` | 3000 | `127.0.0.1:3001` | Admin console |
| `acme` | 8090 | `127.0.0.1:8090` | Acme demo target and webhook receiver |
| `lorem` | 8091 | `127.0.0.1:8091` | Second demo target using the same Acme binary |
| `worker` | no listener | none | Optional; `cloud-worker` profile, two replicas in Compose |
| `enterprise-worker` | no listener | none | Outbound-only Acme worker |
| `lorem-enterprise-worker` | no listener | none | Outbound-only Lorem worker |

The Go image is built from `build/go.Dockerfile`; the UI images are built independently from their `web/*` directories. The Compose file assumes `.env` exists. For a local start:

```bash
cp .env.example .env
docker compose up --build
```

The default credentials are intentionally development-only. Replace the database password, JWT secret, internal secret, admin password, API keys, and webhook secrets before sharing a deployment.

## 4. Configuration reference

`internal/platform/config.go` reads the following values. Every Go service calls `platform.LoadConfig`, so most values are available to every process even when a particular binary only uses a subset.

| Variable | Default | Used for |
|---|---|---|
| `PORT` | `8080` | Echo listen port; Compose overrides this per service |
| `DATABASE_URL` | Local PostgreSQL URL | Application PostgreSQL pool |
| `DBOS_SYSTEM_DATABASE_URL` | `DATABASE_URL` | DBOS system/checkpoint database |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis connection |
| `JWT_SECRET` | Development placeholder | HMAC JWT signing and verification |
| `INTERNAL_SECRET` | Development placeholder | Service-to-service internal HTTP authentication |
| `ACCESS_URL` | `http://localhost:8081` | Gateway target for Access |
| `OPERATIONS_URL` | `http://localhost:8082` | Gateway/worker target for Operations |
| `USAGE_URL` | `http://localhost:8083` | Gateway/worker target for Usage |
| `OPENROUTER_API_KEY` | empty | Enables live AI explanations and assistant responses |
| `OPENROUTER_PRIMARY_MODEL` | `google/gemini-2.5-flash` | First OpenRouter model attempted |
| `OPENROUTER_FALLBACK_MODEL` | `meta-llama/llama-3.3-70b-instruct` | Second model attempted after a primary failure |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | OpenRouter `HTTP-Referer` value |
| `ADMIN_EMAIL` | `admin@nexora.local` | Bootstrapped admin login |
| `ADMIN_PASSWORD` | `ChangeMe123!` | Bootstrapped admin password |
| `REQUEST_TIMEOUT_SECONDS` | `30` | Gateway/worker HTTP client timeout |
| `ACME_API_KEY` | from `.env.example` | Acme demo tenant API key and enterprise worker credential |
| `LOREM_API_KEY` | from `.env.example` | Lorem demo tenant API key and enterprise worker credential |
| `ACME_WEBHOOK_SECRET` | from `.env.example` | Optional Acme receiver-side signature validation |
| `NEXORA_API_URL` | `http://localhost:8080` | Demo/enterprise-worker gateway URL |
| `ENTERPRISE_WORKER_API_KEY` | required by worker | API key used by the private worker |
| `ENTERPRISE_TARGET_URL` | optional | Worker target override, useful for the packaged demo |
| `NEXT_PUBLIC_API_URL` | `http://127.0.0.1:8080` | Browser-visible gateway URL baked into the Next.js build |

`LOG_LEVEL` appears in `.env.example`, but the current Go code does not read it.

## 5. Shared HTTP behavior

`platform.NewServer` is used by every Go HTTP process. It installs:

- `Secure` middleware.
- CORS for `http://localhost:3000` and `http://localhost:3001`.
- Allowed request headers: content negotiation headers, `Authorization`, `X-API-Key`, `Idempotency-Key`, and `X-Request-ID`.
- Request ID middleware.
- Panic recovery and structured request logging.
- `ProblemHandler`, which emits an RFC 7807-like JSON body.
- `GET /healthz`, returning `{ "status": "ok", "service": "..." }`.

### Request IDs

`RequestID` accepts a caller-supplied `X-Request-ID` or generates 12 random bytes encoded as hexadecimal. It stores the value in Echo context, returns it in the response header, forwards it through the gateway to proxied services, and includes it in problem responses and request logs.

### Error shape

Errors are normalized to:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "human-readable detail",
  "instance": "/v1/operations",
  "request_id": "..."
}
```

The exact `detail` is produced by the handler. Upstream failures generated by the gateway use `502 Bad Gateway` and do not expose the upstream error body.

### Tenant context headers

After authentication, the gateway sets these headers before calling a service:

| Header | Meaning |
|---|---|
| `X-Nexora-Tenant-ID` | Verified tenant UUID |
| `X-Nexora-User-ID` | JWT subject, when the credential is a user JWT |
| `X-Nexora-Role` | `customer`, `admin`, or `service` |

The downstream services use these headers as trusted gateway-provided context. Direct service access should therefore be network-restricted; the internal service endpoints additionally require `X-Internal-Secret`.

## 6. Authentication and authorization flow

### Login

1. The browser posts email and password to `POST /v1/auth/login`.
2. The gateway proxies the request to Access without requiring a credential.
3. Access lowercases and trims the email, loads the user, and checks the bcrypt password hash.
4. Access signs an HS256 JWT with issuer `nexora`, an eight-hour expiry, the user UUID as `sub`, `tenant_id`, and `role`.
5. The gateway returns the Access response unchanged.

The response contains `access_token`, `expires_in` (`28800`), `tenant_id`, and `role`.

### Authenticated request

1. The gateway reads and restores the request body so proxying does not consume it.
2. It sends the caller's bearer token or API key to Access `POST /internal/verify` with `X-Internal-Secret`.
3. Access validates the JWT or SHA-256 API-key hash, updates API-key `last_used_at`, and returns a principal.
4. The gateway replaces any caller-supplied tenant/user/role headers with the verified principal.
5. The gateway applies the tenant rate limit and then proxies the request.

Bearer credentials must use the `Bearer <token>` format. API keys are stored as SHA-256 hex digests; the raw key is returned only when it is created. API-key creation is tenant-scoped and returns the secret once.

### Rate limiting

The gateway uses a Redis Lua script to increment `rate:<tenant-id>:<unix-minute>` and set a 70-second expiry on first use. The default limit is 120 requests per minute per tenant. Admins receive a 600-request limit. Responses include `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and, after rejection, `Retry-After: 60`.

## 7. Operation state machine

`internal/domain/state.go` defines the canonical status names and the intended transition map:

| From | Allowed next states |
|---|---|
| `PENDING` | `QUEUED`, `CANCELLED` |
| `QUEUED` | `RUNNING`, `CANCELLED` |
| `RUNNING` | `SUCCEEDED`, `FAILED`, `RETRYING`, `DEAD_LETTERED`, `CANCELLED` |
| `FAILED` | `RETRYING`, `DEAD_LETTERED`, `CANCELLED` |
| `RETRYING` | `QUEUED`, `RUNNING`, `DEAD_LETTERED`, `CANCELLED` |

`SUCCEEDED`, `DEAD_LETTERED`, and `CANCELLED` are terminal. `CanTransition` checks the map and `IsTerminal` checks the three terminal values. The Operations service uses these helpers when deciding whether skipped stream messages are permanently unclaimable and when reporting cancellation conflicts; SQL `WHERE status ...` predicates still enforce atomic state changes.

### Retry delay

`RetryDelay(attempt)` clamps attempts to 1 through 6 and returns `2^attempt` seconds: 2, 4, 8, 16, 32, or 64 seconds. The Operations service uses it for worker retry scheduling.

## 8. Operation submission and dispatch

### `POST /v1/operations`

The Operations service validates:

- `task_type` and `target_url` are present.
- `target_url` begins with `http://` or `https://`.
- Empty payload becomes `{}`.
- `max_attempts` defaults to 3 and must be 1–10.
- `execution_mode` defaults to `PRIVATE` and must be `PRIVATE` or `CLOUD`.
- `Idempotency-Key` is required and must be at most 200 characters.

It then reads the tenant's monthly quota and concurrency limit. Current usage is the monthly sum of `usage.ledger.units`; current concurrency is the count of `RUNNING` operations. Quota exhaustion returns `402`; concurrency exhaustion returns `429`.

The insert uses `ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`. The service reads back the row by tenant and idempotency key. A new submission returns `202 Accepted`; a duplicate returns `200 OK` with the original operation. The operation UUID is generated before the insert, so it is not returned as the idempotency result when a conflict occurs.

`scheduled_for` is carried in the queue envelope as `not_before`. `deadline` is carried as an absolute timestamp.

### DBOS publication

For a new operation, Operations starts a DBOS workflow with ID `operation:<operation-id>`. The workflow:

1. Sleeps until `not_before` when it is in the future.
2. Runs the `enqueue-redis-operation` DBOS step.
3. Changes `PENDING` or `RETRYING` to `QUEUED`.
4. Inserts a transition-log row.
5. Adds a JSON `QueueEnvelope` to Redis Stream `nexora:operations`.

The DBOS step is the durable checkpoint around Redis publication. This repository deliberately registers no DBOS Queue; there is one logical Redis stream.

### Queue envelope

The shared `contracts.QueueEnvelope` has these fields:

```json
{
  "operation_id": "uuid",
  "tenant_id": "uuid",
  "task_type": "acme.ticket.process",
  "target_url": "http://acme:8090/work",
  "payload": {},
  "attempt": 0,
  "max_attempts": 3,
  "deadline": "2026-09-19T10:00:00Z",
  "execution_mode": "PRIVATE",
  "not_before": "2026-09-19T09:00:02Z"
}
```

`deadline` and `not_before` are omitted when absent.

## 9. Worker execution

### Enterprise/private worker

`cmd/enterprise-worker/main.go` is intentionally outbound-only:

1. It requires `ENTERPRISE_WORKER_API_KEY`.
2. It calls `POST /v1/worker/poll` through the configured `NEXORA_API_URL`.
3. It sleeps briefly after network errors and continues polling.
4. For a lease returned by poll, it starts a 15-second heartbeat loop.
5. It POSTs the operation payload to `target_url`, or to `ENTERPRISE_TARGET_URL` when set.
6. It sends `Idempotency-Key` and `X-Nexora-Operation-ID` to the target.
7. It reports success to `/v1/worker/operations/:id/complete`, or failure to `/fail`.
8. It stops the heartbeat after the target call finishes.

The HTTP client timeout is 45 seconds. Non-2xx target responses are categorized as failures. Network errors, 408, 429, and 5xx are retryable; other HTTP failures are permanent. If no OpenRouter key is available, it writes a deterministic fallback explanation.

### Private poll semantics

`POST /worker/poll` is tenant-scoped and requires `worker_id`. The service stores a Redis cursor at `private-worker-cursor:<tenant-id>`, reads up to 50 stream messages with `XREAD`, skips messages for other tenants or `CLOUD` operations, and atomically claims a matching operation if the tenant concurrency limit allows it. The returned envelope has its attempt incremented in the response.

### Cloud worker

`cmd/worker/main.go` uses the Redis consumer group `workers`:

1. Operations creates the `workers` group on startup.
2. The worker reads new messages with `XREADGROUP` and consumer-specific identity.
3. It rejects expired deadlines as `DEADLINE_EXCEEDED`.
4. It ignores non-`CLOUD` envelopes after acknowledging them.
5. It claims the operation through the internal Operations endpoint.
6. It starts heartbeats every 15 seconds, invokes the target, and reports completion/failure.
7. It emits usage events through the internal Usage endpoint and acknowledges the stream message.

The cloud worker uses a 45-second lease, a request timeout inherited from `REQUEST_TIMEOUT_SECONDS`, and the same retryability classification as the enterprise worker.

### Lease ownership and recovery

Claims set `status=RUNNING`, increment `attempt`, set `lease_owner`, set a lease expiry, and increment `version`. Heartbeats only extend a lease when the worker ID still owns the running operation. Completion and failure also require the worker ID to own the lease, preventing a stale worker from overwriting a newer attempt.

Operations scans expired running leases every 10 seconds. If the attempt budget is exhausted, it marks the operation `DEAD_LETTERED` and records an event. Otherwise it marks it `RETRYING`, clears the lease, schedules a two-second recovery workflow, and lets DBOS publish it again.

## 10. Failure, retry, cancellation, and redrive

### Failure classification

Workers map target outcomes to:

| Condition | Error code | Retryable |
|---|---|---|
| Network/client construction failure | `NETWORK_ERROR` | yes |
| HTTP 408 | `TARGET_TIMEOUT` | yes |
| HTTP 429 | `TARGET_RATE_LIMITED` | yes |
| HTTP 5xx | `TARGET_SERVER_ERROR` | yes |
| Other non-2xx | `TARGET_REJECTED` | no |

`DEADLINE_EXCEEDED` is treated as non-retryable by the cloud worker. A retryable failure becomes `RETRYING` while `attempt < max_attempts`; otherwise it becomes `DEAD_LETTERED`.

### Cancellation

`DELETE /v1/operations/:id` changes a tenant-owned operation to `CANCELLED` when its current status is `PENDING`, `QUEUED`, `RUNNING`, `RETRYING`, or `FAILED`. It returns `204`. Missing, terminal, or otherwise ineligible operations return `409`.

Cancellation does not forcibly terminate an already-running target HTTP request. Lease ownership and the terminal status prevent a later completion from succeeding.

### Redrive

`POST /v1/operations/:id/redrive` is allowed only for `FAILED` and `DEAD_LETTERED` operations. It clears error fields, resets the attempt to zero, sets `RETRYING`, and launches a new workflow with ID `redrive:<operation-id>:<random-uuid>`. It returns `202`.

## 11. Usage events and webhook delivery

### Operations event outbox

Worker completion/failure paths and lease recovery create rows in `operations.event_outbox` using an idempotent `event_key`. The Operations dispatch loop runs once per second, takes up to 20 due pending events, and POSTs each event to Usage `/internal/events` with `X-Internal-Secret`.

Successful delivery marks the row `DELIVERED`. Failure increments the attempt, records the error, and retries with bounded exponential delay.

### Usage ledger

Usage accepts an `OperationEvent` containing `event_key`, `event_type`, `operation_id`, `tenant_id`, `units`, payload, and occurrence time. It inserts into `usage.ledger` with `ON CONFLICT(event_key) DO NOTHING`. A duplicate event therefore does not double charge. For a newly inserted event, the same transaction creates one webhook outbox row per enabled tenant endpoint.

`GET /v1/usage` sums the current month's ledger units and returns:

```json
{
  "used": 1,
  "quota": 10000,
  "remaining": 9999,
  "period_start": "2026-09"
}
```

### Webhook endpoints

Customers create endpoints with an HTTP(S) URL and a secret of at least 16 characters. The secret is stored in `usage.webhook_endpoints` and is not returned by the list endpoint. Delivery signs the exact JSON payload using HMAC-SHA256 and sends:

- `X-Nexora-Event`: event type.
- `X-Nexora-Delivery`: webhook outbox UUID.
- `X-Nexora-Signature`: `sha256=<lowercase hex digest>`.

The delivery loop runs once per second, claims up to 20 due records in a transaction with `FOR UPDATE SKIP LOCKED`, marks them `IN_FLIGHT`, and then delivers them outside the transaction. Failed deliveries return to `PENDING` with bounded exponential delay; after eight attempts they are marked `DEAD_LETTERED`.

### Server-sent events

`GET /v1/events` creates an in-memory buffered subscriber for the authenticated tenant. Events are sent as `event: operation` with JSON data. A comment keepalive is sent every 20 seconds. Subscribers are process-local; the durable source of truth is PostgreSQL, not the in-memory channel.

## 12. AI assistant and explanations

There are two AI paths:

1. The gateway assistant (`POST /v1/assistant`) loads the tenant's 20 most recent operations, creates a constrained prompt containing only that JSON context and the user's question, and calls OpenRouter with the primary then fallback model.
2. A worker explanation is generated after target success/failure. The cloud worker and enterprise worker have similar prompts and use the same primary/fallback model sequence.

The gateway assistant limits questions to 2,000 characters and responses to 500 tokens. Worker explanations cap the inserted result/error text before sending it to the model. All prompts instruct the model not to invent facts. Without an OpenRouter key, workers use a deterministic explanation; the gateway returns `503` for assistant requests.

## 13. Public API reference by route

The gateway exposes these routes under `/v1` after authentication unless noted otherwise.

| Method and path | Auth | Upstream/handler | Main behavior |
|---|---|---|---|
| `POST /auth/login` | none | Access | Issue customer/admin JWT |
| `GET /tenant` | tenant | Access | Return current tenant and limits |
| `POST /api-keys` | tenant | Access | Create and return a new API key once |
| `POST /operations` | tenant | Operations | Validate, quota-check, idempotently create, enqueue |
| `GET /operations` | tenant/admin | Operations | Tenant list or admin list; `limit` and `status` supported |
| `GET /operations/:id` | tenant | Operations | Tenant-scoped operation detail |
| `DELETE /operations/:id` | tenant | Operations | Cancel eligible operation |
| `POST /operations/:id/redrive` | tenant | Operations | Requeue failed/dead-lettered operation |
| `GET /usage` | tenant | Usage | Current month usage and remaining quota |
| `GET /events` | tenant | Usage | Tenant-scoped SSE stream |
| `GET /webhooks` | tenant | Usage | List endpoint metadata |
| `POST /webhooks` | tenant | Usage | Register signed webhook endpoint |
| `DELETE /webhooks/:id` | tenant | Usage | Delete tenant-owned endpoint |
| `POST /worker/poll` | API key | Operations | Claim one private operation |
| `POST /worker/operations/:id/heartbeat` | API key | Operations | Extend private lease |
| `POST /worker/operations/:id/complete` | API key | Operations | Complete private operation and emit event |
| `POST /worker/operations/:id/fail` | API key | Operations | Fail/retry/dead-letter private operation |
| `POST /assistant` | tenant | Gateway | Answer question from recent tenant operations |
| `GET /admin/tenants` | admin JWT | Access | List tenants |
| `POST /admin/tenants` | admin JWT | Access | Create tenant and initial customer user |
| `GET /admin/operations` | admin JWT | Operations | List operations across tenants |
| `GET /admin/usage` | admin JWT | Usage | Usage/quota per tenant |

The internal routes are not gateway routes. They are service-to-service endpoints protected by `X-Internal-Secret`:

| Service | Route | Caller |
|---|---|---|
| Access | `POST /internal/verify` | Gateway |
| Operations | `POST /internal/operations/:id/{claim,heartbeat,complete,fail}` | Cloud worker |
| Operations | `GET /internal/operations/:id` | Internal callers |
| Usage | `POST /internal/events` | Operations event dispatcher and cloud worker |

## 14. Database model

All business tables live in one PostgreSQL database but are separated into schemas.

### `access` schema

- `access.tenants`: tenant UUID, display name, unique slug, monthly quota, concurrency limit, creation time.
- `access.users`: user UUID, tenant, unique email, bcrypt password hash, `customer`/`admin` role, creation time.
- `access.api_keys`: tenant, name, non-secret prefix, unique SHA-256 key hash, last-use timestamp, revocation timestamp, creation time.

### `operations` schema

- `operations.operations`: operation identity, tenant, task type, execution mode, target URL, JSON payload, status, attempt budget, deadline, lease owner/expiry, result, error fields, AI explanation, idempotency key, optimistic version, timestamps.
- `operations.transition_log`: operation status transition history and reason.
- `operations.event_outbox`: durable operation events awaiting Usage ingestion, including unique event key, units, payload, retry state, and delivery timestamp.

Important indexes and constraints:

- Unique `(tenant_id, idempotency_key)` for tenant-local deduplication.
- `operations_tenant_created_idx` for tenant operation lists.
- `operations_lease_idx` for expired-lease scans.
- Unique `event_key` for event deduplication.

### `usage` schema

- `usage.ledger`: append-only tenant usage entries keyed by unique `event_key`, with operation ID, units, metadata, and creation time.
- `usage.webhook_endpoints`: tenant-owned URL, secret, enabled flag, and creation time.
- `usage.webhook_outbox`: one event per endpoint, with delivery status, retry state, and a unique `(endpoint_id, event_type, operation_id)` key.

`migrations/002_operation_event_outbox.sql` is separate because it adds the durable bridge from Operations to Usage. DBOS also maintains its own system tables in the configured DBOS database.

## 15. Customer console

`web/customer/app/page.tsx` is a single client component.

### Authentication and polling

- Stores the JWT in `localStorage` under `nexora_customer_token`.
- Starts with Acme demo credentials in the form fields.
- Requires the returned role to be `customer`.
- Refreshes tenant, operations, usage, and webhook data immediately and every three seconds.
- Converts fetch failures into a gateway/Docker troubleshooting message.

### Operation controls

The mode selector maps to the Acme/Lorem demo target payload values `normal`, `fail`, `reject`, and `slow`. Submission always uses `PRIVATE`, creates an independent UUID idempotency key, and selects target/task names from the tenant slug. The table:

- Filters by all operation statuses, execution mode, and created-time window.
- Displays eight operations per page.
- Shows AI explanation and error message when present.
- Offers Cancel for `PENDING`, `QUEUED`, `RUNNING`, and `RETRYING`.
- Offers Redrive for `FAILED` and `DEAD_LETTERED`.

The webhook management JSX is currently present but commented out. The component still contains the state and request functions for it.

### Styling

The customer styles define the blue Nexora visual language, status colors, usage bar, table/panel layout, mobile breakpoints at 800px and 700px, and animated assistant dots.

## 16. Admin console

`web/admin/app/page.tsx` is a single client component.

- Stores the JWT under `nexora_admin_token`.
- Requires the `admin` role after login.
- Refreshes tenants, up to 50 operations, and usage every five seconds.
- Calculates running, queued, succeeded, failed/dead-lettered, total usage, quota utilization, average concurrency, and the five most utilized tenants.
- Marks the dashboard as needing attention when more than 20 operations are failed/dead-lettered or utilization is at least 90%.
- Provides overview, tenants, operations, and usage views.
- Filters operations by status, tenant, and created-time window.
- Uses six rows per page in overview and twelve in the full operations view.
- Creates tenants through a modal form. The Access service applies defaults when quota or concurrency values are zero and creates the initial customer user in one database transaction.

## 17. Demo server and demonstration flows

`cmd/acme/main.go` is both a target service and a test harness.

### Target endpoints

- `POST /work`: increments a request counter, optionally simulates a 500 (`mode=fail`), 422 (`mode=reject`), or 35-second slow request (`mode=slow`), then returns an accepted JSON result.
- `POST /webhook`: optionally validates `X-Nexora-Signature` using `ACME_WEBHOOK_SECRET`; returns 503 when webhooks are toggled off.
- `GET /healthz`: common platform health endpoint.

### Demo endpoints

- `GET /demo/normal`: submit one successful operation.
- `GET /demo/burst?count=25`: submit up to 100 operations with unique idempotency keys.
- `GET /demo/idempotent`: submit the same idempotency key twice and return both responses.
- `POST /demo/webhook/toggle`: enable or disable the receiver with `{ "enabled": false }`.
- `GET /demo/quota-exhaust?count=20`: submit until the first rejected operation.
- `GET /demo/stats`: return demo request count and webhook-enabled state.

`cmd/acme/main.go` sends API requests with the Acme API key, `Idempotency-Key`, and the fixed demo task/target values.

## 18. Tests and verification

### Go tests

- `internal/domain/state_test.go`: terminal immutability, expected lifecycle transitions, and retry-delay bounds.
- `internal/platform/security_test.go`: SHA-256 non-disclosure and HMAC signature validation.
- `cmd/gateway/main_test.go`: verifies that proxy responses do not duplicate gateway CORS headers.

### Commands

```bash
make test       # Go tests plus both web package test commands
make test-unit  # Go packages only
make fmt        # gofmt on cmd/internal/tests paths
make lint       # go vet plus both web ESLint commands
make migrate    # run the Compose migration container
make smoke      # execute scripts/smoke.sh
docker compose config --quiet
```

The web package `test` scripts currently invoke `node --test`; no dedicated frontend test files are present in the repository. The meaningful UI verification is therefore lint/build plus the end-to-end smoke/demo paths.

## 19. Security and operational notes

### Positive controls implemented

- Passwords use bcrypt.
- API keys are hashed before persistence.
- JWT issuer, expiry, and signing method are checked.
- Service-to-service routes require a shared internal secret.
- Tenant context is derived from verified credentials rather than request JSON.
- Operation reads, cancellation, redrive, and webhook deletion include tenant predicates.
- Worker completion/failure requires lease ownership.
- Usage and webhook delivery are idempotent by database keys.
- Webhook payloads are HMAC signed.
- Request bodies, target responses, AI context, and webhook responses have size limits in the worker/gateway paths.

### Current hardening gaps to understand before production

These are properties of the current implementation, not recommendations silently applied by this documentation:

- The default secrets and demo credentials are suitable only for local development.
- Webhook secrets are stored as plaintext in `usage.webhook_endpoints` so the dispatcher can sign payloads.
- Target and webhook URLs accept any HTTP(S) URL; there is no SSRF/IP-range policy in the current validator.
- Operation payloads transit and persist in the control plane as JSONB. Envelope encryption or payload-reference-only mode is not implemented.
- SSE subscribers are in memory in one Usage process and are not shared across replicas.
- The optional cloud worker consumes the same logical Redis stream as private work. It acknowledges non-`CLOUD` messages when it sees them, so enabling cloud workers together with private work requires careful deployment/testing; the default Compose profile keeps cloud workers disabled.
- Private workers rely on the poll/heartbeat protocol; the private path does not perform the cloud worker's explicit deadline check before target execution.
- `transition_log` captures the DBOS enqueue transition, but not every SQL status update has a corresponding transition-log row.
- `version` is incremented on updates but is not currently used in a compare-and-swap predicate.
- The gateway CORS allowlist is hard-coded to the two local UI origins.
- `LOG_LEVEL` is present in the environment template but is not wired into the Go logger.

## 20. Change guide

When modifying this codebase, keep the following boundaries in mind:

- Add or change a public route in the gateway and the owning service, then update `docs/api.md` and this file.
- Change operation states in `internal/domain/state.go`, SQL status predicates, worker behavior, UI status lists, tests, and the state-machine documentation together.
- Change a shared wire shape in `internal/contracts/contracts.go` only with corresponding worker/service and API documentation updates.
- Change a database table or index through a new migration file; do not edit an already-applied migration for an existing environment.
- Change environment configuration in `internal/platform/config.go`, `.env.example`, Compose overrides, and this configuration table together.
- Keep tenant filters on every customer-facing read and write.
- Preserve idempotency keys for operation creation, usage events, and webhook outbox records.

