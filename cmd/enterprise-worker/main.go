package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cipherion-ai/nexora/internal/contracts"
	"github.com/google/uuid"
)

type agent struct {
	apiURL, apiKey, targetOverride, openRouterKey, primaryModel, fallbackModel, id string
	http                                                                           *http.Client
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	hostname, _ := os.Hostname()
	a := &agent{apiURL: env("NEXORA_API_URL", "http://localhost:8080"), apiKey: os.Getenv("ENTERPRISE_WORKER_API_KEY"), targetOverride: os.Getenv("ENTERPRISE_TARGET_URL"), openRouterKey: os.Getenv("OPENROUTER_API_KEY"), primaryModel: env("OPENROUTER_PRIMARY_MODEL", "google/gemini-2.5-flash"), fallbackModel: env("OPENROUTER_FALLBACK_MODEL", "meta-llama/llama-3.3-70b-instruct"), id: hostname + ":" + uuid.NewString()[:8], http: &http.Client{Timeout: 45 * time.Second}}
	if a.apiKey == "" {
		panic("ENTERPRISE_WORKER_API_KEY is required")
	}
	slog.Info("enterprise worker started", "worker_id", a.id, "outbound_only", true)
	a.run(ctx)
}
func (a *agent) run(ctx context.Context) {
	for ctx.Err() == nil {
		var operation contracts.QueueEnvelope
		status, err := a.call(ctx, http.MethodPost, "/v1/worker/poll", map[string]string{"worker_id": a.id}, &operation)
		if err != nil {
			slog.Warn("poll failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if status == 204 {
			continue
		}
		a.process(ctx, operation)
	}
}
func (a *agent) process(ctx context.Context, operation contracts.QueueEnvelope) {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	go a.heartbeat(heartbeatCtx, operation.OperationID)
	target := operation.TargetURL
	if a.targetOverride != "" {
		target = a.targetOverride
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(operation.Payload))
	status := 0
	var result []byte
	if err == nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", operation.OperationID)
		request.Header.Set("X-Nexora-Operation-ID", operation.OperationID)
		var response *http.Response
		response, err = a.http.Do(request)
		if response != nil {
			status = response.StatusCode
			result, _ = io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if status < 200 || status >= 300 {
				err = fmt.Errorf("target returned HTTP %d: %s", status, result)
			}
		}
	}
	cancel()
	if err == nil {
		if !json.Valid(result) {
			result = mustJSON(map[string]any{"body": string(result)})
		}
		_, callErr := a.call(ctx, http.MethodPost, "/v1/worker/operations/"+operation.OperationID+"/complete", map[string]any{"worker_id": a.id, "result": json.RawMessage(result), "ai_explanation": a.explain(ctx, operation, result, "")}, nil)
		if callErr != nil {
			slog.Error("completion failed", "operation_id", operation.OperationID, "error", callErr)
		}
		return
	}
	retryable := status == 0 || status == 408 || status == 429 || status >= 500
	_, callErr := a.call(ctx, http.MethodPost, "/v1/worker/operations/"+operation.OperationID+"/fail", map[string]any{"worker_id": a.id, "error_code": errorCode(status), "error_message": err.Error(), "retryable": retryable, "ai_explanation": a.explain(ctx, operation, result, err.Error())}, nil)
	if callErr != nil {
		slog.Error("failure report failed", "operation_id", operation.OperationID, "error", callErr)
	}
}
func (a *agent) heartbeat(ctx context.Context, id string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = a.call(ctx, http.MethodPost, "/v1/worker/operations/"+id+"/heartbeat", map[string]string{"worker_id": a.id}, nil)
		}
	}
}
func (a *agent) call(ctx context.Context, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.apiURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-Key", a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return response.StatusCode, fmt.Errorf("Nexora returned %d: %s", response.StatusCode, data)
	}
	if output != nil && response.StatusCode != 204 {
		if err = json.NewDecoder(response.Body).Decode(output); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}
func (a *agent) explain(ctx context.Context, operation contracts.QueueEnvelope, result []byte, failure string) string {
	if a.openRouterKey == "" {
		if failure != "" {
			return "The private worker could not complete the operation. Check the target service, network path, and error details."
		}
		return "The private worker completed the operation successfully."
	}
	prompt := fmt.Sprintf("Explain this private job outcome concisely without inventing facts. Task: %s\nResult: %s\nError: %s", operation.TaskType, truncate(string(result), 2500), truncate(failure, 2500))
	for _, model := range []string{a.primaryModel, a.fallbackModel} {
		payload := map[string]any{"model": model, "messages": []map[string]string{{"role": "system", "content": "You explain automation outcomes for operations engineers."}, {"role": "user", "content": prompt}}, "temperature": 0.1, "max_tokens": 250}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+a.openRouterKey)
		req.Header.Set("Content-Type", "application/json")
		response, err := a.http.Do(req)
		if err != nil {
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			var decoded struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			err = json.NewDecoder(response.Body).Decode(&decoded)
			response.Body.Close()
			if err == nil && len(decoded.Choices) > 0 {
				return decoded.Choices[0].Message.Content
			}
		} else {
			response.Body.Close()
		}
	}
	return "AI explanation was unavailable; inspect the private worker error details."
}
func env(k, v string) string {
	if x := os.Getenv(k); x != "" {
		return x
	}
	return v
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
func errorCode(status int) string {
	switch {
	case status == 0:
		return "NETWORK_ERROR"
	case status == 408:
		return "TARGET_TIMEOUT"
	case status == 429:
		return "TARGET_RATE_LIMITED"
	case status >= 500:
		return "TARGET_SERVER_ERROR"
	default:
		return "TARGET_REJECTED"
	}
}

var _ = errors.Is
var _ = strings.TrimSpace
