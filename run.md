# Nexora Preparation Notes

## What this project is

Nexora is a multi-tenant operation orchestration demo platform. A tenant submits an operation with a target URL and payload. Nexora stores it, queues it, leases it to a worker, tracks status, records usage, and optionally sends webhook events.

Core capabilities:

- Tenant login and API-key authentication
- Operation submission and lifecycle tracking
- Private enterprise-worker execution through outbound polling
- Optional cloud-worker execution through Redis consumer groups
- Usage accounting and monthly quota checks
- Tenant concurrency limits
- Webhook endpoint registration and delivery
- Admin console for tenant/operation visibility
- Optional AI explanation/assistant support through OpenRouter

## Local URLs

| Purpose            | URL                           |
| ------------------ | ----------------------------- |
| Customer console   | http://127.0.0.1:3000         |
| Admin console      | http://127.0.0.1:3001         |
| Gateway/API        | http://127.0.0.1:8080         |
| Gateway readiness  | http://127.0.0.1:8080/readyz  |
| Acme demo service  | http://127.0.0.1:8090/healthz |
| Lorem demo service | http://127.0.0.1:8091/healthz |

## Local credentials

### Admin

- Email: `admin@nexora.local`
- Password: `ChangeMe123!`
- Console: http://127.0.0.1:3001

### Acme customer tenant

- Email: `ops@acme.test`
- Password: `AcmeDemo123!`
- Console: http://127.0.0.1:3000
- Demo API key from `.env.example`: `nx_test_acme_local_demo_change_before_deployment`
- Compose fallback API key if `.env` is absent: `acme_demo_key_change_me_123456`

### Lorem customer tenant

- Email: `ops@lorem.test`
- Password: `LoremDemo123!`
- Console: http://127.0.0.1:3000
- Demo API key from `.env.example`: `nx_test_lorem_local_demo_change_before_deployment`
- Compose fallback API key if `.env` is absent: `lorem_demo_key_change_me_123456`

### Local database

- DB: `nexora`
- User: `nexora`
- Password: `nexora_dev`
- In Compose URL: `postgres://nexora:nexora_dev@postgres:5432/nexora?sslmode=disable`

### Redis

- In Compose URL: `redis://redis:6379/0`

> These credentials are for local demo only. Do not use them in production or shared deployments.

## How to run locally

```sh
make dev
```

or:

```sh
docker compose up --build
```

Check status:

```sh
docker compose ps
curl http://127.0.0.1:8080/readyz
```

Expected readiness response:

```json
{ "status": { "access": "ok", "operations": "ok", "usage": "ok" } }
```

Stop everything:

```sh
docker compose down
```

If you need a clean DB/Redis reset:

```sh
docker compose down -v
make dev
```

## Main services

### `access`

Handles:

- User login
- JWT issuing
- API key verification
- Tenant bootstrap/seed data
- Admin tenant management

Important routes behind gateway:

- `POST /v1/auth/login`
- `GET /v1/tenant`
- `POST /v1/api-keys`
- `GET /v1/admin/tenants`

### `gateway`

Browser/public API entrypoint. It authenticates requests through `access`, rate-limits requests, and reverse-proxies to internal services.

Public local base URL:

```text
http://127.0.0.1:8080
```

### `operations`

Handles operation lifecycle:

- Create operation
- Queue operation
- Claim operation
- Heartbeat lease
- Complete/fail/cancel/redrive
- Redis stream polling for private workers
- Internal event outbox dispatch to usage service

Important statuses:

- `PENDING`
- `QUEUED`
- `RUNNING`
- `SUCCEEDED`
- `FAILED`
- `RETRYING`
- `DEAD_LETTERED`
- `CANCELLED`

Terminal statuses:

- `SUCCEEDED`
- `DEAD_LETTERED`
- `CANCELLED`

### `usage`

Handles:

- Usage ledger records
- Monthly usage/quota reporting
- Server-sent events
- Webhook endpoint registration
- Webhook outbox delivery

Important routes:

- `GET /v1/usage`
- `GET /v1/events`
- `GET /v1/webhooks`
- `POST /v1/webhooks`

### `enterprise-worker`

Private worker that polls Nexora through the gateway. It represents a customer-network worker. It executes operations by calling the target URL.

### `worker`

Cloud worker using Redis consumer groups. It is behind the optional `cloud-worker` Compose profile.

### `acme` and `lorem`

Demo customer target services. They expose `/work` endpoints that receive operation payloads.

Internal Docker target URLs:

- Acme: `http://acme:8090/work`
- Lorem: `http://lorem:8091/work`

Browser health URLs:

- Acme: `http://127.0.0.1:8090/healthz`
- Lorem: `http://127.0.0.1:8091/healthz`

## Webhooks

In the customer console, the “Webhook endpoints” section registers where Nexora should send operation event notifications.

Demo Acme webhook URL:

```text
http://acme:8090/webhook
```

This URL is internal to Docker Compose. It is correct that the browser itself cannot open `http://acme:8090`; the backend containers can.

Demo webhook secret:

```text
local-demo-webhook-secret
```

Webhook requests include headers such as:

- `X-Nexora-Event`
- `X-Nexora-Delivery`
- `X-Nexora-Signature`

## Useful demo flows

### Login as customer

Open:

```text
http://127.0.0.1:3000
```

Use:

```text
ops@acme.test / AcmeDemo123!
```

### Trigger Acme demo operations

```sh
curl http://127.0.0.1:8090/demo/normal
curl 'http://127.0.0.1:8090/demo/burst?count=25'
curl http://127.0.0.1:8090/demo/idempotent
```

### Toggle Acme webhook receiver

```sh
curl -X POST http://127.0.0.1:8090/demo/webhook/toggle \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true}'
```

The frontend now adjusts the API hostname to match the browser hostname.

### Backend containers restart with Postgres/Redis localhost errors

Inside Docker, services must use Compose service names, not localhost:

```text
postgres:5432
redis:6379
```

The current `docker-compose.yml` provides these defaults through shared environment config.

### Existing database does not reflect migration changes

If schema changes are not visible because an old Docker volume exists:

```sh
docker compose down -v
make dev
```

This deletes local Postgres/Redis data and recreates from migrations.
