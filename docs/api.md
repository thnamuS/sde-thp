# Nexora API reference

This document describes the HTTP APIs implemented in the current codebase. The public API is exposed by the Go/Echo gateway; the domain services also expose private service-to-service endpoints that are not published by the gateway.

## 1. Base URLs and conventions

For the local Compose deployment:

| Surface | URL |
|---|---|
| Public gateway | `http://localhost:8080` |
| Customer console | `http://localhost:3000` |
| Admin console | `http://localhost:3001` |
| Acme demo server | `http://localhost:8090` |
| Lorem demo server | `http://localhost:8091` |

Public API routes are prefixed with `/v1`:

```text
http://localhost:8080/v1
```

JSON requests should send `Content-Type: application/json`. Successful `204` responses have no body. The gateway sets or forwards `X-Request-ID` on every request.

## 2. Authentication

### User login

```http
POST /v1/auth/login
Content-Type: application/json
```

Request:

```json
{
  "email": "ops@acme.test",
  "password": "AcmeDemo123!"
}
```

Response `200`:

```json
{
  "access_token": "eyJ...",
  "expires_in": 28800,
  "tenant_id": "tenant-uuid",
  "role": "customer"
}
```

Access lowercases and trims the email before lookup. Tokens are HS256 JWTs with issuer `nexora`, an eight-hour expiry, the user ID as `sub`, and `tenant_id`/`role` claims.

Errors: `400` for malformed JSON; `401` for invalid email or password.

### Bearer token

Send a user token on authenticated public routes:

```http
Authorization: Bearer <access_token>
```

The gateway verifies the token through Access, then derives tenant context from the verified claims. Caller-supplied `X-Nexora-Tenant-ID`, `X-Nexora-User-ID`, and `X-Nexora-Role` headers are replaced by the gateway.

### API key

Workers and service clients may use:

```http
X-API-Key: nx_live_...
```

API keys are tenant-bound. Access stores only the SHA-256 hash, updates `last_used_at` after successful verification, and rejects revoked keys.

### Request IDs and rate limits

Callers may provide `X-Request-ID`. If absent, the gateway generates one, returns it in the response, and includes it in problem responses and logs.

The gateway applies a Redis-backed per-minute limit:

| Principal | Limit |
|---|---:|
| Customer/service tenant | 120 requests/minute |
| Admin | 600 requests/minute |

Responses include `X-RateLimit-Limit` and `X-RateLimit-Remaining`. A rejected request returns `429` with `Retry-After: 60`.

## 3. Error responses

Errors use an RFC 7807-like body:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "task_type and target_url are required",
  "instance": "/v1/operations",
  "request_id": "request-correlation-id"
}
```

Common statuses:

| Status | Meaning |
|---:|---|
| `400` | Invalid JSON, missing field, or invalid value |
| `401` | Missing or invalid credentials |
| `402` | Monthly quota exhausted |
| `403` | Admin role required |
| `404` | Resource not found |
| `409` | State conflict or non-owned lease |
| `429` | Rate or concurrency limit exceeded |
| `502` | Upstream service or AI provider failure |
| `503` | Required service, limiter, or configuration unavailable |

## 4. Tenant and API-key APIs

### Get the current tenant

```http
GET /v1/tenant
Authorization: Bearer <customer-token>
```

Response `200`:

```json
{
  "id": "tenant-uuid",
  "name": "Acme Support",
  "slug": "acme-support",
  "monthly_quota": 10000,
  "concurrency_limit": 10
}
```

### Create an API key

```http
POST /v1/api-keys
Authorization: Bearer <tenant-token>
Content-Type: application/json
```

Request:

```json
{
  "name": "enterprise worker"
}
```

The name defaults to `default` when empty.

Response `201`:

```json
{
  "id": "api-key-uuid",
  "name": "enterprise worker",
  "prefix": "nx_live_abc123",
  "api_key": "nx_live_full-secret-returned-once"
}
```

The raw `api_key` is not returned by any later API.

## 5. Operations API

### Create an operation

`POST /v1/operations` requires an idempotency key:

```http
POST /v1/operations
Authorization: Bearer <customer-token>
Idempotency-Key: acme-ticket-1001
Content-Type: application/json
```

Request:

```json
{
  "task_type": "acme.ticket.process",
  "target_url": "http://acme:8090/work",
  "payload": {
    "ticket_id": "ACME-1001",
    "mode": "normal"
  },
  "max_attempts": 3,
  "deadline": "2026-09-20T12:00:00Z",
  "scheduled_for": "2026-09-20T11:55:00Z",
  "execution_mode": "PRIVATE"
}
```

| Field | Required | Rules/default |
|---|---|---|
| `task_type` | yes | Non-empty string |
| `target_url` | yes | Must begin with `http://` or `https://` |
| `payload` | no | Defaults to `{}`; stored as JSON |
| `max_attempts` | no | Defaults to `3`; must be 1–10 |
| `deadline` | no | Absolute timestamp |
| `scheduled_for` | no | Publication is delayed until this timestamp |
| `execution_mode` | no | `PRIVATE` by default; must be `PRIVATE` or `CLOUD` |

