"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

const API = process.env.NEXT_PUBLIC_API_URL || "http://127.0.0.1:8080";
const TOKEN_KEY = "nexora_admin_token";

type Tenant = {
  id: string;
  name: string;
  slug: string;
  monthly_quota: number;
  concurrency_limit: number;
  created_at?: string;
};
type Op = {
  id: string;
  tenant_id: string;
  task_type: string;
  status: string;
  execution_mode: string;
  created_at: string;
};
type Usage = { tenant_id: string; name: string; quota: number; used: number };
type View = "overview" | "tenants" | "operations" | "usage";

const attentionStatuses = ["FAILED", "DEAD_LETTERED"];
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
type CreatedPeriod = "all" | "hour" | "day" | "week" | "month";

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

function formatRelative(value?: string) {
  if (!value) return "—";
  const diff = Date.now() - new Date(value).getTime();
  const minutes = Math.max(0, Math.floor(diff / 60000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function pct(used: number, quota: number) {
  return quota ? Math.min(100, Math.round((used / quota) * 100)) : 0;
}

function errorMessage(error: unknown) {
  if (
    error instanceof TypeError &&
    error.message.toLowerCase().includes("fetch")
  )
    return `Unable to contact the Nexora API at ${API}. Check that Docker Compose is running and the browser can access the gateway.`;
  return error instanceof Error ? error.message : "Request failed";
}

async function api<T>(
  path: string,
  token: string,
  init: RequestInit = {},
): Promise<T> {
  const r = await fetch(API + path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      Authorization: token ? `Bearer ${token}` : "",
      ...init.headers,
    },
  });
  const body = r.status === 204 ? null : await r.json().catch(() => ({}));
  if (!r.ok)
    throw new Error(
      (body as { detail?: string; title?: string }).detail ||
        (body as { title?: string }).title ||
        r.statusText,
    );
  return body as T;
}

export default function Admin() {
  const [token, setToken] = useState(() =>
    typeof window === "undefined"
      ? ""
      : window.localStorage.getItem(TOKEN_KEY) || "",
  );
  const [email, setEmail] = useState("admin@nexora.local");
  const [password, setPassword] = useState("ChangeMe123!");
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [ops, setOps] = useState<Op[]>([]);
  const [usage, setUsage] = useState<Usage[]>([]);
  const [error, setError] = useState("");
  const [showNew, setShowNew] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [activeView, setActiveView] = useState<View>("overview");
  const [opsPage, setOpsPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState("all");
  const [tenantFilter, setTenantFilter] = useState("all");
  const [createdFilter, setCreatedFilter] = useState<CreatedPeriod>("all");

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const [t, o, u] = await Promise.all([
        api<{ items: Tenant[] }>("/v1/admin/tenants", token),
        api<{ items: Op[] }>("/v1/admin/operations?limit=50", token),
        api<{ items: Usage[] }>("/v1/admin/usage", token),
      ]);
      setTenants(t.items || []);
      setOps(o.items || []);
      setUsage(u.items || []);
      setLastUpdated(new Date());
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [token]);

  useEffect(() => {
    const initial = setTimeout(() => void refresh(), 0);
    const i = setInterval(() => void refresh(), 5000);
    return () => {
      clearTimeout(initial);
      clearInterval(i);
    };
  }, [refresh]);

  async function login(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const b = await api<{ access_token: string; role: string }>(
        "/v1/auth/login",
        "",
        { method: "POST", body: JSON.stringify({ email, password }) },
      );
      if (b.role !== "admin") throw new Error("Administrator role required");
      window.localStorage.setItem(TOKEN_KEY, b.access_token);
      setToken(b.access_token);
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  async function createTenant(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await api("/v1/admin/tenants", token, {
        method: "POST",
        body: JSON.stringify({
          ...data,
          monthly_quota: Number(data.monthly_quota),
          concurrency_limit: Number(data.concurrency_limit),
        }),
      });
      setShowNew(false);
      await refresh();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  function signOut() {
    window.localStorage.removeItem(TOKEN_KEY);
    setToken("");
  }

  const tenantNames = useMemo(
    () => new Map(tenants.map((t) => [t.id, t.name])),
    [tenants],
  );

  if (!token)
    return (
      <main className="login">
        <form onSubmit={login}>
          <div className="logo">N</div>
          <p className="eyebrow">Nexora Control Panel</p>
          <h1>Platform administration</h1>
          <p className="muted">
            Secure admin access for tenant, usage, and operation management.
          </p>
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
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          {error && <p className="error">{error}</p>}
          <button>Sign in</button>
        </form>
      </main>
    );

  const running = ops.filter((o) => o.status === "RUNNING").length;
  const queued = ops.filter((o) => o.status === "QUEUED").length;
  const succeeded = ops.filter((o) => o.status === "SUCCEEDED").length;
  const failed = ops.filter((o) => attentionStatuses.includes(o.status)).length;
  const totalUsage = usage.reduce((n, u) => n + u.used, 0);
  const totalQuota = usage.reduce((n, u) => n + u.quota, 0);
  const utilization = pct(totalUsage, totalQuota);
  const avgConcurrency = tenants.length
    ? Math.round(
        tenants.reduce((n, t) => n + t.concurrency_limit, 0) / tenants.length,
      )
    : 0;
  const busiestTenants = [...usage]
    .sort((a, b) => pct(b.used, b.quota) - pct(a.used, a.quota))
    .slice(0, 5);
  const needsAttention = failed > 20 || utilization >= 90;
  const filteredOps = ops.filter(
    (o) =>
      (statusFilter === "all" || o.status === statusFilter) &&
      (tenantFilter === "all" || o.tenant_id === tenantFilter) &&
      matchesCreatedPeriod(o.created_at, createdFilter),
  );
  const opsPageSize = activeView === "operations" ? 12 : 6;
  const opsPages = Math.max(1, Math.ceil(filteredOps.length / opsPageSize));
  const safeOpsPage = Math.min(opsPage, opsPages);
  const pagedOps = filteredOps.slice(
    (safeOpsPage - 1) * opsPageSize,
    safeOpsPage * opsPageSize,
  );
  const pageTitle =
    activeView === "overview"
      ? "Dashboard"
      : activeView[0].toUpperCase() + activeView.slice(1);

  const operationsTable = (full = false) => (
    <section className={full ? "panel" : "panel wide"}>
      <div className="title">
        <h2>{full ? "Operations" : "Recent operations"}</h2>
        <span>{filteredOps.length} of {ops.length} loaded</span>
      </div>
      <div className="filters">
        <label>
          Status
          <select
            value={statusFilter}
            onChange={(e) => {
              setStatusFilter(e.target.value);
              setOpsPage(1);
            }}
          >
            <option value="all">All statuses</option>
            {operationStatuses.map((status) => (
              <option key={status} value={status}>{status}</option>
            ))}
          </select>
        </label>
        <label>
          Tenant
          <select
            value={tenantFilter}
            onChange={(e) => {
              setTenantFilter(e.target.value);
              setOpsPage(1);
            }}
          >
            <option value="all">All tenants</option>
            {tenants.map((tenant) => (
              <option key={tenant.id} value={tenant.id}>{tenant.name}</option>
            ))}
          </select>
        </label>
        <label>
          Created
          <select
            value={createdFilter}
            onChange={(e) => {
              setCreatedFilter(e.target.value as CreatedPeriod);
              setOpsPage(1);
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
      <div className="table compactTable">
        <div className="row head">
          <span>Task</span>
          <span>Tenant</span>
          <span>Execution</span>
          <span>Status</span>
          <span>Created</span>
        </div>
        {pagedOps.map((o) => (
          <div className="row" key={o.id}>
            <span>
              <b>{o.task_type}</b>
              <code>{o.id.slice(0, 8)}</code>
            </span>
            <span>
              <b>{tenantNames.get(o.tenant_id) || "Unknown"}</b>
              <code>{o.tenant_id.slice(0, 8)}</code>
            </span>
            <span>{o.execution_mode}</span>
            <span>
              <i className={"status " + o.status.toLowerCase()}>
                {o.status.replace("DEAD_LETTERED", "DLQ")}
              </i>
            </span>
            <span title={new Date(o.created_at).toLocaleString()}>
              {formatRelative(o.created_at)}
            </span>
          </div>
        ))}
        {!filteredOps.length && (
          <div className="empty">
            {ops.length ? "No operations match the selected filters." : "No operations have been submitted."}
          </div>
        )}
      </div>
      <div className="pagination">
        <button
          className="outline compact"
          disabled={safeOpsPage === 1}
          onClick={() => setOpsPage((p) => Math.max(1, p - 1))}
        >
          Previous
        </button>
        <span>
          Page {safeOpsPage} of {opsPages}
        </span>
        <button
          className="outline compact"
          disabled={safeOpsPage === opsPages}
          onClick={() => setOpsPage((p) => Math.min(opsPages, p + 1))}
        >
          Next
        </button>
      </div>
    </section>
  );

  const usagePanel = (full = false) => (
    <section className="panel">
      <div className="title">
        <h2>{full ? "Detailed usage" : "Top quota usage"}</h2>
        <span>{utilization}% total</span>
      </div>
      {(full ? usage : busiestTenants).map((u) => {
        const percent = pct(u.used, u.quota);
        return (
          <div className="tenant" key={u.tenant_id}>
            <div>
              <b>{u.name}</b>
              <span>{percent}%</span>
            </div>
            <div>
              <i
                className={percent >= 90 ? "hot" : ""}
                style={{ width: `${percent}%` }}
              />
            </div>
            <small>
              {u.used.toLocaleString()} / {u.quota.toLocaleString()} units
            </small>
          </div>
        );
      })}
      {!usage.length && <div className="empty small">No usage yet.</div>}
    </section>
  );

  const tenantsPanel = (full = false) => (
    <section className="panel wide">
      <div className="title">
        <h2>{full ? "Tenants" : "Tenant directory"}</h2>
        <span>{tenants.length} tenants</span>
      </div>
      <div className="tenantRows">
        {(full ? tenants : tenants.slice(0, 8)).map((t) => (
          <div className="tenantRow" key={t.id}>
            <span>
              <b>{t.name}</b>
              <code>
                {t.slug} · {t.id.slice(0, 8)}
              </code>
            </span>
            <span>{t.monthly_quota.toLocaleString()} units</span>
            <span>{t.concurrency_limit} concurrent</span>
            <span>{formatRelative(t.created_at)}</span>
          </div>
        ))}
        {!tenants.length && <div className="empty small">No tenants yet.</div>}
      </div>
    </section>
  );

  return (
    <main>
      <aside>
        <div className="brand">
          <span className="logo small">N</span>Nexora
        </div>
        <nav>
          {(["overview", "tenants", "operations", "usage"] as View[]).map(
            (view) => (
              <button
                key={view}
                className={activeView === view ? "active" : ""}
                onClick={() => {
                  setActiveView(view);
                  setOpsPage(1);
                }}
              >
                {view[0].toUpperCase() + view.slice(1)}
              </button>
            ),
          )}
        </nav>
        <button className="outline" onClick={signOut}>
          Sign out
        </button>
      </aside>
      <section className="content">
        <header>
          <div>
            <p className="eyebrow">CONTROL PLANE</p>
            <h1>{pageTitle}</h1>
            <p className="muted">
              {lastUpdated
                ? `Updated ${lastUpdated.toLocaleTimeString()}`
                : "Syncing live metrics"}
            </p>
          </div>
          <div className="actions">
            <span className={needsAttention ? "health warn" : "health"}>
              {needsAttention ? "Attention" : "Healthy"}
            </span>
            <button className="outline compact" onClick={refresh}>
              Refresh
            </button>
            <button onClick={() => setShowNew(true)}>Add tenant</button>
          </div>
        </header>
        {error && <p className="error alert">{error}</p>}
        <div className="metrics">
          <article>
            <span>Tenants</span>
            <strong>{tenants.length}</strong>
            <small>{avgConcurrency} avg concurrency</small>
          </article>
          <article>
            <span>In flight</span>
            <strong>{running + queued}</strong>
            <small>
              {running} running · {queued} queued
            </small>
          </article>
          <article>
            <span>Attention</span>
            <strong className={failed ? "red" : ""}>{failed}</strong>
            <small>{succeeded} successful recent ops</small>
          </article>
          <article>
            <span>Quota used</span>
            <strong>{utilization}%</strong>
            <small>
              {totalUsage.toLocaleString()} / {totalQuota.toLocaleString()}{" "}
              units
            </small>
          </article>
        </div>
        {activeView === "overview" && (
          <div className="grid">
            {operationsTable()}
            <div className="sideStack">
              {usagePanel()}
              {tenantsPanel()}
            </div>
          </div>
        )}
        {activeView === "tenants" && (
          <div className="single">{tenantsPanel(true)}</div>
        )}
        {activeView === "operations" && (
          <div className="single">{operationsTable(true)}</div>
        )}
        {activeView === "usage" && (
          <div className="single">{usagePanel(true)}</div>
        )}
        {showNew && (
          <div className="modal">
            <form onSubmit={createTenant}>
              <div className="title">
                <h2>Create tenant</h2>
                <button
                  type="button"
                  className="text"
                  onClick={() => setShowNew(false)}
                >
                  Close
                </button>
              </div>
              <label>
                Company name
                <input name="name" required />
              </label>
              <label>
                Slug
                <input name="slug" pattern="[a-z0-9-]+" required />
              </label>
              <label>
                Owner email
                <input name="email" type="email" required />
              </label>
              <label>
                Temporary password
                <input
                  name="password"
                  type="password"
                  minLength={10}
                  required
                />
              </label>
              <div className="split">
                <label>
                  Monthly quota
                  <input
                    name="monthly_quota"
                    type="number"
                    defaultValue="10000"
                  />
                </label>
                <label>
                  Concurrency
                  <input
                    name="concurrency_limit"
                    type="number"
                    defaultValue="5"
                  />
                </label>
              </div>
              <button>Create tenant</button>
            </form>
          </div>
        )}
      </section>
    </main>
  );
}
