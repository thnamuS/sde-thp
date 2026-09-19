package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/sumanth/cipherion-ai/internal/contracts"
	"github.com/sumanth/cipherion-ai/internal/platform"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

type gateway struct {
	cfg     platform.Config
	redis   *redis.Client
	http    *http.Client
	targets map[string]*url.URL
}

func main() {
	cfg := platform.LoadConfig()
	redisClient, err := platform.OpenRedis(context.Background(), cfg.RedisURL)
	if err != nil {
		panic(err)
	}
	defer redisClient.Close()
	g := &gateway{cfg: cfg, redis: redisClient, http: &http.Client{Timeout: cfg.RequestTimeout}, targets: map[string]*url.URL{}}
	for name, raw := range map[string]string{"access": cfg.AccessURL, "operations": cfg.OperationsURL, "usage": cfg.UsageURL} {
		target, err := url.Parse(raw)
		if err != nil {
			panic(err)
		}
		g.targets[name] = target
	}
	e := platform.NewServer("gateway")
	e.POST("/v1/auth/login", g.proxy("access", "/auth/login"))
	v1 := e.Group("/v1", g.authenticate, g.rateLimit)
	v1.GET("/tenant", g.proxy("access", "/tenant"))
	v1.POST("/api-keys", g.proxy("access", "/api-keys"))
	v1.POST("/operations", g.proxy("operations", "/operations"))
	v1.GET("/operations", g.proxy("operations", "/operations"))
	v1.GET("/operations/:id", g.proxyParam("operations", "/operations/:id"))
	v1.DELETE("/operations/:id", g.proxyParam("operations", "/operations/:id"))
	v1.POST("/operations/:id/redrive", g.proxyParam("operations", "/operations/:id/redrive"))
	v1.GET("/usage", g.proxy("usage", "/usage"))
	v1.GET("/events", g.proxy("usage", "/events"))
	v1.GET("/webhooks", g.proxy("usage", "/webhooks"))
	v1.POST("/webhooks", g.proxy("usage", "/webhooks"))
	v1.DELETE("/webhooks/:id", g.proxyParam("usage", "/webhooks/:id"))
	v1.POST("/worker/poll", g.proxy("operations", "/worker/poll"))
	v1.POST("/worker/operations/:id/heartbeat", g.proxyParam("operations", "/worker/operations/:id/heartbeat"))
	v1.POST("/worker/operations/:id/complete", g.proxyParam("operations", "/worker/operations/:id/complete"))
	v1.POST("/worker/operations/:id/fail", g.proxyParam("operations", "/worker/operations/:id/fail"))
	v1.POST("/assistant", g.assistant)
	v1.GET("/admin/tenants", g.requireAdmin(g.proxy("access", "/admin/tenants")))
	v1.POST("/admin/tenants", g.requireAdmin(g.proxy("access", "/admin/tenants")))
	v1.GET("/admin/operations", g.requireAdmin(g.proxy("operations", "/operations")))
	v1.GET("/admin/usage", g.requireAdmin(g.proxy("usage", "/admin/usage")))
	e.GET("/readyz", g.ready)
	platform.Start(e, cfg.Port)
}

func (g *gateway) assistant(c echo.Context) error {
	if g.cfg.OpenRouterKey == "" {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "OPENROUTER_API_KEY is not configured")
	}
	var input struct {
		Question string `json:"question"`
	}
	if err := c.Bind(&input); err != nil || strings.TrimSpace(input.Question) == "" {
		return echo.NewHTTPError(400, "question is required")
	}
	if len(input.Question) > 2000 {
		return echo.NewHTTPError(400, "question is too long")
	}
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, g.cfg.OperationsURL+"/operations?limit=20", nil)
	if err != nil {
		return err
	}
	req.Header.Set(platform.TenantHeader, c.Request().Header.Get(platform.TenantHeader))
	req.Header.Set(platform.RoleHeader, c.Request().Header.Get(platform.RoleHeader))
	response, err := g.http.Do(req)
	if err != nil {
		return echo.NewHTTPError(503, "operations service unavailable")
	}
	defer response.Body.Close()
	operationsBody, _ := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if response.StatusCode != 200 {
		return echo.NewHTTPError(502, "could not load operation context")
	}
	prompt := fmt.Sprintf("Answer the tenant's operations question using only the JSON context. Be concise, mention operation IDs when useful, and say when evidence is insufficient.\nQuestion: %s\nContext: %s", input.Question, operationsBody)
	for _, model := range []string{g.cfg.PrimaryModel, g.cfg.FallbackModel} {
		answer, callErr := g.askOpenRouter(c.Request().Context(), model, prompt)
		if callErr == nil {
			return c.JSON(200, map[string]string{"answer": answer, "model": model})
		}
	}
	return echo.NewHTTPError(502, "AI providers were unavailable")
}

