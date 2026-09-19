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
	"github.com/cipherion-ai/nexora/internal/platform"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const streamName = "nexora:operations"

type worker struct {
	cfg   platform.Config
	redis *redis.Client
	http  *http.Client
	id    string
}

func main() {
	cfg := platform.LoadConfig()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	redisClient, err := platform.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		panic(err)
	}
	defer redisClient.Close()
	hostname, _ := os.Hostname()
	w := &worker{cfg: cfg, redis: redisClient, http: &http.Client{Timeout: cfg.RequestTimeout}, id: hostname + ":" + uuid.NewString()[:8]}
	slog.Info("worker started", "worker_id", w.id)
	if err = w.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		panic(err)
	}
}
func (w *worker) run(ctx context.Context) error {
	for {
		streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "workers", Consumer: w.id, Streams: []string{streamName, ">"}, Count: 1, Block: 5 * time.Second}).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return err
		}
		for _, stream := range streams {
			for _, message := range stream.Messages {
				w.process(ctx, message)
			}
		}
	}
}
func (w *worker) process(ctx context.Context, message redis.XMessage) {
	raw, ok := message.Values["envelope"].(string)
	if !ok {
		_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
		return
	}
	var env contracts.QueueEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
		return
	}
	if env.Deadline != nil && time.Now().After(*env.Deadline) {
		w.reportFailure(ctx, env, "DEADLINE_EXCEEDED", "operation deadline exceeded", false, "")
		_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
		return
	}
	if env.ExecutionMode != "CLOUD" {
		_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
		return
	}
	if err := w.postJSON(ctx, w.cfg.OperationsURL+"/internal/operations/"+env.OperationID+"/claim", map[string]any{"worker_id": w.id, "lease_seconds": 45}, nil); err != nil {
		slog.Warn("claim rejected", "operation_id", env.OperationID, "error", err)
		_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
		return
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	go w.heartbeat(heartbeatCtx, env.OperationID)
	result, status, err := w.execute(ctx, env)
	stopHeartbeat()
	if err == nil {
		explanation := w.explain(ctx, env, result, "")
		completeErr := w.postJSON(ctx, w.cfg.OperationsURL+"/internal/operations/"+env.OperationID+"/complete", map[string]any{"worker_id": w.id, "result": json.RawMessage(result), "ai_explanation": explanation}, nil)
		if completeErr == nil {
			w.emit(ctx, env, "operation.succeeded", 1, result)
			_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
		}
		return
	}
	retryable := status == 0 || status == 408 || status == 429 || status >= 500
	explanation := w.explain(ctx, env, nil, err.Error())
	w.reportFailure(ctx, env, errorCode(status), err.Error(), retryable, explanation)
	if !retryable || env.Attempt+1 >= env.MaxAttempts {
		w.emit(ctx, env, "operation.dead_lettered", 1, mustJSON(map[string]any{"error": err.Error()}))
	} else {
		w.emit(ctx, env, "operation.failed", 1, mustJSON(map[string]any{"error": err.Error(), "retryable": true}))
	}
	_ = w.redis.XAck(ctx, streamName, "workers", message.ID).Err()
}
func (w *worker) heartbeat(ctx context.Context, operationID string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = w.postJSON(ctx, w.cfg.OperationsURL+"/internal/operations/"+operationID+"/heartbeat", map[string]string{"worker_id": w.id}, nil)
		}
	}
}
func (w *worker) execute(ctx context.Context, env contracts.QueueEnvelope) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.TargetURL, bytes.NewReader(env.Payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nexora-Operation-ID", env.OperationID)
	req.Header.Set("Idempotency-Key", env.OperationID)
	response, err := w.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return body, response.StatusCode, fmt.Errorf("target returned HTTP %d: %s", response.StatusCode, string(body))
	}
	if !json.Valid(body) {
		body = mustJSON(map[string]any{"body": string(body), "status": response.StatusCode})
	}
	return body, response.StatusCode, nil
}
func (w *worker) reportFailure(ctx context.Context, env contracts.QueueEnvelope, code, message string, retryable bool, explanation string) {
	_ = w.postJSON(ctx, w.cfg.OperationsURL+"/internal/operations/"+env.OperationID+"/fail", map[string]any{"worker_id": w.id, "error_code": code, "error_message": message, "retryable": retryable, "ai_explanation": explanation}, nil)
}
func (w *worker) emit(ctx context.Context, env contracts.QueueEnvelope, eventType string, units int64, payload []byte) {
	event := contracts.OperationEvent{EventKey: fmt.Sprintf("%s:%s:%d", env.OperationID, eventType, env.Attempt), EventType: eventType, OperationID: env.OperationID, TenantID: env.TenantID, Units: units, Payload: payload, OccurredAt: time.Now()}
	_ = w.postJSON(ctx, w.cfg.UsageURL+"/internal/events", event, nil)
}
func (w *worker) postJSON(ctx context.Context, url string, input any, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", w.cfg.InternalSecret)
	response, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return fmt.Errorf("%s returned %d: %s", url, response.StatusCode, message)
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}
func (w *worker) explain(ctx context.Context, env contracts.QueueEnvelope, result []byte, failure string) string {
	if w.cfg.OpenRouterKey == "" {
		if failure != "" {
			return "The operation failed after calling the configured target. Review the HTTP error and confirm the target is reachable and accepts the submitted payload."
		}
		return "The operation completed successfully and the target acknowledged the request."
	}
	prompt := fmt.Sprintf("Explain this automation operation outcome concisely for an operations engineer. Task type: %s. Result: %s. Error: %s. Include likely cause and one next action. Never invent facts.", env.TaskType, truncate(string(result), 3000), truncate(failure, 3000))
	for _, model := range []string{w.cfg.PrimaryModel, w.cfg.FallbackModel} {
		answer, err := w.callOpenRouter(ctx, model, prompt)
		if err == nil {
			return answer
		}
		slog.Warn("AI provider attempt failed", "model", model, "error", err)
	}
	return "AI explanation was unavailable. Inspect the recorded result or error and retry after confirming the target service health."
}
func (w *worker) callOpenRouter(ctx context.Context, model, prompt string) (string, error) {
	payload := map[string]any{"model": model, "messages": []map[string]string{{"role": "system", "content": "You explain job execution outcomes accurately and briefly."}, {"role": "user", "content": prompt}}, "temperature": 0.1, "max_tokens": 300}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.OpenRouterKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", w.cfg.PublicBaseURL)
	req.Header.Set("X-Title", "Nexora AI")
	response, err := w.http.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("OpenRouter returned %d", response.StatusCode)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err = json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return "", errors.New("OpenRouter returned no completion")
	}
	return decoded.Choices[0].Message.Content, nil
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
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
