package contracts

import (
	"encoding/json"
	"time"
)

type Principal struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id,omitempty"`
	Role     string `json:"role"`
}

type QueueEnvelope struct {
	OperationID   string          `json:"operation_id"`
	TenantID      string          `json:"tenant_id"`
	TaskType      string          `json:"task_type"`
	TargetURL     string          `json:"target_url"`
	Payload       json.RawMessage `json:"payload"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"max_attempts"`
	Deadline      *time.Time      `json:"deadline,omitempty"`
	ExecutionMode string          `json:"execution_mode"`
	NotBefore     *time.Time      `json:"not_before,omitempty"`
}

type OperationEvent struct {
	EventKey    string          `json:"event_key"`
	EventType   string          `json:"event_type"`
	OperationID string          `json:"operation_id"`
	TenantID    string          `json:"tenant_id"`
	Units       int64           `json:"units"`
	Payload     json.RawMessage `json:"payload"`
	OccurredAt  time.Time       `json:"occurred_at"`
}

type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id"`
}