The `Idempotency-Key` header is required and must be at most 200 characters. Idempotency is scoped to the authenticated tenant.

Response `202` for a new operation:

```json
{
  "id": "operation-uuid",
  "tenant_id": "tenant-uuid",
  "task_type": "acme.ticket.process",
  "execution_mode": "PRIVATE",
  "target_url": "http://acme:8090/work",
  "payload": {
    "ticket_id": "ACME-1001",
    "mode": "normal"
  },
  "status": "PENDING",
  "attempt": 0,
  "max_attempts": 3,
  "created_at": "2026-09-20T11:55:00Z",
  "updated_at": "2026-09-20T11:55:00Z"
}
```

Submitting the same idempotency key again returns the original operation with `200` rather than creating another operation.

Admission failures: `400` for invalid input, `402` when the monthly quota is exhausted, and `429` when the tenant's running-operation limit is reached.

### Operation response shape

Operation objects returned by list/detail/create APIs contain:

| Field | Type | Description |
|---|---|---|
| `id` | string | Operation UUID |
| `tenant_id` | string | Owning tenant UUID |
| `task_type` | string | Caller-defined task identifier |
| `execution_mode` | string | `PRIVATE` or `CLOUD` |
| `target_url` | string | Worker target URL |
| `payload` | object | Submitted JSON payload |
| `status` | string | Lifecycle status |
| `attempt` | integer | Current attempt number |
| `max_attempts` | integer | Retry budget |
| `deadline` | timestamp/null | Optional deadline |
| `result` | object/null | Successful target result |
| `error_code` | string/null | Normalized failure code |
| `error_message` | string/null | Failure detail |
| `ai_explanation` | string/null | Worker-generated explanation |
| `created_at` | timestamp | Creation time |
| `updated_at` | timestamp | Last update time |

### Operation statuses

| Status | Meaning |
|---|---|
| `PENDING` | Stored but not yet published to Redis |
| `QUEUED` | Published and waiting for a worker |
| `RUNNING` | Leased by a worker |
| `SUCCEEDED` | Target returned a 2xx response |
| `FAILED` | Retryable failure recorded; used by some internal paths |
| `RETRYING` | Waiting for retry publication |
| `DEAD_LETTERED` | Permanent or exhausted failure |
| `CANCELLED` | Cancelled by the tenant before completion |

Terminal statuses are `SUCCEEDED`, `DEAD_LETTERED`, and `CANCELLED`.

### List operations

```http
GET /v1/operations?status=RUNNING&limit=50
Authorization: Bearer <customer-token>
```

`status` is an exact status filter. `limit` defaults to 50; values outside 1–100 also become 50.

Response `200`:

```json
{
  "items": [
    {
      "id": "operation-uuid",
      "tenant_id": "tenant-uuid",
      "task_type": "acme.ticket.process",
      "execution_mode": "PRIVATE",
      "target_url": "http://acme:8090/work",
      "payload": {},
      "status": "RUNNING",
      "attempt": 1,
      "max_attempts": 3,
      "result": null,
      "error_code": null,
      "error_message": null,
      "ai_explanation": null,
      "created_at": "2026-09-20T11:55:00Z",
      "updated_at": "2026-09-20T11:55:01Z"
    }
  ]
}
```

