"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

const API = process.env.NEXT_PUBLIC_API_URL || "http://127.0.0.1:8080";
const TOKEN_KEY = "nexora_customer_token";

type Operation = {
  id: string;
  task_type: string;
  target_url: string;
  execution_mode: string;
  status: string;
  attempt: number;
  max_attempts: number;
  ai_explanation?: string | null;
  error_message?: string | null;
  created_at: string;
};
type Usage = {
  used: number;
  quota: number;
  remaining: number;
  period_start?: string;
};
type Tenant = {
  id: string;
  name: string;
  slug: string;
  monthly_quota: number;
  concurrency_limit: number;
};
type Webhook = {
  id: string;
  url: string;
  enabled: boolean;
  created_at: string;
};
type CreatedPeriod = "all" | "hour" | "day" | "week" | "month";

const operationStatuses = [
  "PENDING",
  "QUEUED",
  "RUNNING",
  "SUCCEEDED",
  "FAILED",
  "RETRYING",
  "DEAD_LETTERED",
  "CANCELLED",
];

function matchesCreatedPeriod(value: string, period: CreatedPeriod) {
  if (period === "all") return true;
  const created = new Date(value).getTime();
  if (Number.isNaN(created)) return false;
  const ranges: Record<Exclude<CreatedPeriod, "all">, number> = {
    hour: 60 * 60 * 1000,
    day: 24 * 60 * 60 * 1000,
    week: 7 * 24 * 60 * 60 * 1000,
    month: 30 * 24 * 60 * 60 * 1000,
  };
  return Date.now() - created <= ranges[period];
}

function errorMessage(error: unknown) {
  if (
    error instanceof TypeError &&
    error.message.toLowerCase().includes("fetch")
  ) {
    return `Unable to contact the Nexora API at ${API}. Check that Docker Compose is running and the browser can access the gateway.`;
  }
  return error instanceof Error ? error.message : "Request failed";
}

async function request<T>(
  path: string,
  token: string,
  init: RequestInit = {},
): Promise<T> {
  const response = await fetch(API + path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      ...init.headers,
    },
  });
  const body =
    response.status === 204 ? null : await response.json().catch(() => ({}));
  if (!response.ok)
    throw new Error(
      (body as { detail?: string; title?: string }).detail ||
        (body as { title?: string }).title ||
        response.statusText,
    );
  return body as T;
}