func (g *gateway) askOpenRouter(ctx context.Context, model, prompt string) (string, error) {
	payload := map[string]any{"model": model, "messages": []map[string]string{{"role": "system", "content": "You are the Nexora operations assistant. Never invent operational facts."}, {"role": "user", "content": prompt}}, "temperature": 0.1, "max_tokens": 500}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.cfg.OpenRouterKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", g.cfg.PublicBaseURL)
	req.Header.Set("X-Title", "Nexora AI Assistant")
	response, err := g.http.Do(req)
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
		return "", fmt.Errorf("empty AI response")
	}
	return decoded.Choices[0].Message.Content, nil
}

func (g *gateway) authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		requestBody, err := io.ReadAll(io.LimitReader(c.Request().Body, 2<<20))
		if err != nil {
			return echo.NewHTTPError(400, "could not read request")
		}
		c.Request().Body = io.NopCloser(bytes.NewReader(requestBody))
		req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, g.cfg.AccessURL+"/internal/verify", nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-Internal-Secret", g.cfg.InternalSecret)
		req.Header.Set("Authorization", c.Request().Header.Get("Authorization"))
		req.Header.Set("X-API-Key", c.Request().Header.Get("X-API-Key"))
		response, err := g.http.Do(req)
		if err != nil {
			return echo.NewHTTPError(503, "authentication service unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return echo.NewHTTPError(401, "invalid credentials")
		}
		var principal contracts.Principal
		if err = json.NewDecoder(response.Body).Decode(&principal); err != nil {
			return err
		}
		c.Request().Header.Del(platform.TenantHeader)
		c.Request().Header.Del(platform.UserHeader)
		c.Request().Header.Del(platform.RoleHeader)
		c.Request().Header.Set(platform.TenantHeader, principal.TenantID)
		c.Request().Header.Set(platform.UserHeader, principal.UserID)
		c.Request().Header.Set(platform.RoleHeader, principal.Role)
		return next(c)
	}
}
func (g *gateway) rateLimit(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		tenant := c.Request().Header.Get(platform.TenantHeader)
		window := time.Now().Unix() / 60
		key := fmt.Sprintf("rate:%s:%d", tenant, window)
		script := redis.NewScript(`local n=redis.call('INCR',KEYS[1]);if n==1 then redis.call('EXPIRE',KEYS[1],ARGV[1]) end;return n`)
		count, err := script.Run(c.Request().Context(), g.redis, []string{key}, 70).Int64()
		if err != nil {
			return echo.NewHTTPError(503, "rate limiter unavailable")
		}
		limit := int64(120)
		if c.Request().Header.Get(platform.RoleHeader) == "admin" {
			limit = 600
		}
		c.Response().Header().Set("X-RateLimit-Limit", fmt.Sprint(limit))
		c.Response().Header().Set("X-RateLimit-Remaining", fmt.Sprint(max(limit-count, 0)))
		if count > limit {
			c.Response().Header().Set("Retry-After", "60")
			return echo.NewHTTPError(429, "tenant request rate exceeded")
		}
		return next(c)
	}
}
func (g *gateway) requireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Header.Get(platform.RoleHeader) != "admin" {
			return echo.NewHTTPError(403, "admin role required")
		}
		return next(c)
	}
}
func (g *gateway) proxy(service, path string) echo.HandlerFunc {
	return g.proxyPath(service, func(echo.Context) string { return path })
}
func (g *gateway) proxyParam(service, path string) echo.HandlerFunc {
	return g.proxyPath(service, func(c echo.Context) string {
		result := path
		for _, name := range c.ParamNames() {
			result = strings.ReplaceAll(result, ":"+name, url.PathEscape(c.Param(name)))
		}
		return result
	})
}
func (g *gateway) proxyPath(service string, path func(echo.Context) string) echo.HandlerFunc {
	return func(c echo.Context) error {
		target := g.targets[service]
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(response *http.Response) error {
			stripUpstreamCORSHeaders(response.Header)
			return nil
		}
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.URL.Path = path(c)
			req.URL.RawPath = ""
			req.Host = target.Host
			req.Header.Set("X-Request-ID", platform.RequestIDFrom(c))
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			problem := contracts.Problem{Type: "about:blank", Title: "Bad Gateway", Status: 502, Detail: "upstream service unavailable", Instance: r.URL.Path, RequestID: platform.RequestIDFrom(c)}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(502)
			_ = json.NewEncoder(w).Encode(problem)
		}
		proxy.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}

func stripUpstreamCORSHeaders(headers http.Header) {
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), "access-control-") {
			headers.Del(key)
		}
	}
}

func (g *gateway) ready(c echo.Context) error {
	status := map[string]string{}
	healthy := true
	for name, target := range g.targets {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String()+"/healthz", nil)
		response, err := g.http.Do(req)
		cancel()
		if err != nil || response.StatusCode != 200 {
			status[name] = "unavailable"
			healthy = false
		} else {
			status[name] = "ok"
		}
		if response != nil {
			response.Body.Close()
		}
	}
	code := 200
	if !healthy {
		code = 503
	}
	return c.JSON(code, map[string]any{"status": status})
}
