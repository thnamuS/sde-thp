# Nexora AI

Nexora is a multi-tenant operations control plane for customer-owned jobs. Enterprises run jobs inside their own network through an outbound-only private worker; Nexora coordinates execution, tracks state and usage, delivers signed webhooks, and generates outcome explanations through OpenRouter.

## What runs where

- **Nexora control plane:** Go/Echo gateway, Access service, Operations service with DBOS, Usage/Webhooks service, PostgreSQL, and one Redis Stream.
- **Customer environment:** the enterprise worker and the customer's job endpoints. It needs only outbound HTTPS access to Nexora and OpenRouter. It receives no Nexora database or Redis credentials.
- **Optional cloud execution:** `docker compose --profile cloud-worker up`; disabled by default.
- **Interfaces:** separate Next.js customer and admin applications.

## Quick start

Requirements: Docker with Compose, and optionally an OpenRouter API key.

```bash
cp .env.example .env
# Set OPENROUTER_API_KEY in .env for live AI explanations.
docker compose up --build
```

Open:

- Customer console: http://localhost:3000 (`ops@acme.test` / `AcmeDemo123!`, or `ops@lorem.test` / `LoremDemo123!`)
- Admin console: http://localhost:3001 (`admin@nexora.local` / `ChangeMe123!`)
- Gateway health: http://localhost:8080/readyz
- Acme demo server: http://localhost:8090/healthz

Development credentials are intentionally local-only. Replace every secret before a shared deployment.

Run an end-to-end demonstration:

```bash
curl http://localhost:8090/demo/normal
curl 'http://localhost:8090/demo/burst?count=25'
curl http://localhost:8090/demo/idempotent
curl -X POST http://localhost:8090/demo/webhook/toggle \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
```

## Verification

```bash
make test
make lint
docker compose config --quiet
./scripts/smoke.sh
```

## Architecture constraints

The implementation has exactly three domain services: Customer & Access, Operations & Processing, and Usage & Webhooks. Redis contains one logical operation stream. DBOS checkpoints the transition that publishes an operation to that stream; DBOS Queues are deliberately not registered, avoiding a second logical queue.

The assessment requested a Node.js/TypeScript gateway. This project intentionally uses Go/Echo for the gateway following the project owner's explicit decision. That deviation is recorded in [ADR 0001](docs/decisions/0001-go-gateway.md).

See [architecture](docs/architecture.md), [API reference](docs/api.md), and [submission checklist](docs/submission-checklist.md).

For an implementation-level walkthrough of every service, source directory, runtime flow, data model, configuration value, worker protocol, frontend behavior, verification command, and current hardening caveat, see the [complete codebase reference](docs/codebase.md).
