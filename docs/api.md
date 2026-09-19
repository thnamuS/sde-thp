# API reference

Base URL: `http://localhost:8080/v1`. Errors use an RFC 7807-like body containing `type`, `title`, `status`, `detail`, `instance`, and `request_id`.

## Authentication

`POST /auth/login` accepts `{ "email", "password" }` and returns a bearer token. All other routes require either `Authorization: Bearer <token>` or `X-API-Key: <key>`. Tenant identity is derived by the gateway.

## Operations

- `POST /operations` — requires `Idempotency-Key`; accepts `task_type`, `target_url`, `payload`, `max_attempts`, optional `deadline`, optional `scheduled_for`, and `execution_mode` (`PRIVATE` by default).
- `GET /operations` — tenant-scoped list; accepts `status` and `limit`.
- `GET /operations/{id}` — operation detail and AI explanation.
- `DELETE /operations/{id}` — cancellation for non-terminal operations.
- `POST /operations/{id}/redrive` — resets a failed or dead-lettered operation.

## Usage and webhooks

- `GET /usage` — current monthly use, quota, and remaining units.
- `GET /events` — server-sent operation events.
- `GET /webhooks`, `POST /webhooks`, `DELETE /webhooks/{id}`.
- `POST /assistant` — asks OpenRouter about the tenant's 20 most recent operations; requires `OPENROUTER_API_KEY`.

Webhook requests contain `X-Nexora-Event`, `X-Nexora-Delivery`, and `X-Nexora-Signature: sha256=<hex hmac>` headers.

## Private worker protocol

- `POST /worker/poll`
- `POST /worker/operations/{id}/heartbeat`
- `POST /worker/operations/{id}/complete`
- `POST /worker/operations/{id}/fail`

These routes are tenant-bound and intended for the packaged enterprise worker. A worker has no PostgreSQL or Redis access.

## Administration

- `GET`, `POST /admin/tenants`
- `GET /admin/operations`
- `GET /admin/usage`

All administration routes require an admin JWT.
