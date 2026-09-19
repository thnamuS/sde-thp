package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/cipherion-ai/nexora/internal/platform"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type server struct {
	cfg      platform.Config
	apiKey   string
	webhooks atomic.Bool
	requests atomic.Int64
	http     *http.Client
	apiURL   string
}

func main() {
	cfg := platform.LoadConfig()
	apiURL := os.Getenv("NEXORA_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}
	s := &server{cfg: cfg, apiKey: os.Getenv("ACME_API_KEY"), apiURL: apiURL, http: &http.Client{Timeout: 15 * time.Second}}
	s.webhooks.Store(true)
	e := platform.NewServer("acme")
	e.POST("/work", s.work)
	e.POST("/webhook", s.webhook)
	e.GET("/demo/normal", s.normal)
	e.GET("/demo/burst", s.burst)
	e.GET("/demo/idempotent", s.idempotent)
	e.POST("/demo/webhook/toggle", s.toggleWebhook)
	e.GET("/demo/quota-exhaust", s.quotaExhaust)
	e.GET("/demo/stats", func(c echo.Context) error {
		return c.JSON(200, map[string]any{"work_requests": s.requests.Load(), "webhooks_enabled": s.webhooks.Load()})
	})
	platform.Start(e, cfg.Port)
}
func (s *server) work(c echo.Context) error {
	s.requests.Add(1)
	var payload map[string]any
	_ = c.Bind(&payload)
	mode, _ := payload["mode"].(string)
	switch mode {
	case "fail":
		return c.JSON(500, map[string]string{"error": "simulated Acme outage"})
	case "reject":
		return c.JSON(422, map[string]string{"error": "simulated validation failure"})
	case "slow":
		select {
		case <-time.After(35 * time.Second):
		case <-c.Request().Context().Done():
			return c.Request().Context().Err()
		}
	}
	return c.JSON(200, map[string]any{"accepted": true, "processed_at": time.Now().UTC(), "operation_id": c.Request().Header.Get("X-Nexora-Operation-ID"), "input": payload})
}
func (s *server) webhook(c echo.Context) error {
	if !s.webhooks.Load() {
		return c.JSON(503, map[string]string{"error": "webhooks disabled"})
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	secret := os.Getenv("ACME_WEBHOOK_SECRET")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(c.Request().Header.Get("X-Nexora-Signature"))) {
			return c.JSON(401, map[string]string{"error": "invalid signature"})
		}
	}
	return c.NoContent(204)
}
func (s *server) normal(c echo.Context) error {
	return s.submit(c, "normal-"+uuid.NewString(), map[string]any{"ticket_id": "ACME-1001", "action": "summarize", "mode": "normal"})
}
func (s *server) burst(c echo.Context) error {
	count, _ := strconv.Atoi(c.QueryParam("count"))
	if count <= 0 {
		count = 25
	}
	if count > 100 {
		count = 100
	}
	results := make([]any, 0, count)
	for i := 0; i < count; i++ {
		result, status := s.createOperation(c.Request().Context(), fmt.Sprintf("burst-%s-%d", uuid.NewString(), i), map[string]any{"ticket_id": fmt.Sprintf("ACME-%d", i), "mode": "normal"})
		results = append(results, map[string]any{"status": status, "response": result})
	}
	return c.JSON(202, map[string]any{"submitted": count, "results": results})
}
func (s *server) idempotent(c echo.Context) error {
	key := "acme-fixed-idempotency-demo"
	first, a := s.createOperation(c.Request().Context(), key, map[string]any{"ticket_id": "ACME-IDEMPOTENT", "mode": "normal"})
	second, b := s.createOperation(c.Request().Context(), key, map[string]any{"ticket_id": "ACME-IDEMPOTENT", "mode": "normal"})
	return c.JSON(200, map[string]any{"first_status": a, "second_status": b, "first": first, "second": second})
}
func (s *server) toggleWebhook(c echo.Context) error {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	s.webhooks.Store(in.Enabled)
	return c.JSON(200, map[string]bool{"enabled": in.Enabled})
}
func (s *server) quotaExhaust(c echo.Context) error {
	count, _ := strconv.Atoi(c.QueryParam("count"))
	if count <= 0 {
		count = 20
	}
	if count > 100 {
		count = 100
	}
	accepted, rejected := 0, 0
	for i := 0; i < count; i++ {
		_, status := s.createOperation(c.Request().Context(), uuid.NewString(), map[string]any{"quota_test": i, "mode": "normal"})
		if status == 202 || status == 200 {
			accepted++
		} else {
			rejected++
			break
		}
	}
	return c.JSON(200, map[string]int{"attempted": accepted + rejected, "accepted": accepted, "rejected": rejected})
}
func (s *server) submit(c echo.Context, key string, payload any) error {
	result, status := s.createOperation(c.Request().Context(), key, payload)
	return c.JSON(status, result)
}
func (s *server) createOperation(ctx context.Context, key string, payload any) (any, int) {
	if s.apiKey == "" {
		return map[string]string{"error": "ACME_API_KEY is not configured"}, 503
	}
	input := map[string]any{"task_type": "acme.ticket.process", "target_url": "http://acme:8090/work", "payload": payload, "max_attempts": 3}
	body, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/v1/operations", bytes.NewReader(body))
	if err != nil {
		return map[string]string{"error": err.Error()}, 500
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", s.apiKey)
	req.Header.Set("Idempotency-Key", key)
	response, err := s.http.Do(req)
	if err != nil {
		return map[string]string{"error": err.Error()}, 502
	}
	defer response.Body.Close()
	var result any
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		return map[string]string{"error": err.Error()}, response.StatusCode
	}
	return result, response.StatusCode
}