Customer requests are tenant-scoped. Admin requests to `/v1/admin/operations` use the same handler but return operations across tenants.

### Get an operation

```http
GET /v1/operations/{id}
Authorization: Bearer <customer-token>
```

Response `200` is one operation object. An operation belonging to another tenant returns `404`.

### Cancel an operation

```http
DELETE /v1/operations/{id}
Authorization: Bearer <customer-token>
```

`PENDING`, `QUEUED`, `RUNNING`, `RETRYING`, and `FAILED` operations may be cancelled. Response `204` has no body. Missing, terminal, or already-changed operations return `409`.

Cancellation does not forcibly terminate a target request already in progress.

### Redrive an operation

```http
POST /v1/operations/{id}/redrive
Authorization: Bearer <customer-token>
```

Only `FAILED` and `DEAD_LETTERED` operations are redrivable. The service clears error fields, resets `attempt` to zero, sets `RETRYING`, and starts a new workflow.

Response `202`:

```json
{
  "id": "operation-uuid",
  "status": "RETRYING"
}
```

## 6. Usage and event APIs

### Current tenant usage

```http
GET /v1/usage
Authorization: Bearer <customer-token>
```

Response `200`:

```json
{
  "used": 12,
  "quota": 10000,
  "remaining": 9988,
  "period_start": "2026-09"
}
```

`used` is the current month's sum of usage ledger units. Remaining is clamped at zero.

### Server-sent events

```http
GET /v1/events
Authorization: Bearer <customer-token>
Accept: text/event-stream
```

Response headers include `Content-Type: text/event-stream` and `Cache-Control: no-cache`.

Events are sent as:

```text
event: operation
data: {"event_key":"operation-uuid:operation.succeeded:1","event_type":"operation.succeeded","operation_id":"operation-uuid","tenant_id":"tenant-uuid","units":1,"payload":{},"occurred_at":"2026-09-20T12:00:00Z"}

```

A `: keepalive` comment is sent every 20 seconds. Subscribers are process-local and receive live events only; there is no replay parameter.

## 7. Webhook APIs

### Create a webhook endpoint

```http
POST /v1/webhooks
Authorization: Bearer <customer-token>
Content-Type: application/json
```

Request:

```json
{
  "url": "https://customer.example.com/nexora/webhook",
  "secret": "at-least-16-character-secret"
}
```

The URL must use `http://` or `https://`. The secret must contain at least 16 characters.

Response `201`:

```json
{
  "id": "endpoint-uuid",
  "url": "https://customer.example.com/nexora/webhook"
}
```

### List webhook endpoints

```http
GET /v1/webhooks
Authorization: Bearer <customer-token>
```

Response `200`:

```json
{
  "items": [
    {
      "id": "endpoint-uuid",
      "url": "https://customer.example.com/nexora/webhook",
      "enabled": true,
      "created_at": "2026-09-20T12:00:00Z"
    }
  ]
}
```

The signing secret is never returned.

### Delete a webhook endpoint

```http
DELETE /v1/webhooks/{id}
Authorization: Bearer <customer-token>
```

Response `204` has no body. A missing endpoint or endpoint owned by another tenant returns `404`.

### Webhook delivery contract

The webhook body is the event payload stored in the outbox. Nexora sends:

```http
Content-Type: application/json
X-Nexora-Event: operation.succeeded
X-Nexora-Delivery: delivery-uuid
X-Nexora-Signature: sha256=<lowercase-hex-hmac>
```

The signature is HMAC-SHA256 over the exact request body using the endpoint secret. Any 2xx response is considered delivered. Failed deliveries retry with bounded exponential delay and become `DEAD_LETTERED` after eight attempts.

## 8. AI assistant

```http
POST /v1/assistant
Authorization: Bearer <customer-token>
Content-Type: application/json
```

