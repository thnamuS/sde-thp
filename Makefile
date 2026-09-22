.PHONY: dev down test test-unit fmt lint migrate smoke

dev:
	docker compose up --build

down:
	docker compose down

test:
	go test ./...
	node --test tests/*.test.mjs

test-unit:
	go test ./internal/... ./cmd/...

fmt:
	gofmt -w cmd internal tests

lint:
	go vet ./...
	cd web/customer && npm run lint
	cd web/admin && npm run lint

migrate:
	docker compose up -d postgres

smoke:
	./scripts/smoke.sh
