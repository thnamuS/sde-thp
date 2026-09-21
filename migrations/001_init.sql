CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE SCHEMA IF NOT EXISTS access;
CREATE SCHEMA IF NOT EXISTS operations;
CREATE SCHEMA IF NOT EXISTS usage;

CREATE TABLE access.tenants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  monthly_quota bigint NOT NULL DEFAULT 10000 CHECK (monthly_quota >= 0),
  concurrency_limit integer NOT NULL DEFAULT 5 CHECK (concurrency_limit > 0),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE access.users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid REFERENCES access.tenants(id),
  email text NOT NULL UNIQUE,
  password_hash text NOT NULL,
  role text NOT NULL CHECK (role IN ('customer','admin')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE access.api_keys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES access.tenants(id),
  name text NOT NULL,
  prefix text NOT NULL,
  key_hash text NOT NULL UNIQUE,
  last_used_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE operations.operations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES access.tenants(id),
  task_type text NOT NULL,
  execution_mode text NOT NULL DEFAULT 'PRIVATE' CHECK (execution_mode IN ('PRIVATE','CLOUD')),
  target_url text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL CHECK (status IN ('PENDING','QUEUED','RUNNING','SUCCEEDED','FAILED','RETRYING','DEAD_LETTERED','CANCELLED')),
  attempt integer NOT NULL DEFAULT 0,
  max_attempts integer NOT NULL DEFAULT 3,
  deadline timestamptz,
  lease_owner text,
  lease_expires_at timestamptz,
  result jsonb,
  error_code text,
  error_message text,
  ai_explanation text,
  idempotency_key text,
  version bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX operations_tenant_created_idx ON operations.operations(tenant_id, created_at DESC);
CREATE INDEX operations_lease_idx ON operations.operations(status, lease_expires_at);
CREATE TABLE operations.transition_log (
  id bigserial PRIMARY KEY,
  operation_id uuid NOT NULL REFERENCES operations.operations(id),
  tenant_id uuid NOT NULL,
  from_status text,
  to_status text NOT NULL,
  reason text,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE usage.ledger (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES access.tenants(id),
  operation_id uuid NOT NULL,
  event_key text NOT NULL UNIQUE,
  units bigint NOT NULL CHECK (units >= 0),
  metadata jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_tenant_month_idx ON usage.ledger(tenant_id, created_at);
CREATE TABLE usage.webhook_endpoints (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES access.tenants(id),
  url text NOT NULL,
  secret text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE usage.webhook_outbox (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  endpoint_id uuid NOT NULL REFERENCES usage.webhook_endpoints(id),
  event_type text NOT NULL,
  operation_id uuid NOT NULL,
  payload jsonb NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','IN_FLIGHT','DELIVERED','DEAD_LETTERED')),
  attempt integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz
);
CREATE INDEX webhook_due_idx ON usage.webhook_outbox(status, next_attempt_at);
