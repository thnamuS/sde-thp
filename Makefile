.PHONY: dev down test test-unit fmt lint migrate smoke

dev:
	docker compose up --build

down:
	docker compose down

test:
	go test ./...
	cd web/customer && npm test
	cd web/admin && npm test

test-unit:
	go test ./internal/... ./cmd/...

fmt:
	gofmt -w cmd internal tests

lint:
	go vet ./...
	cd web/customer && npm run lint
	cd web/admin && npm run lint

migrate:
	docker compose run --rm migrate

smoke:
	./scripts/smoke.sh