export default function Home() {
  const [token, setToken] = useState(() =>
    typeof window === "undefined"
      ? ""
      : window.localStorage.getItem(TOKEN_KEY) || "",
  );
  const [email, setEmail] = useState("ops@acme.test");
  const [password, setPassword] = useState("AcmeDemo123!");
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [webhooks, setWebhooks] = useState<Webhook[]>([]);
  const [usage, setUsage] = useState<Usage>({
    used: 0,
    quota: 0,
    remaining: 0,
  });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState("normal");
  const [question, setQuestion] = useState("Why did my latest operation fail?");
  const [answer, setAnswer] = useState("");
  const [asking, setAsking] = useState(false);
  const [webhookUrl, setWebhookUrl] = useState("http://acme:8090/webhook");
  const [webhookSecret, setWebhookSecret] = useState(
    "local-demo-webhook-secret",
  );
  const [operationsPage, setOperationsPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState("all");
  const [executionFilter, setExecutionFilter] = useState("all");
  const [createdFilter, setCreatedFilter] = useState<CreatedPeriod>("all");

  const usagePct = useMemo(
    () => (usage.quota ? Math.min(100, (usage.used / usage.quota) * 100) : 0),
    [usage],
  );

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const [tenantBody, opsBody, usageBody, webhookBody] = await Promise.all([
        request<Tenant>("/v1/tenant", token),
        request<{ items: Operation[] }>("/v1/operations?limit=50", token),
        request<Usage>("/v1/usage", token),
        request<{ items: Webhook[] }>("/v1/webhooks", token),
      ]);
      setTenant(tenantBody);
      setOperations(opsBody.items || []);
      setUsage(usageBody);
      setWebhooks(webhookBody.items || []);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [token]);

  useEffect(() => {
    const initial = setTimeout(() => void refresh(), 0);
    const id = setInterval(() => void refresh(), 3000);
    return () => {
      clearTimeout(initial);
      clearInterval(id);
    };
  }, [refresh]);

  async function login(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const body = await request<{ access_token: string; role: string }>(
        "/v1/auth/login",
        "",
        {
          method: "POST",
          headers: { Authorization: "" },
          body: JSON.stringify({ email, password }),
        },
      );
      if (body.role !== "customer")
        throw new Error("Customer account required");
      window.localStorage.setItem(TOKEN_KEY, body.access_token);
      setToken(body.access_token);
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function create() {
    setBusy(true);
    setError("");
    const isLorem = tenant?.slug === "lorem-ipsum";
    try {
      await request("/v1/operations", token, {
        method: "POST",
        headers: { "Idempotency-Key": crypto.randomUUID() },
        body: JSON.stringify({
          task_type: isLorem ? "lorem.ticket.process" : "acme.ticket.process",
          execution_mode: "PRIVATE",
          target_url: isLorem ? "http://lorem:8091/work" : "http://acme:8090/work",
          payload: { ticket_id: `${isLorem ? "LOREM" : "ACME"}-${Date.now()}`, mode },
          max_attempts: 3,
        }),
      });
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function cancel(id: string) {
    try {
      await request("/v1/operations/" + id, token, { method: "DELETE" });
      refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function redrive(id: string) {
    try {
      await request("/v1/operations/" + id + "/redrive", token, {
        method: "POST",
      });
      refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function ask() {
    setError("");
    setAnswer("");
    setAsking(true);
    try {
      const result = await request<{ answer: string }>("/v1/assistant", token, {
        method: "POST",
        body: JSON.stringify({ question }),
      });
      setAnswer(result.answer);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setAsking(false);
    }
  }

  async function addWebhook(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await request("/v1/webhooks", token, {
        method: "POST",
        body: JSON.stringify({ url: webhookUrl, secret: webhookSecret }),
      });
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function deleteWebhook(id: string) {
    try {
      await request("/v1/webhooks/" + id, token, { method: "DELETE" });
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  function signOut() {
    window.localStorage.removeItem(TOKEN_KEY);
    setToken("");
  }

  if (!token) {
    return (
      <main className="login">
        <section className="loginCard">
          <div className="mark">N</div>
          <p className="eyebrow">NEXORA PRIVATE OPERATIONS</p>
          <h1>Professional operations, clearly explained.</h1>
          <p className="muted">Sign in to access the Acme customer console.</p>
          <form onSubmit={login}>
            <label>
              Email
              <input
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                type="email"
              />
            </label>
            <label>
              Password
              <input
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                type="password"
              />
            </label>
            {error && <p className="error">{error}</p>}
            <button>Sign in</button>
          </form>
        </section>
      </main>
    );
  }

  const executionModes = Array.from(
    new Set(operations.map((operation) => operation.execution_mode)),
  ).sort();
  const filteredOperations = operations.filter(
    (operation) =>
      (statusFilter === "all" || operation.status === statusFilter) &&
      (executionFilter === "all" || operation.execution_mode === executionFilter) &&
      matchesCreatedPeriod(operation.created_at, createdFilter),
  );
  const operationsPageSize = 8;
  const operationsPages = Math.max(
    1,
    Math.ceil(filteredOperations.length / operationsPageSize),
  );
  const safeOperationsPage = Math.min(operationsPage, operationsPages);
  const pagedOperations = filteredOperations.slice(
    (safeOperationsPage - 1) * operationsPageSize,
    safeOperationsPage * operationsPageSize,
  );

  return (
    <main>
      <header>
        <div>
          <div className="brand">
            <span className="mark small">N</span>Nexora
          </div>
          <p>
            {tenant
              ? `${tenant.name} · ${tenant.concurrency_limit} concurrent jobs`
              : "Customer operations console"}
          </p>
        </div>
        <button className="ghost" onClick={signOut}>
          Sign out
        </button>
      </header>
      <section className="hero">
        <div>
          <p className="eyebrow">
            {tenant?.slug || "ACME SUPPORT"} · PRIVATE WORKER ONLINE
          </p>
          <h1>Operations, explained.</h1>
          <p className="muted">
            Jobs execute inside your network. Nexora coordinates, observes,
            signs webhooks, and explains outcomes.
          </p>
        </div>
        <div className="usage">
          <span>
            Monthly usage {usage.period_start && `· ${usage.period_start}`}
          </span>
          <strong>
            {usage.used.toLocaleString()}{" "}
            <small>/ {usage.quota.toLocaleString()}</small>
          </strong>
          <div>
            <i style={{ width: `${usagePct}%` }} />
          </div>
          <small>{usage.remaining.toLocaleString()} units remaining</small>
        </div>
      </section>
      <section className="controls">
        <select value={mode} onChange={(e) => setMode(e.target.value)}>
          <option value="normal">Successful job</option>
          <option value="fail">Retryable failure</option>
          <option value="reject">Permanent rejection</option>
          <option value="slow">Lease / timeout test</option>
        </select>
        <button onClick={create} disabled={busy}>
          {busy ? "Submitting…" : "Run private operation"}
        </button>
        <button className="ghost" onClick={refresh}>
          Refresh
        </button>
      </section>
      {error && <p className="error banner">{error}</p>}
      <section className="assistant">
        <div>
          <p className="eyebrow">AI OPERATIONS ASSISTANT</p>
          <h2>Ask about recent runs</h2>
        </div>
        <div className="ask">
          <input
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
          />
          <button onClick={ask} disabled={asking}>
            {asking ? (
              <span className="thinking" aria-label="Waiting for Nexora AI response">
                Thinking<span>.</span><span>.</span><span>.</span>
              </span>
            ) : (
              "Ask Nexora"
            )}
          </button>
        </div>
        {asking && (
          <p className="assistantLoading">
            Nexora AI is reviewing recent operation context
            <span>.</span><span>.</span><span>.</span>
          </p>
        )}
        {answer && <p>{answer}</p>}
      </section>
      {/*
      <section className="panel webhooks">
        <div className="panelTitle">
          <h2>Webhook endpoints</h2>
          <span>GET /webhooks · POST /webhooks</span>
        </div>
        <form onSubmit={addWebhook} className="webhookForm">
          <input
            value={webhookUrl}
            onChange={(e) => setWebhookUrl(e.target.value)}
            placeholder="https://example.com/webhook"
          />
          <input
            value={webhookSecret}
            onChange={(e) => setWebhookSecret(e.target.value)}
            placeholder="16+ character signing secret"
          />
          <button>Add webhook</button>
        </form>
        {webhooks.map((h) => (
          <div className="hook" key={h.id}>
            <span>
              <b>{h.url}</b>
              <code>
                {h.id.slice(0, 8)} · {new Date(h.created_at).toLocaleString()}
              </code>
            </span>
            <span className={h.enabled ? "status succeeded" : "status failed"}>
              {h.enabled ? "ENABLED" : "DISABLED"}
            </span>
            <button className="link" onClick={() => deleteWebhook(h.id)}>
              Delete
            </button>
          </div>
        ))}
        {!webhooks.length && (
          <div className="empty compact">No webhook endpoints registered.</div>
        )}
      </section>
      */}
      <section className="panel">
        <div className="panelTitle">
          <h2>Recent operations</h2>
          <span>
            {filteredOperations.length} of {operations.length} total · page {safeOperationsPage} of {operationsPages}
          </span>
        </div>
        <div className="filters">
          <label>
            Status
            <select
              value={statusFilter}
              onChange={(e) => {
                setStatusFilter(e.target.value);
                setOperationsPage(1);
              }}
            >
              <option value="all">All statuses</option>
              {operationStatuses.map((status) => (
                <option key={status} value={status}>{status}</option>
              ))}
            </select>
          </label>
          <label>
            Execution
            <select
              value={executionFilter}
              onChange={(e) => {
                setExecutionFilter(e.target.value);
                setOperationsPage(1);
              }}
            >
              <option value="all">All executions</option>
              {executionModes.map((execution) => (
                <option key={execution} value={execution}>{execution}</option>
              ))}
            </select>
          </label>
          <label>
            Created
            <select
              value={createdFilter}
              onChange={(e) => {
                setCreatedFilter(e.target.value as CreatedPeriod);
                setOperationsPage(1);
              }}
            >
              <option value="all">Any time</option>
              <option value="hour">Last hour</option>
              <option value="day">Last 24 hours</option>
              <option value="week">Last 7 days</option>
              <option value="month">Last 30 days</option>
            </select>
          </label>
        </div>
        <div className="table">
          <div className="row head">
            <span>Operation</span>
            <span>Execution</span>
            <span>Status</span>
            <span>Attempts</span>
            <span>Created</span>
            <span />
          </div>
          {pagedOperations.map((op) => (
            <div className="row" key={op.id}>
              <span>
                <b>{op.task_type}</b>
                <code>{op.id.slice(0, 8)}</code>
                {op.ai_explanation && <em>{op.ai_explanation}</em>}
                {op.error_message && (
                  <em className="failure">{op.error_message}</em>
                )}
              </span>
              <span>
                <span className="private">↗ {op.execution_mode}</span>
              </span>
              <span>
                <span className={"status " + op.status.toLowerCase()}>
                  {op.status}
                </span>
              </span>
              <span>
                {op.attempt} / {op.max_attempts}
              </span>
              <span>{new Date(op.created_at).toLocaleString()}</span>
              <span>
                {["PENDING", "QUEUED", "RUNNING", "RETRYING"].includes(
                  op.status,
                ) && (
                  <button className="link" onClick={() => cancel(op.id)}>
                    Cancel
                  </button>
                )}
                {["FAILED", "DEAD_LETTERED"].includes(op.status) && (
                  <button className="link" onClick={() => redrive(op.id)}>
                    Redrive
                  </button>
                )}
              </span>
            </div>
          ))}
          {!filteredOperations.length && (
            <div className="empty">
              {operations.length ? "No operations match the selected filters." : "No operations yet. Run the first private job."}
            </div>
          )}
        </div>
        <div className="pagination">
          <button
            className="ghost compact"
            disabled={safeOperationsPage === 1}
            onClick={() => setOperationsPage((page) => Math.max(1, page - 1))}
          >
            Previous
          </button>
          <span>
            Showing {filteredOperations.length ? (safeOperationsPage - 1) * operationsPageSize + 1 : 0}
            -{Math.min(safeOperationsPage * operationsPageSize, filteredOperations.length)} of {filteredOperations.length}
          </span>
          <button
            className="ghost compact"
            disabled={safeOperationsPage === operationsPages}
            onClick={() =>
              setOperationsPage((page) => Math.min(operationsPages, page + 1))
            }
          >
            Next
          </button>
        </div>
      </section>
    </main>
  );
}
