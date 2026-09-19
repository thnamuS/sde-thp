CREATE TABLE IF NOT EXISTS operations.event_outbox (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_key text NOT NULL UNIQUE,
  event_type text NOT NULL,
  operation_id uuid NOT NULL,
  tenant_id uuid NOT NULL,
  units bigint NOT NULL DEFAULT 1,
  payload jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','DELIVERED')),
  attempt integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz
);
CREATE INDEX IF NOT EXISTS operation_events_due_idx ON operations.event_outbox(status, next_attempt_at);
