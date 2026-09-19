package platform

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/sumanth/cipherion-ai/internal/contracts"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

const (
	TenantHeader = "X-Nexora-Tenant-ID"
	UserHeader   = "X-Nexora-User-ID"
	RoleHeader   = "X-Nexora-Role"
)

func NewServer(name string) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Secure())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     []string{"http://localhost:3000", "http://localhost:3001"},
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization, "X-API-Key", "Idempotency-Key", "X-Request-ID"},
		AllowCredentials: true,
	}))
	e.Use(RequestID())
	e.Use(Recover(name))
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "service": name})
	})
	e.HTTPErrorHandler = ProblemHandler
	return e
}

func RequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := strings.TrimSpace(c.Request().Header.Get("X-Request-ID"))
			if id == "" {
				b := make([]byte, 12)
				_, _ = rand.Read(b)
				id = hex.EncodeToString(b)
			}
			c.Set("request_id", id)
			c.Response().Header().Set("X-Request-ID", id)
			start := time.Now()
			err := next(c)
			slog.Info("request", "request_id", id, "method", c.Request().Method, "path", c.Path(), "status", c.Response().Status, "duration_ms", time.Since(start).Milliseconds())
			return err
		}
	}
}

func Recover(service string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic", "service", service, "error", r, "stack", string(debug.Stack()))
					err = echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
				}
			}()
			return next(c)
		}
	}
}

func ProblemHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status, detail := http.StatusInternalServerError, "internal server error"
	if he, ok := err.(*echo.HTTPError); ok {
		status = he.Code
		if message, ok := he.Message.(string); ok {
			detail = message
		} else {
			detail = http.StatusText(status)
		}
	}
	problem := contracts.Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Instance: c.Request().URL.Path, RequestID: RequestIDFrom(c)}
	_ = c.JSON(status, problem)
}

func RequestIDFrom(c echo.Context) string {
	value, _ := c.Get("request_id").(string)
	return value
}

func RequireInternal(secret string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if secret == "" || c.Request().Header.Get("X-Internal-Secret") != secret {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid internal credentials")
			}
			return next(c)
		}
	}
}

func RequireTenant(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Header.Get(TenantHeader) == "" {
			return echo.NewHTTPError(http.StatusUnauthorized, "tenant context missing")
		}
		return next(c)
	}
}

func Start(e *echo.Echo, port string) {
	if err := e.Start(":" + port); err != nil && err != http.ErrServerClosed {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
