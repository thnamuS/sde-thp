package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sumanth/cipherion-ai/internal/contracts"
	"github.com/sumanth/cipherion-ai/internal/platform"
)

func TestLoadConfigUsesDefaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DBOS_SYSTEM_DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("INTERNAL_SECRET", "")
	t.Setenv("ACCESS_URL", "")
	t.Setenv("OPERATIONS_URL", "")
	t.Setenv("USAGE_URL", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_PRIMARY_MODEL", "")
	t.Setenv("OPENROUTER_FALLBACK_MODEL", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("REQUEST_TIMEOUT_SECONDS", "")

	cfg := platform.LoadConfig()

	if cfg.Port != "8080" {
		t.Fatalf("expected default port 8080, got %q", cfg.Port)
	}
	if cfg.DBOSDatabaseURL != cfg.DatabaseURL {
		t.Fatalf("expected DBOS database URL to inherit database URL")
	}
	if cfg.RequestTimeout != 30*time.Second {
		t.Fatalf("expected default timeout 30s, got %s", cfg.RequestTimeout)
	}
}

func TestLoadConfigHonorsEnvironment(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DATABASE_URL", "postgres://app:pass@db:5432/app")
	t.Setenv("DBOS_SYSTEM_DATABASE_URL", "postgres://dbos:pass@db:5432/dbos")
	t.Setenv("REDIS_URL", "redis://cache:6379/1")
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	t.Setenv("REQUEST_TIMEOUT_SECONDS", "7")

	cfg := platform.LoadConfig()

	if cfg.Port != "9090" || cfg.RedisURL != "redis://cache:6379/1" {
		t.Fatalf("environment values were not loaded: %+v", cfg)
	}
	if cfg.DBOSDatabaseURL != "postgres://dbos:pass@db:5432/dbos" {
		t.Fatalf("expected explicit DBOS URL, got %q", cfg.DBOSDatabaseURL)
	}
	if cfg.RequestTimeout != 7*time.Second {
		t.Fatalf("expected 7s timeout, got %s", cfg.RequestTimeout)
	}
}

func TestNewServerHealthzAndRequestID(t *testing.T) {
	e := platform.NewServer("unit-service")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "test-request-id")
	response := httptest.NewRecorder()

	e.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Request-ID"); got != "test-request-id" {
		t.Fatalf("expected request ID to be echoed, got %q", got)
	}
}

func TestProblemHandlerIncludesRequestContext(t *testing.T) {
	e := platform.NewServer("problem-service")
	e.GET("/problem", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot, "short and stout")
	})

	request := httptest.NewRequest(http.MethodGet, "/problem", nil)
	request.Header.Set("X-Request-ID", "problem-request")
	response := httptest.NewRecorder()

	e.ServeHTTP(response, request)

	if response.Code != http.StatusTeapot {
		t.Fatalf("expected 418, got %d: %s", response.Code, response.Body.String())
	}
	var problem contracts.Problem
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Detail != "short and stout" || problem.Instance != "/problem" || problem.RequestID != "problem-request" {
		t.Fatalf("problem payload missing context: %+v", problem)
	}
}

func TestRequireInternalAndTenantMiddleware(t *testing.T) {
	e := platform.NewServer("protected-service")
	e.GET("/internal", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, platform.RequireInternal("shared-secret"))
	e.GET("/tenant", func(c echo.Context) error {
		return c.String(http.StatusOK, c.Request().Header.Get(platform.TenantHeader))
	}, platform.RequireTenant)

	unauthorized := httptest.NewRecorder()
	e.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing internal secret to be rejected, got %d", unauthorized.Code)
	}

	internalReq := httptest.NewRequest(http.MethodGet, "/internal", nil)
	internalReq.Header.Set("X-Internal-Secret", "shared-secret")
	acceptedInternal := httptest.NewRecorder()
	e.ServeHTTP(acceptedInternal, internalReq)
	if acceptedInternal.Code != http.StatusNoContent {
		t.Fatalf("expected valid internal secret to pass, got %d", acceptedInternal.Code)
	}

	tenantReq := httptest.NewRequest(http.MethodGet, "/tenant", nil)
	tenantReq.Header.Set(platform.TenantHeader, "tenant_123")
	acceptedTenant := httptest.NewRecorder()
	e.ServeHTTP(acceptedTenant, tenantReq)
	if acceptedTenant.Code != http.StatusOK || acceptedTenant.Body.String() != "tenant_123" {
		t.Fatalf("expected tenant context to pass, got %d %q", acceptedTenant.Code, acceptedTenant.Body.String())
	}
}

func TestQueueEnvelopeAndProblemJSONContracts(t *testing.T) {
	deadline := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	envelope := contracts.QueueEnvelope{
		OperationID:   "op_123",
		TenantID:      "tenant_123",
		TaskType:      "webhook.call",
		TargetURL:     "https://customer.example/jobs",
		Payload:       json.RawMessage(`{"priority":"high","retries":2}`),
		Attempt:       2,
		MaxAttempts:   5,
		Deadline:      &deadline,
		ExecutionMode: "private",
	}

	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded contracts.QueueEnvelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.Payload) != string(envelope.Payload) || decoded.Deadline == nil || !decoded.Deadline.Equal(deadline) {
		t.Fatalf("queue envelope changed after JSON round trip: %+v", decoded)
	}

	problem, err := json.Marshal(contracts.Problem{RequestID: "req_123"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(problem, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["request_id"]; !ok {
		t.Fatalf("expected snake_case request_id field in %s", problem)
	}
}
