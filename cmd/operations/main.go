package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/sumanth/cipherion-ai/internal/contracts"
	"github.com/sumanth/cipherion-ai/internal/domain"
	"github.com/sumanth/cipherion-ai/internal/platform"
)

const streamName = "nexora:operations"

type service struct {
	db    *pgxpool.Pool
	redis *redis.Client
	dbos  dbos.Context
	cfg   platform.Config
	http  *http.Client
}

var runtimeService *service

type createInput struct {
	TaskType      string          `json:"task_type"`
	TargetURL     string          `json:"target_url"`
	Payload       json.RawMessage `json:"payload"`
	MaxAttempts   int             `json:"max_attempts"`
	Deadline      *time.Time      `json:"deadline,omitempty"`
	ExecutionMode string          `json:"execution_mode"`
	ScheduledFor  *time.Time      `json:"scheduled_for,omitempty"`
}

func main() {
	cfg := platform.LoadConfig()
	ctx := context.Background()
	database, err := platform.OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		panic(err)
	}
	defer database.Close()
	redisClient, err := platform.OpenRedis(ctx, cfg.RedisURL)
	if err != nil {
		panic(err)
	}
	defer redisClient.Close()
	dbosCtx, err := dbos.NewContext(ctx, dbos.Config{AppName: "nexora-operations", ApplicationVersion: "1.0.0", DatabaseURL: cfg.DBOSDatabaseURL})
	if err != nil {
		panic(err)
	}
	s := &service{db: database, redis: redisClient, dbos: dbosCtx, cfg: cfg, http: &http.Client{Timeout: 10 * time.Second}}
	runtimeService = s
	dbos.RegisterWorkflow(dbosCtx, queueOperationWorkflow)
	if err = dbos.Launch(dbosCtx); err != nil {
		panic(err)
	}
	defer dbos.Shutdown(dbosCtx, 5*time.Second)
	if err := s.ensureConsumerGroup(ctx); err != nil {
		panic(err)
	}
	go s.dispatchEvents(ctx)
	go s.reapExpiredLeases(ctx)
	e := platform.NewServer("operations")
	e.POST("/operations", s.create, platform.RequireTenant)
	e.GET("/operations", s.list, platform.RequireTenant)
	e.GET("/operations/:id", s.get, platform.RequireTenant)
	e.DELETE("/operations/:id", s.cancel, platform.RequireTenant)
	e.POST("/operations/:id/redrive", s.redrive, platform.RequireTenant)
	e.POST("/worker/poll", s.workerPoll, platform.RequireTenant)
	e.POST("/worker/operations/:id/heartbeat", s.workerHeartbeat, platform.RequireTenant)
	e.POST("/worker/operations/:id/complete", s.workerComplete, platform.RequireTenant)
	e.POST("/worker/operations/:id/fail", s.workerFail, platform.RequireTenant)
	internal := e.Group("/internal", platform.RequireInternal(cfg.InternalSecret))
	internal.POST("/operations/:id/claim", s.claim)
	internal.POST("/operations/:id/heartbeat", s.heartbeat)
	internal.POST("/operations/:id/complete", s.complete)
	internal.POST("/operations/:id/fail", s.fail)
	internal.GET("/operations/:id", s.getInternal)
	platform.Start(e, cfg.Port)
}