Request:

```json
{
  "question": "Why did my latest operation fail?"
}
```

The question must be non-empty and no longer than 2,000 characters. The gateway loads the tenant's 20 most recent operations and sends that JSON context to OpenRouter.

Response `200`:

```json
{
  "answer": "The target returned HTTP 422, which is treated as a permanent rejection.",
  "model": "google/gemini-2.5-flash"
}
```

Errors: `400` for invalid questions, `502` when operation context or both model attempts fail, and `503` when `OPENROUTER_API_KEY` is not configured.

## 9. Private worker protocol

These are public gateway routes intended for the packaged enterprise worker. Authenticate them with the tenant's API key. The worker has no PostgreSQL or Redis credentials.

### Poll for one private operation

```http
POST /v1/worker/poll
X-API-Key: <tenant-api-key>
Content-Type: application/json
```

Request:

```json
{
  "worker_id": "host-a:worker-1234"
}
```

Response `200` when work is available:

```json
{
  "operation_id": "operation-uuid",
  "tenant_id": "tenant-uuid",
  "task_type": "acme.ticket.process",
  "target_url": "http://acme:8090/work",
  "payload": {
    "ticket_id": "ACME-1001"
  },
  "attempt": 1,
  "max_attempts": 3,
  "execution_mode": "PRIVATE"
}
```

Response `204` means no private operation is available. Polling claims the operation and starts a 45-second lease.

### Heartbeat

```http
POST /v1/worker/operations/{id}/heartbeat
X-API-Key: <tenant-api-key>
Content-Type: application/json
```

Request: `{ "worker_id": "host-a:worker-1234" }`

Response `204`. The worker must own the running lease; otherwise the service returns `409`.

### Complete

```http
POST /v1/worker/operations/{id}/complete
X-API-Key: <tenant-api-key>
Content-Type: application/json
```

Request:

```json
{
  "worker_id": "host-a:worker-1234",
  "result": {
    "accepted": true
  },
  "ai_explanation": "The target accepted the request successfully."
}
```

Response `204`. Completion requires a running operation and matching lease owner.

### Fail

```http
POST /v1/worker/operations/{id}/fail
X-API-Key: <tenant-api-key>
Content-Type: application/json
```

Request:

```json
{
  "worker_id": "host-a:worker-1234",
  "error_code": "TARGET_SERVER_ERROR",
  "error_message": "target returned HTTP 503",
  "retryable": true,
  "ai_explanation": "The target was unavailable; retry after checking service health."
}
```

Response `200`:

```json
{
  "status": "RETRYING"
}
```

The status is `RETRYING` while attempts remain; otherwise it is `DEAD_LETTERED`.

## 10. Administration APIs

All admin routes require a JWT whose `role` is `admin`.

### List tenants

```http
GET /v1/admin/tenants
Authorization: Bearer <admin-token>
```

Response `200`:

```json
{
  "items": [
    {
      "id": "tenant-uuid",
      "name": "Acme Support",
      "slug": "acme-support",
      "monthly_quota": 10000,
      "concurrency_limit": 10,
      "created_at": "2026-09-20T12:00:00Z"
    }
  ]
}
```

### Create a tenant

```http
POST /v1/admin/tenants
Authorization: Bearer <admin-token>
Content-Type: application/json
```

Request:

```json
{
  "name": "Example Corp",
  "slug": "example-corp",
  "email": "ops@example.com",
  "password": "TemporaryPassword123!",
  "monthly_quota": 10000,
  "concurrency_limit": 5
}
```

`name`, `slug`, and `email` are required. Passwords must be at least 10 characters. A zero quota defaults to `10000`; a zero concurrency limit defaults to `5`.

Response `201`:

```json
{
  "id": "tenant-uuid"
}
```

Tenant creation inserts the tenant and initial customer user in one PostgreSQL transaction.

### List all operations

```http
GET /v1/admin/operations?status=DEAD_LETTERED&limit=50
Authorization: Bearer <admin-token>
```

Response shape is the same as `GET /v1/operations`, but the admin role removes the tenant filter. `limit` defaults to 50 and is capped at 100.

