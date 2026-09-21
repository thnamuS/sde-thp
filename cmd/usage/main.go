package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/sumanth/cipherion-ai/internal/contracts"
	"github.com/sumanth/cipherion-ai/internal/platform"
)

type service struct {
	db          *pgxpool.Pool
	cfg         platform.Config
	http        *http.Client
	subMu       sync.Mutex
	subscribers map[string]map[chan []byte]struct{}
}

func main() {
	cfg := platform.LoadConfig()
	db, err := platform.OpenDB(context.Background(), cfg.DatabaseURL)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	s := &service{db: db, cfg: cfg, http: &http.Client{Timeout: 10 * time.Second}, subscribers: map[string]map[chan []byte]struct{}{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.dispatchLoop(ctx)
	e := platform.NewServer("usage")
	e.GET("/usage", s.getUsage, platform.RequireTenant)
	e.GET("/events", s.events, platform.RequireTenant)
	e.GET("/webhooks", s.listWebhooks, platform.RequireTenant)
	e.POST("/webhooks", s.createWebhook, platform.RequireTenant)
	e.DELETE("/webhooks/:id", s.deleteWebhook, platform.RequireTenant)
	e.GET("/admin/usage", s.adminUsage, platform.RequireTenant)
	e.POST("/internal/events", s.recordEvent, platform.RequireInternal(cfg.InternalSecret))
	platform.Start(e, cfg.Port)
}

func (s *service) recordEvent(c echo.Context) error {
	var event contracts.OperationEvent
	if err := c.Bind(&event); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	if event.EventKey == "" || event.OperationID == "" || event.TenantID == "" {
		return echo.NewHTTPError(400, "event_key, operation_id and tenant_id are required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	tx, err := s.db.Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())
	result, err := tx.Exec(c.Request().Context(), `INSERT INTO usage.ledger(tenant_id,operation_id,event_key,units,metadata) VALUES($1,$2,$3,$4,$5) ON CONFLICT(event_key) DO NOTHING`, event.TenantID, event.OperationID, event.EventKey, event.Units, event.Payload)
	if err != nil {
		return err
	}
	if result.RowsAffected() > 0 {
		_, err = tx.Exec(c.Request().Context(), `INSERT INTO usage.webhook_outbox(tenant_id,endpoint_id,event_type,operation_id,payload) SELECT $1,id,$2,$3,$4 FROM usage.webhook_endpoints WHERE tenant_id=$1 AND enabled=true`, event.TenantID, event.EventType, event.OperationID, event.Payload)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(c.Request().Context()); err != nil {
		return err
	}
	encoded, _ := json.Marshal(event)
	s.publish(event.TenantID, encoded)
	return c.NoContent(202)
}

func (s *service) getUsage(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var used, quota int64
	err := s.db.QueryRow(c.Request().Context(), `SELECT COALESCE((SELECT sum(units) FROM usage.ledger WHERE tenant_id=$1 AND created_at>=date_trunc('month',now())),0),monthly_quota FROM access.tenants WHERE id=$1`, tenant).Scan(&used, &quota)
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"used": used, "quota": quota, "remaining": max(quota-used, 0), "period_start": time.Now().UTC().Format("2006-01")})
}
func (s *service) adminUsage(c echo.Context) error {
	if c.Request().Header.Get(platform.RoleHeader) != "admin" {
		return echo.NewHTTPError(403, "admin role required")
	}
	rows, err := s.db.Query(c.Request().Context(), `SELECT t.id,t.name,t.monthly_quota,COALESCE(sum(l.units) FILTER(WHERE l.created_at>=date_trunc('month',now())),0) FROM access.tenants t LEFT JOIN usage.ledger l ON l.tenant_id=t.id GROUP BY t.id,t.name,t.monthly_quota ORDER BY 4 DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name string
		var quota, used int64
		if err := rows.Scan(&id, &name, &quota, &used); err != nil {
			return err
		}
		items = append(items, map[string]any{"tenant_id": id, "name": name, "quota": quota, "used": used})
	}
	return c.JSON(200, map[string]any{"items": items})
}

func (s *service) createWebhook(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	var in struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return echo.NewHTTPError(400, "webhook URL must use http or https")
	}
	if len(in.Secret) < 16 {
		return echo.NewHTTPError(400, "webhook secret must have at least 16 characters")
	}
	id := uuid.NewString()
	_, err := s.db.Exec(c.Request().Context(), `INSERT INTO usage.webhook_endpoints(id,tenant_id,url,secret) VALUES($1,$2,$3,$4)`, id, tenant, in.URL, in.Secret)
	if err != nil {
		return err
	}
	return c.JSON(201, map[string]string{"id": id, "url": in.URL})
}
func (s *service) listWebhooks(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	rows, err := s.db.Query(c.Request().Context(), `SELECT id,url,enabled,created_at FROM usage.webhook_endpoints WHERE tenant_id=$1 ORDER BY created_at DESC`, tenant)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, url string
		var enabled bool
		var created time.Time
		if err := rows.Scan(&id, &url, &enabled, &created); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "url": url, "enabled": enabled, "created_at": created})
	}
	return c.JSON(200, map[string]any{"items": items})
}
func (s *service) deleteWebhook(c echo.Context) error {
	result, err := s.db.Exec(c.Request().Context(), `DELETE FROM usage.webhook_endpoints WHERE id=$1 AND tenant_id=$2`, c.Param("id"), c.Request().Header.Get(platform.TenantHeader))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return echo.NewHTTPError(404, "webhook not found")
	}
	return c.NoContent(204)
}

func (s *service) events(c echo.Context) error {
	tenant := c.Request().Header.Get(platform.TenantHeader)
	c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
	c.Response().Header().Set(echo.HeaderCacheControl, "no-cache")
	c.Response().WriteHeader(200)
	ch := make(chan []byte, 16)
	s.subMu.Lock()
	if s.subscribers[tenant] == nil {
		s.subscribers[tenant] = map[chan []byte]struct{}{}
	}
	s.subscribers[tenant][ch] = struct{}{}
	s.subMu.Unlock()
	defer func() { s.subMu.Lock(); delete(s.subscribers[tenant], ch); close(ch); s.subMu.Unlock() }()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case data := <-ch:
			_, _ = fmt.Fprintf(c.Response(), "event: operation\ndata: %s\n\n", data)
			c.Response().Flush()
		case <-ticker.C:
			_, _ = io.WriteString(c.Response(), ": keepalive\n\n")
			c.Response().Flush()
		case <-c.Request().Context().Done():
			return nil
		}
	}
}
func (s *service) publish(tenant string, data []byte) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subscribers[tenant] {
		select {
		case ch <- data:
		default:
		}
	}
}

type delivery struct {
	ID, TenantID, URL, Secret, EventType, OperationID string
	Payload                                           []byte
	Attempt                                           int
}

func (s *service) dispatchLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchBatch(ctx)
		}
	}
}
func (s *service) dispatchBatch(ctx context.Context) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH picked AS (
		SELECT o.id
		FROM usage.webhook_outbox o
		WHERE o.status='PENDING' AND o.next_attempt_at<=now()
		ORDER BY o.created_at
		LIMIT 20
		FOR UPDATE SKIP LOCKED
	)
	UPDATE usage.webhook_outbox o
	SET status='IN_FLIGHT'
	FROM picked, usage.webhook_endpoints e
	WHERE o.id=picked.id AND e.id=o.endpoint_id
	RETURNING o.id,o.tenant_id,e.url,e.secret,o.event_type,o.operation_id,o.payload,o.attempt`)
	if err != nil {
		return
	}
	items := []delivery{}
	for rows.Next() {
		var d delivery
		if err := rows.Scan(&d.ID, &d.TenantID, &d.URL, &d.Secret, &d.EventType, &d.OperationID, &d.Payload, &d.Attempt); err == nil {
			items = append(items, d)
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return
	}
	if err = tx.Commit(ctx); err != nil {
		return
	}
	for _, d := range items {
		s.deliver(ctx, d)
	}
}
func (s *service) deliver(ctx context.Context, d delivery) {
	signature := hmac.New(sha256.New, []byte(d.Secret))
	signature.Write(d.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(d.Payload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Nexora-Event", d.EventType)
		req.Header.Set("X-Nexora-Signature", "sha256="+hex.EncodeToString(signature.Sum(nil)))
		req.Header.Set("X-Nexora-Delivery", d.ID)
		var response *http.Response
		response, err = s.http.Do(req)
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				err = fmt.Errorf("webhook returned %d", response.StatusCode)
			}
		}
	}
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE usage.webhook_outbox SET status='DELIVERED',delivered_at=now(),attempt=attempt+1 WHERE id=$1 AND status='IN_FLIGHT'`, d.ID)
		return
	}
	attempt := d.Attempt + 1
	if attempt >= 8 {
		_, _ = s.db.Exec(ctx, `UPDATE usage.webhook_outbox SET status='DEAD_LETTERED',attempt=$2,last_error=$3 WHERE id=$1 AND status='IN_FLIGHT'`, d.ID, attempt, err.Error())
		return
	}
	delay := time.Duration(1<<min(attempt, 8)) * time.Second
	_, _ = s.db.Exec(ctx, `UPDATE usage.webhook_outbox SET status='PENDING',attempt=$2,last_error=$3,next_attempt_at=now()+make_interval(secs=>$4) WHERE id=$1 AND status='IN_FLIGHT'`, d.ID, attempt, err.Error(), int(delay.Seconds()))
}

var _ = errors.Is
var _ = pgx.ErrNoRows
