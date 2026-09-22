import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const customerPage = readFileSync(new URL("../web/customer/app/page.tsx", import.meta.url), "utf8");
const adminPage = readFileSync(new URL("../web/admin/app/page.tsx", import.meta.url), "utf8");

function assertSourceIncludes(source, values) {
  for (const value of values) {
    assert.match(source, new RegExp(value.replace(/[/?]/g, "\\$&")));
  }
}

test("customer console keeps the expected API contract", () => {
  assertSourceIncludes(customerPage, [
    "/v1/auth/login",
    "/v1/tenant",
    "/v1/operations?limit=50",
    "/v1/usage",
    "/v1/webhooks",
    "/v1/assistant",
  ]);
});

test("customer console advertises all operation lifecycle statuses", () => {
  assertSourceIncludes(customerPage, [
    "PENDING",
    "QUEUED",
    "RUNNING",
    "SUCCEEDED",
    "FAILED",
    "RETRYING",
    "DEAD_LETTERED",
    "CANCELLED",
  ]);
});

test("customer console persists auth in a customer-scoped localStorage key", () => {
  assert.match(customerPage, /const TOKEN_KEY = "nexora_customer_token";/);
  assert.match(customerPage, /window\.localStorage\.setItem\(TOKEN_KEY, body\.access_token\)/);
  assert.match(customerPage, /window\.localStorage\.removeItem\(TOKEN_KEY\)/);
});

test("admin console keeps the expected admin API contract", () => {
  assertSourceIncludes(adminPage, [
    "/v1/auth/login",
    "/v1/admin/tenants",
    "/v1/admin/operations?limit=50",
    "/v1/admin/usage",
  ]);
});

test("admin console protects admin-only authentication", () => {
  assert.match(adminPage, /const TOKEN_KEY = "nexora_admin_token";/);
  assert.match(adminPage, /if \(b\.role !== "admin"\) throw new Error\("Administrator role required"\);/);
  assert.match(adminPage, /window\.localStorage\.removeItem\(TOKEN_KEY\)/);
});

test("admin console highlights operational attention states and usage percentage", () => {
  assert.match(adminPage, /const attentionStatuses = \["FAILED", "DEAD_LETTERED"\];/);
  assert.match(adminPage, /function pct\(used: number, quota: number\)/);
  assert.match(adminPage, /Math\.min\(100, Math\.round\(\(used \/ quota\) \* 100\)\)/);
});