### Usage by tenant

```http
GET /v1/admin/usage
Authorization: Bearer <admin-token>
```

Response `200`:

```json
{
  "items": [
    {
      "tenant_id": "tenant-uuid",
      "name": "Acme Support",
      "quota": 10000,
      "used": 12
    }
  ]
}
```

## 11. Internal service APIs

These routes are not exposed by the gateway. Callers must send:

```http
X-Internal-Secret: <INTERNAL_SECRET>
```

### Access verification

```http
POST http://access:8081/internal/verify
X-Internal-Secret: <INTERNAL_SECRET>
Authorization: Bearer <token>
```

The request body is empty. The gateway forwards either `Authorization` or `X-API-Key`. Response `200`:

```json
{
  "tenant_id": "tenant-uuid",
  "user_id": "user-uuid",
  "role": "customer"
}
```

For an API key, `user_id` is omitted and `role` is `service`.

### Cloud worker operation endpoints

The cloud worker calls Operations directly at `operations:8082`:

| Method | Path | Body | Success |
|---|---|---|---|
| `POST` | `/internal/operations/{id}/claim` | `worker_id`, optional `lease_seconds` | `200` with `tenant_id`, `status` |
| `POST` | `/internal/operations/{id}/heartbeat` | `worker_id` | `204` |
| `POST` | `/internal/operations/{id}/complete` | `worker_id`, `result`, `ai_explanation` | `204` |
| `POST` | `/internal/operations/{id}/fail` | `worker_id`, error fields, `retryable`, explanation | `200` with status |
| `GET` | `/internal/operations/{id}` | none | `200` operation object |

These endpoints use the internal secret instead of tenant authentication. Worker ID and operation state still gate claim, heartbeat, completion, and failure.

### Usage event ingestion

```http
POST http://usage:8083/internal/events
X-Internal-Secret: <INTERNAL_SECRET>
Content-Type: application/json
```

Request:

```json
{
  "event_key": "operation-uuid:operation.succeeded:1",
  "event_type": "operation.succeeded",
  "operation_id": "operation-uuid",
  "tenant_id": "tenant-uuid",
  "units": 1,
  "payload": {
    "accepted": true
  },
  "occurred_at": "2026-09-20T12:00:00Z"
}
```

Response `202`. Duplicate `event_key` values are ignored by the usage ledger so retries do not double charge.

## 12. Health and readiness endpoints

Every Go service exposes:

```http
GET /healthz
```

Response `200`:

```json
{
  "status": "ok",
  "service": "operations"
}
```

The gateway additionally exposes:

```http
GET /readyz
```

Response `200` when Access, Operations, and Usage are healthy:

```json
{
  "status": {
    "access": "ok",
    "operations": "ok",
    "usage": "ok"
  }
}
```

If any dependency is unavailable, the gateway returns `503` and marks that service `unavailable`.

## 13. Demo server APIs

The `acme` and `lorem` containers run `cmd/acme/main.go`. These endpoints are demo/test helpers, not public Nexora APIs.

| Method | Path | Behavior |
|---|---|---|
| `POST` | `/work` | Simulated target; `mode=fail` returns 500, `reject` returns 422, `slow` waits 35 seconds |
| `POST` | `/webhook` | Receives signed webhook requests; can be disabled |
| `GET` | `/demo/normal` | Submit one successful private operation |
| `GET` | `/demo/burst?count=25` | Submit up to 100 operations |
| `GET` | `/demo/idempotent` | Submit the same idempotency key twice |
| `POST` | `/demo/webhook/toggle` | Body `{ "enabled": false }` toggles receiver availability |
| `GET` | `/demo/quota-exhaust?count=20` | Submit until the first rejected operation |
| `GET` | `/demo/stats` | Return target request count and webhook state |

Examples:

```bash
curl http://localhost:8090/demo/normal
curl 'http://localhost:8090/demo/burst?count=25'
curl http://localhost:8090/demo/idempotent
curl -X POST http://localhost:8090/demo/webhook/toggle \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false}'
```
