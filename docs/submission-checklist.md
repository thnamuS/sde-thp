# Submission checklist

## Day 1 — core reliability

- Run all tests and Compose smoke test.
- Demonstrate idempotency, tenant isolation, retries, DLQ, cancellation, quota, and webhook recovery.
- Add the real OpenRouter key and verify both model paths.

## Day 2 — security and polish

- Rotate development secrets.
- Add a payload-reference/encryption option if sensitive payloads are in scope.
- Add TLS termination and restrict target URL policy for optional cloud execution.
- Capture screenshots and a short architecture walkthrough.

## Day 3 — rehearsal

- Start from a clean `docker compose down -v` and reproduce setup.
- Kill the Operations service during a DBOS workflow and demonstrate recovery.
- Kill a private worker during execution and demonstrate lease recovery/redelivery.
- Review logs using request IDs and explain rate limit versus monthly quota.

## Day 4 — reserved buffer

- No feature additions. Review, rehearse, and fix only release-blocking issues.