func (s *service) ensureConsumerGroup(ctx context.Context) error {
	err := s.redis.XGroupCreateMkStream(ctx, streamName, "workers", "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func queueOperationWorkflow(ctx dbos.Context, envelope contracts.QueueEnvelope) (string, error) {
	if envelope.NotBefore != nil {
		if delay := time.Until(*envelope.NotBefore); delay > 0 {
			dbos.Sleep(ctx, delay)
		}
	}
	return dbos.RunAsStep(ctx, func(stepCtx context.Context) (string, error) {
		return enqueueOperationStep(stepCtx, envelope)
	}, dbos.WithStepName("enqueue-redis-operation"))
}

func enqueueOperationStep(ctx context.Context, envelope contracts.QueueEnvelope) (string, error) {
	s := runtimeService
	if s == nil {
		return "", errors.New("operations runtime unavailable")
	}
	result, err := s.db.Exec(ctx, `UPDATE operations.operations SET status='QUEUED',updated_at=now(),version=version+1 WHERE id=$1 AND status IN ('PENDING','RETRYING')`, envelope.OperationID)
	if err != nil {
		return "", err
	}
	if result.RowsAffected() == 0 {
		var status string
		if err = s.db.QueryRow(ctx, `SELECT status FROM operations.operations WHERE id=$1`, envelope.OperationID).Scan(&status); err != nil {
			return "", err
		}
		if status != "QUEUED" {
			return envelope.OperationID, nil
		}
	}
	_, err = s.db.Exec(ctx, `INSERT INTO operations.transition_log(operation_id,tenant_id,from_status,to_status,reason) VALUES($1,$2,NULL,'QUEUED','DBOS workflow enqueued operation')`, envelope.OperationID, envelope.TenantID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	if err = s.redis.XAdd(ctx, &redis.XAddArgs{Stream: streamName, Values: map[string]any{"envelope": string(payload)}}).Err(); err != nil {
		return "", err
	}
	return envelope.OperationID, nil
}

func (s *service) create(c echo.Context) error {
	var in createInput
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	if in.TaskType == "" || in.TargetURL == "" {
		return echo.NewHTTPError(400, "task_type and target_url are required")
	}
	if !strings.HasPrefix(in.TargetURL, "http://") && !strings.HasPrefix(in.TargetURL, "https://") {
		return echo.NewHTTPError(400, "target_url must use http or https")
	}
	if len(in.Payload) == 0 {
		in.Payload = json.RawMessage(`{}`)
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 3
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > 10 {
		return echo.NewHTTPError(400, "max_attempts must be between 1 and 10")
	}
	if in.ExecutionMode == "" {
		in.ExecutionMode = "PRIVATE"
	}
	if in.ExecutionMode != "PRIVATE" && in.ExecutionMode != "CLOUD" {
		return echo.NewHTTPError(400, "execution_mode must be PRIVATE or CLOUD")
	}
	tenantID := c.Request().Header.Get(platform.TenantHeader)
	idem := strings.TrimSpace(c.Request().Header.Get("Idempotency-Key"))
	if idem == "" {
		return echo.NewHTTPError(400, "Idempotency-Key header is required")
	}
	if len(idem) > 200 {
		return echo.NewHTTPError(400, "Idempotency-Key is too long")
	}
	var quota, used int64
	var limit, running int
	if err := s.db.QueryRow(c.Request().Context(), `SELECT monthly_quota,concurrency_limit FROM access.tenants WHERE id=$1`, tenantID).Scan(&quota, &limit); err != nil {
		return err
	}
	if err := s.db.QueryRow(c.Request().Context(), `SELECT COALESCE(sum(units),0) FROM usage.ledger WHERE tenant_id=$1 AND created_at>=date_trunc('month',now())`, tenantID).Scan(&used); err != nil {
		return err
	}
	if err := s.db.QueryRow(c.Request().Context(), `SELECT count(*) FROM operations.operations WHERE tenant_id=$1 AND status='RUNNING'`, tenantID).Scan(&running); err != nil {
		return err
	}
	if used >= quota {
		return echo.NewHTTPError(http.StatusPaymentRequired, "monthly quota exhausted")
	}
	if running >= limit {
		return echo.NewHTTPError(http.StatusTooManyRequests, "tenant concurrency limit reached")
	}
	id := uuid.NewString()
	_, err := s.db.Exec(c.Request().Context(), `INSERT INTO operations.operations(id,tenant_id,task_type,execution_mode,target_url,payload,status,max_attempts,deadline,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,'PENDING',$7,$8,$9) ON CONFLICT(tenant_id,idempotency_key) DO NOTHING`, id, tenantID, in.TaskType, in.ExecutionMode, in.TargetURL, in.Payload, in.MaxAttempts, in.Deadline, idem)
	if err != nil {
		return err
	}
	var op operation
	if err = s.scanOne(c.Request().Context(), `SELECT `+operationColumns+` FROM operations.operations WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID, idem, &op); err != nil {
		return err
	}
	if op.ID != id {
		return c.JSON(http.StatusOK, op)
	}
	envelope := contracts.QueueEnvelope{OperationID: id, TenantID: tenantID, TaskType: in.TaskType, TargetURL: in.TargetURL, Payload: in.Payload, Attempt: 0, MaxAttempts: in.MaxAttempts, Deadline: in.Deadline, ExecutionMode: in.ExecutionMode, NotBefore: in.ScheduledFor}
	_, err = dbos.RunWorkflow(s.dbos, queueOperationWorkflow, envelope, dbos.WithWorkflowID("operation:"+id))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, op)
}

type operation struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	TaskType      string          `json:"task_type"`
	ExecutionMode string          `json:"execution_mode"`
	TargetURL     string          `json:"target_url"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	Attempt       int             `json:"attempt"`
	MaxAttempts   int             `json:"max_attempts"`
	Deadline      *time.Time      `json:"deadline,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	ErrorCode     *string         `json:"error_code,omitempty"`
	ErrorMessage  *string         `json:"error_message,omitempty"`
	AIExplanation *string         `json:"ai_explanation,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

const operationColumns = `id,tenant_id,task_type,execution_mode,target_url,payload,status,attempt,max_attempts,deadline,result,error_code,error_message,ai_explanation,created_at,updated_at`

func scanOperation(row pgx.Row, o *operation) error {
	return row.Scan(&o.ID, &o.TenantID, &o.TaskType, &o.ExecutionMode, &o.TargetURL, &o.Payload, &o.Status, &o.Attempt, &o.MaxAttempts, &o.Deadline, &o.Result, &o.ErrorCode, &o.ErrorMessage, &o.AIExplanation, &o.CreatedAt, &o.UpdatedAt)
}
func (s *service) scanOne(ctx context.Context, q string, a1 any, a2 any, o *operation) error {
	return scanOperation(s.db.QueryRow(ctx, q, a1, a2), o)
}
func (s *service) get(c echo.Context) error {
	var o operation
	tenant := c.Request().Header.Get(platform.TenantHeader)
	if err := s.scanOne(c.Request().Context(), `SELECT `+operationColumns+` FROM operations.operations WHERE id=$1 AND tenant_id=$2`, c.Param("id"), tenant, &o); errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(404, "operation not found")
	} else if err != nil {
		return err
	}
	return c.JSON(200, o)
}
func (s *service) getInternal(c echo.Context) error {
	var o operation
	err := scanOperation(s.db.QueryRow(c.Request().Context(), `SELECT `+operationColumns+` FROM operations.operations WHERE id=$1`, c.Param("id")), &o)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(404, "operation not found")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, o)
}
func (s *service) list(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	status := c.QueryParam("status")
	admin := c.Request().Header.Get(platform.RoleHeader) == "admin"
	args := []any{limit}
	q := `SELECT ` + operationColumns + ` FROM operations.operations`
	where := []string{}
	if !admin {
		args = append(args, tenant)
		where = append(where, fmt.Sprintf("tenant_id=$%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY created_at DESC LIMIT $1`
	rows, err := s.db.Query(c.Request().Context(), q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []operation{}
	for rows.Next() {
		var o operation
		if err := rows.Scan(&o.ID, &o.TenantID, &o.TaskType, &o.ExecutionMode, &o.TargetURL, &o.Payload, &o.Status, &o.Attempt, &o.MaxAttempts, &o.Deadline, &o.Result, &o.ErrorCode, &o.ErrorMessage, &o.AIExplanation, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return err
		}
		items = append(items, o)
	}
	return c.JSON(200, map[string]any{"items": items})
}

func (s *service) cancel(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	result, err := s.db.Exec(c.Request().Context(), `UPDATE operations.operations SET status='CANCELLED',lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND tenant_id=$2 AND status IN ('PENDING','QUEUED','RUNNING','RETRYING','FAILED')`, c.Param("id"), tenant)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		var status string
		if scanErr := s.db.QueryRow(c.Request().Context(), `SELECT status FROM operations.operations WHERE id=$1 AND tenant_id=$2`, c.Param("id"), tenant).Scan(&status); errors.Is(scanErr, pgx.ErrNoRows) {
			return echo.NewHTTPError(404, "operation not found")
		} else if scanErr != nil {
			return scanErr
		}
		if domain.IsTerminal(domain.OperationStatus(status)) || !domain.CanTransition(domain.OperationStatus(status), domain.Cancelled) {
			return echo.NewHTTPError(409, "operation is already terminal or not cancellable")
		}
		return echo.NewHTTPError(409, "operation could not be cancelled")
	}
	return c.NoContent(204)
}
func (s *service) redrive(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var envelope contracts.QueueEnvelope
	err := s.db.QueryRow(c.Request().Context(), `UPDATE operations.operations SET status='RETRYING',attempt=0,error_code=NULL,error_message=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND tenant_id=$2 AND status IN ('FAILED','DEAD_LETTERED') RETURNING id,tenant_id,task_type,execution_mode,target_url,payload,attempt,max_attempts,deadline`, c.Param("id"), tenant).Scan(&envelope.OperationID, &envelope.TenantID, &envelope.TaskType, &envelope.ExecutionMode, &envelope.TargetURL, &envelope.Payload, &envelope.Attempt, &envelope.MaxAttempts, &envelope.Deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(409, "operation is not redrivable")
	}
	if err != nil {
		return err
	}
	_, err = dbos.RunWorkflow(s.dbos, queueOperationWorkflow, envelope, dbos.WithWorkflowID("redrive:"+envelope.OperationID+":"+uuid.NewString()))
	if err != nil {
		return err
	}
	return c.JSON(202, map[string]string{"id": envelope.OperationID, "status": "RETRYING"})
}

func (s *service) claim(c echo.Context) error {
	var in struct {
		WorkerID     string `json:"worker_id"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	if in.LeaseSeconds <= 0 {
		in.LeaseSeconds = 30
	}
	tenant, claimed, err := s.claimOperation(c.Request().Context(), c.Param("id"), "", in.WorkerID, in.LeaseSeconds)
	if err != nil {
		return err
	}
	if !claimed {
		return echo.NewHTTPError(409, "operation is not claimable")
	}
	return c.JSON(200, map[string]string{"tenant_id": tenant, "status": "RUNNING"})
}

func (s *service) claimOperation(ctx context.Context, operationID, expectedTenant, workerID string, leaseSeconds int) (string, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)

	var tenant string
	query := `SELECT tenant_id FROM operations.operations WHERE id=$1 AND status IN ('QUEUED','RETRYING') FOR UPDATE`
	args := []any{operationID}
	if expectedTenant != "" {
		query = `SELECT tenant_id FROM operations.operations WHERE id=$1 AND tenant_id=$2 AND status IN ('QUEUED','RETRYING') FOR UPDATE`
		args = append(args, expectedTenant)
	}
	if err = tx.QueryRow(ctx, query, args...).Scan(&tenant); errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}

	var limit, running int
	if err = tx.QueryRow(ctx, `SELECT concurrency_limit FROM access.tenants WHERE id=$1 FOR UPDATE`, tenant).Scan(&limit); err != nil {
		return "", false, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM operations.operations WHERE tenant_id=$1 AND status='RUNNING'`, tenant).Scan(&running); err != nil {
		return "", false, err
	}
	if running >= limit {
		return tenant, false, nil
	}

	result, err := tx.Exec(ctx, `UPDATE operations.operations SET status='RUNNING',attempt=attempt+1,lease_owner=$2,lease_expires_at=now()+make_interval(secs=>$3),updated_at=now(),version=version+1 WHERE id=$1 AND tenant_id=$4 AND status IN ('QUEUED','RETRYING')`, operationID, workerID, leaseSeconds, tenant)
	if err != nil {
		return "", false, err
	}
	if result.RowsAffected() != 1 {
		return tenant, false, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return tenant, true, nil
}
func (s *service) heartbeat(c echo.Context) error {
	var in struct {
		WorkerID string `json:"worker_id"`
	}
	_ = c.Bind(&in)
	result, err := s.db.Exec(c.Request().Context(), `UPDATE operations.operations SET lease_expires_at=now()+interval '30 seconds',updated_at=now() WHERE id=$1 AND status='RUNNING' AND lease_owner=$2`, c.Param("id"), in.WorkerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return echo.NewHTTPError(409, "lease not owned")
	}
	return c.NoContent(204)
}
func (s *service) complete(c echo.Context) error {
	var in struct {
		WorkerID      string          `json:"worker_id"`
		Result        json.RawMessage `json:"result"`
		AIExplanation string          `json:"ai_explanation"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	result, err := s.db.Exec(c.Request().Context(), `UPDATE operations.operations SET status='SUCCEEDED',result=$3,ai_explanation=$4,error_code=NULL,error_message=NULL,lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND status='RUNNING' AND lease_owner=$2`, c.Param("id"), in.WorkerID, in.Result, in.AIExplanation)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return echo.NewHTTPError(409, "operation cannot be completed")
	}
	return c.NoContent(204)
}
func (s *service) fail(c echo.Context) error {
	var in struct {
		WorkerID      string `json:"worker_id"`
		ErrorCode     string `json:"error_code"`
		ErrorMessage  string `json:"error_message"`
		AIExplanation string `json:"ai_explanation"`
		Retryable     bool   `json:"retryable"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	var envelope contracts.QueueEnvelope
	var attempt int
	err := s.db.QueryRow(c.Request().Context(), `SELECT id,tenant_id,task_type,execution_mode,target_url,payload,attempt,max_attempts,deadline FROM operations.operations WHERE id=$1 AND status='RUNNING' AND lease_owner=$2`, c.Param("id"), in.WorkerID).Scan(&envelope.OperationID, &envelope.TenantID, &envelope.TaskType, &envelope.ExecutionMode, &envelope.TargetURL, &envelope.Payload, &attempt, &envelope.MaxAttempts, &envelope.Deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(409, "operation cannot be failed")
	}
	if err != nil {
		return err
	}
	envelope.Attempt = attempt
	status := "DEAD_LETTERED"
	if in.Retryable && attempt < envelope.MaxAttempts {
		status = "RETRYING"
	}
	result, err := s.db.Exec(c.Request().Context(), `UPDATE operations.operations SET status=$3,error_code=$4,error_message=$5,ai_explanation=$6,lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND lease_owner=$2 AND status='RUNNING'`, c.Param("id"), in.WorkerID, status, in.ErrorCode, in.ErrorMessage, in.AIExplanation)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return echo.NewHTTPError(409, "operation cannot be failed")
	}
	if status == "RETRYING" {
		delay := domain.RetryDelay(attempt)
		envelope.NotBefore = timePointer(time.Now().Add(delay))
		if _, runErr := dbos.RunWorkflow(s.dbos, queueOperationWorkflow, envelope, dbos.WithWorkflowID(fmt.Sprintf("retry:%s:%d", envelope.OperationID, attempt))); runErr != nil {
			return runErr
		}
	}
	return c.JSON(200, map[string]string{"status": status})
}

func (s *service) workerPoll(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var in struct {
		WorkerID string `json:"worker_id"`
	}
	_ = c.Bind(&in)
	if in.WorkerID == "" {
		return echo.NewHTTPError(400, "worker_id is required")
	}
	cursorKey := "private-worker-cursor:" + tenant
	cursor, err := s.redis.Get(c.Request().Context(), cursorKey).Result()
	if err == redis.Nil {
		cursor = "0-0"
	} else if err != nil {
		return err
	}
	streams, err := s.redis.XRead(c.Request().Context(), &redis.XReadArgs{Streams: []string{streamName, cursor}, Count: 50, Block: 2 * time.Second}).Result()
	if err == redis.Nil {
		return c.NoContent(204)
	}
	if err != nil {
		return err
	}
	for _, stream := range streams {
		for _, message := range stream.Messages {
			raw, ok := message.Values["envelope"].(string)
			if !ok {
				_ = s.redis.Set(c.Request().Context(), cursorKey, message.ID, 0).Err()
				continue
			}
			var env contracts.QueueEnvelope
			if json.Unmarshal([]byte(raw), &env) != nil || env.TenantID != tenant || env.ExecutionMode != "PRIVATE" {
				_ = s.redis.Set(c.Request().Context(), cursorKey, message.ID, 0).Err()
				continue
			}
			_, claimed, err := s.claimOperation(c.Request().Context(), env.OperationID, tenant, in.WorkerID, 45)
			if err != nil {
				return err
			}
			if claimed {
				_ = s.redis.Set(c.Request().Context(), cursorKey, message.ID, 0).Err()
				env.Attempt++
				return c.JSON(200, env)
			}
			var status string
			if err := s.db.QueryRow(c.Request().Context(), `SELECT status FROM operations.operations WHERE id=$1 AND tenant_id=$2`, env.OperationID, tenant).Scan(&status); errors.Is(err, pgx.ErrNoRows) || !domain.CanTransition(domain.OperationStatus(status), domain.Running) {
				_ = s.redis.Set(c.Request().Context(), cursorKey, message.ID, 0).Err()
				continue
			} else if err != nil {
				return err
			}
			return c.NoContent(204)
		}
	}
	return c.NoContent(204)
}
func (s *service) workerHeartbeat(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var in struct {
		WorkerID string `json:"worker_id"`
	}
	_ = c.Bind(&in)
	result, err := s.db.Exec(c.Request().Context(), `UPDATE operations.operations SET lease_expires_at=now()+interval '45 seconds',updated_at=now() WHERE id=$1 AND tenant_id=$2 AND status='RUNNING' AND lease_owner=$3`, c.Param("id"), tenant, in.WorkerID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return echo.NewHTTPError(409, "lease not owned")
	}
	return c.NoContent(204)
}
func (s *service) workerComplete(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var in struct {
		WorkerID      string          `json:"worker_id"`
		Result        json.RawMessage `json:"result"`
		AIExplanation string          `json:"ai_explanation"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	tx, err := s.db.Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())
	var attempt int
	err = tx.QueryRow(c.Request().Context(), `UPDATE operations.operations SET status='SUCCEEDED',result=$4,ai_explanation=$5,error_code=NULL,error_message=NULL,lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND tenant_id=$2 AND status='RUNNING' AND lease_owner=$3 RETURNING attempt`, c.Param("id"), tenant, in.WorkerID, in.Result, in.AIExplanation).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(409, "operation cannot be completed")
	}
	if err != nil {
		return err
	}
	if err = insertEvent(c.Request().Context(), tx, fmt.Sprintf("%s:operation.succeeded:%d", c.Param("id"), attempt), "operation.succeeded", c.Param("id"), tenant, in.Result); err != nil {
		return err
	}
	if err = tx.Commit(c.Request().Context()); err != nil {
		return err
	}
	return c.NoContent(204)
}
func (s *service) workerFail(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var in struct {
		WorkerID      string `json:"worker_id"`
		ErrorCode     string `json:"error_code"`
		ErrorMessage  string `json:"error_message"`
		AIExplanation string `json:"ai_explanation"`
		Retryable     bool   `json:"retryable"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	var env contracts.QueueEnvelope
	err := s.db.QueryRow(c.Request().Context(), `SELECT id,tenant_id,task_type,execution_mode,target_url,payload,attempt,max_attempts,deadline FROM operations.operations WHERE id=$1 AND tenant_id=$2 AND status='RUNNING' AND lease_owner=$3`, c.Param("id"), tenant, in.WorkerID).Scan(&env.OperationID, &env.TenantID, &env.TaskType, &env.ExecutionMode, &env.TargetURL, &env.Payload, &env.Attempt, &env.MaxAttempts, &env.Deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(409, "operation cannot be failed")
	}
	if err != nil {
		return err
	}
	status := "DEAD_LETTERED"
	if in.Retryable && env.Attempt < env.MaxAttempts {
		status = "RETRYING"
	}
	tx, err := s.db.Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())
	result, err := tx.Exec(c.Request().Context(), `UPDATE operations.operations SET status=$4,error_code=$5,error_message=$6,ai_explanation=$7,lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND tenant_id=$2 AND lease_owner=$3 AND status='RUNNING'`, env.OperationID, tenant, in.WorkerID, status, in.ErrorCode, in.ErrorMessage, in.AIExplanation)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return echo.NewHTTPError(409, "operation cannot be failed")
	}
	eventType := "operation.failed"
	if status == "DEAD_LETTERED" {
		eventType = "operation.dead_lettered"
	}
	eventPayload, _ := json.Marshal(map[string]any{"status": status, "error_code": in.ErrorCode, "error_message": in.ErrorMessage, "retryable": in.Retryable})
	if err = insertEvent(c.Request().Context(), tx, fmt.Sprintf("%s:%s:%d", env.OperationID, eventType, env.Attempt), eventType, env.OperationID, tenant, eventPayload); err != nil {
		return err
	}
	if err = tx.Commit(c.Request().Context()); err != nil {
		return err
	}
	if status == "RETRYING" {
		delay := domain.RetryDelay(env.Attempt)
		env.NotBefore = timePointer(time.Now().Add(delay))
		if _, runErr := dbos.RunWorkflow(s.dbos, queueOperationWorkflow, env, dbos.WithWorkflowID(fmt.Sprintf("retry:%s:%d", env.OperationID, env.Attempt))); runErr != nil {
			return runErr
		}
	}
	return c.JSON(200, map[string]string{"status": status})
}

func insertEvent(ctx context.Context, tx pgx.Tx, eventKey, eventType, operationID, tenantID string, payload []byte) error {
	if len(payload) == 0 || !json.Valid(payload) {
		payload = []byte(`{}`)
	}
	_, err := tx.Exec(ctx, `INSERT INTO operations.event_outbox(event_key,event_type,operation_id,tenant_id,units,payload) VALUES($1,$2,$3,$4,1,$5) ON CONFLICT(event_key) DO NOTHING`, eventKey, eventType, operationID, tenantID, payload)
	return err
}

func (s *service) dispatchEvents(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchEventBatch(ctx)
		}
	}
}
func (s *service) dispatchEventBatch(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT id,event_key,event_type,operation_id,tenant_id,units,payload,attempt FROM operations.event_outbox WHERE status='PENDING' AND next_attempt_at<=now() ORDER BY created_at LIMIT 20`)
	if err != nil {
		return
	}
	type item struct {
		id, eventKey, eventType, operationID, tenantID string
		units                                          int64
		payload                                        []byte
		attempt                                        int
	}
	items := []item{}
	for rows.Next() {
		var x item
		if rows.Scan(&x.id, &x.eventKey, &x.eventType, &x.operationID, &x.tenantID, &x.units, &x.payload, &x.attempt) == nil {
			items = append(items, x)
		}
	}
	rows.Close()
	for _, x := range items {
		event := contracts.OperationEvent{EventKey: x.eventKey, EventType: x.eventType, OperationID: x.operationID, TenantID: x.tenantID, Units: x.units, Payload: x.payload, OccurredAt: time.Now()}
		body, _ := json.Marshal(event)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.UsageURL+"/internal/events", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Internal-Secret", s.cfg.InternalSecret)
			var response *http.Response
			response, err = s.http.Do(req)
			if response != nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode < 200 || response.StatusCode >= 300 {
					err = fmt.Errorf("usage service returned %d", response.StatusCode)
				}
			}
		}
		if err == nil {
			_, _ = s.db.Exec(ctx, `UPDATE operations.event_outbox SET status='DELIVERED',delivered_at=now(),attempt=attempt+1 WHERE id=$1`, x.id)
		} else {
			delay := time.Duration(1<<min(x.attempt+1, 8)) * time.Second
			_, _ = s.db.Exec(ctx, `UPDATE operations.event_outbox SET attempt=attempt+1,last_error=$2,next_attempt_at=now()+make_interval(secs=>$3) WHERE id=$1`, x.id, err.Error(), int(delay.Seconds()))
		}
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func (s *service) reapExpiredLeases(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapExpiredLeaseBatch(ctx)
		}
	}
}

func (s *service) reapExpiredLeaseBatch(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT id,tenant_id,task_type,execution_mode,target_url,payload,attempt,max_attempts,deadline FROM operations.operations WHERE status='RUNNING' AND lease_expires_at<now() ORDER BY lease_expires_at LIMIT 50`)
	if err != nil {
		return
	}
	envelopes := []contracts.QueueEnvelope{}
	for rows.Next() {
		var env contracts.QueueEnvelope
		if rows.Scan(&env.OperationID, &env.TenantID, &env.TaskType, &env.ExecutionMode, &env.TargetURL, &env.Payload, &env.Attempt, &env.MaxAttempts, &env.Deadline) == nil {
			envelopes = append(envelopes, env)
		}
	}
	rows.Close()
	for _, env := range envelopes {
		if env.Attempt >= env.MaxAttempts {
			tx, err := s.db.Begin(ctx)
			if err != nil {
				continue
			}
			result, err := tx.Exec(ctx, `UPDATE operations.operations SET status='DEAD_LETTERED',error_code='LEASE_EXPIRED',error_message='worker lease expired',lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND status='RUNNING' AND lease_expires_at<now()`, env.OperationID)
			if err == nil && result.RowsAffected() == 1 {
				payload, _ := json.Marshal(map[string]string{"error": "worker lease expired"})
				err = insertEvent(ctx, tx, fmt.Sprintf("%s:operation.dead_lettered:%d", env.OperationID, env.Attempt), "operation.dead_lettered", env.OperationID, env.TenantID, payload)
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				tx.Rollback(ctx)
			}
			continue
		}
		result, err := s.db.Exec(ctx, `UPDATE operations.operations SET status='RETRYING',error_code='LEASE_EXPIRED',error_message='worker lease expired; operation requeued',lease_owner=NULL,lease_expires_at=NULL,updated_at=now(),version=version+1 WHERE id=$1 AND status='RUNNING' AND lease_expires_at<now()`, env.OperationID)
		if err != nil || result.RowsAffected() == 0 {
			continue
		}
		env.NotBefore = timePointer(time.Now().Add(2 * time.Second))
		_, _ = dbos.RunWorkflow(s.dbos, queueOperationWorkflow, env, dbos.WithWorkflowID(fmt.Sprintf("lease-recovery:%s:%d", env.OperationID, env.Attempt)))
	}
}
