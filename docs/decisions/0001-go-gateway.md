# ADR 0001: Go/Echo API gateway

Status: accepted by project owner.

The supplied assessment specification requested a Node.js/TypeScript API Gateway. The project owner explicitly selected Go/Echo after being shown the conflict. The gateway therefore uses the same language and HTTP framework as the domain services.

Consequences: the repository has one backend toolchain and shared middleware, but this is a known literal deviation from the assessment specification and must be explained during review.
