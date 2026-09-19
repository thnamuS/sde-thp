package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cipherion-ai/nexora/internal/platform"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

type service struct {
	db  *pgxpool.Pool
	cfg platform.Config
}

type claims struct {
	TenantID string `json:"tenant_id"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

func main() {
	cfg := platform.LoadConfig()
	db, err := platform.OpenDB(context.Background(), cfg.DatabaseURL)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	s := &service{db: db, cfg: cfg}
	if err := s.bootstrap(context.Background()); err != nil {
		panic(err)
	}
	e := platform.NewServer("access")
	e.POST("/auth/login", s.login)
	e.POST("/internal/verify", s.verify, platform.RequireInternal(cfg.InternalSecret))
	e.POST("/api-keys", s.createAPIKey, platform.RequireTenant)
	e.GET("/tenant", s.getTenant, platform.RequireTenant)
	e.GET("/admin/tenants", s.listTenants, platform.RequireTenant)
	e.POST("/admin/tenants", s.createTenant, platform.RequireTenant)
	platform.Start(e, cfg.Port)
}

func (s *service) bootstrap(ctx context.Context) error {
	password, err := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	var tenantID string
	err = s.db.QueryRow(ctx, `INSERT INTO access.tenants(name, slug) VALUES ('Nexora Administration','nexora-admin') ON CONFLICT(slug) DO UPDATE SET name=EXCLUDED.name RETURNING id`).Scan(&tenantID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO access.users(tenant_id,email,password_hash,role) VALUES($1,$2,$3,'admin') ON CONFLICT(email) DO NOTHING`, tenantID, strings.ToLower(s.cfg.AdminEmail), string(password))
	if err != nil {
		return err
	}
	acmeKey := os.Getenv("ACME_API_KEY")
	if acmeKey == "" {
		return nil
	}
	var acmeTenant string
	if err = s.db.QueryRow(ctx, `INSERT INTO access.tenants(name,slug,monthly_quota,concurrency_limit) VALUES('Acme Support','acme-support',10000,10) ON CONFLICT(slug) DO UPDATE SET name=EXCLUDED.name RETURNING id`).Scan(&acmeTenant); err != nil {
		return err
	}
	acmePassword, err := bcrypt.GenerateFromPassword([]byte("AcmeDemo123!"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO access.users(tenant_id,email,password_hash,role) VALUES($1,'ops@acme.test',$2,'customer') ON CONFLICT(email) DO NOTHING`, acmeTenant, string(acmePassword))
	if err != nil {
		return err
	}
	keyHash := sha256.Sum256([]byte(acmeKey))
	prefix := acmeKey
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	_, err = s.db.Exec(ctx, `INSERT INTO access.api_keys(tenant_id,name,prefix,key_hash) VALUES($1,'local demo',$2,$3) ON CONFLICT(key_hash) DO NOTHING`, acmeTenant, prefix, hex.EncodeToString(keyHash[:]))
	return err
}

func (s *service) login(c echo.Context) error {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.Bind(&input); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
	}
	var userID, tenantID, role, passwordHash string
	err := s.db.QueryRow(c.Request().Context(), `SELECT id, tenant_id, role, password_hash FROM access.users WHERE email=$1`, strings.ToLower(strings.TrimSpace(input.Email))).Scan(&userID, &tenantID, &role, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid email or password")
	}
	if err != nil {
		return err
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{TenantID: tenantID, Role: role, RegisteredClaims: jwt.RegisteredClaims{Subject: userID, Issuer: "nexora", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(8 * time.Hour))}})
	signed, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"access_token": signed, "expires_in": 28800, "tenant_id": tenantID, "role": role})
}

func (s *service) verify(c echo.Context) error {
	if raw := bearer(c.Request().Header.Get("Authorization")); raw != "" {
		parsed, err := jwt.ParseWithClaims(raw, &claims{}, func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(s.cfg.JWTSecret), nil
		}, jwt.WithIssuer("nexora"))
		if err != nil || !parsed.Valid {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
		}
		cl := parsed.Claims.(*claims)
		return c.JSON(http.StatusOK, map[string]string{"tenant_id": cl.TenantID, "user_id": cl.Subject, "role": cl.Role})
	}
	key := strings.TrimSpace(c.Request().Header.Get("X-API-Key"))
	if key == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "credentials required")
	}
	hash := sha256.Sum256([]byte(key))
	var tenantID string
	err := s.db.QueryRow(c.Request().Context(), `UPDATE access.api_keys SET last_used_at=now() WHERE key_hash=$1 AND revoked_at IS NULL RETURNING tenant_id`, hex.EncodeToString(hash[:])).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid API key")
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"tenant_id": tenantID, "role": "service"})
}

func (s *service) createAPIKey(c echo.Context) error {
	tenantID := c.Request().Header.Get(platform.TenantHeader)
	var input struct {
		Name string `json:"name"`
	}
	_ = c.Bind(&input)
	if strings.TrimSpace(input.Name) == "" {
		input.Name = "default"
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return err
	}
	raw := "nx_live_" + base64.RawURLEncoding.EncodeToString(secretBytes)
	hash := sha256.Sum256([]byte(raw))
	prefix := raw[:16]
	var id string
	err := s.db.QueryRow(c.Request().Context(), `INSERT INTO access.api_keys(tenant_id,name,prefix,key_hash) VALUES($1,$2,$3,$4) RETURNING id`, tenantID, input.Name, prefix, hex.EncodeToString(hash[:])).Scan(&id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]string{"id": id, "name": input.Name, "prefix": prefix, "api_key": raw})
}

func (s *service) getTenant(c echo.Context) error {
	id := c.Request().Header.Get(platform.TenantHeader)
	var name, slug string
	var quota int64
	var concurrency int
	if err := s.db.QueryRow(c.Request().Context(), `SELECT name,slug,monthly_quota,concurrency_limit FROM access.tenants WHERE id=$1`, id).Scan(&name, &slug, &quota, &concurrency); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"id": id, "name": name, "slug": slug, "monthly_quota": quota, "concurrency_limit": concurrency})
}

func (s *service) listTenants(c echo.Context) error {
	if c.Request().Header.Get(platform.RoleHeader) != "admin" {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	rows, err := s.db.Query(c.Request().Context(), `SELECT id,name,slug,monthly_quota,concurrency_limit,created_at FROM access.tenants ORDER BY created_at DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, slug string
		var quota int64
		var limit int
		var created time.Time
		if err := rows.Scan(&id, &name, &slug, &quota, &limit, &created); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "name": name, "slug": slug, "monthly_quota": quota, "concurrency_limit": limit, "created_at": created})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items})
}

func (s *service) createTenant(c echo.Context) error {
	if c.Request().Header.Get(platform.RoleHeader) != "admin" {
		return echo.NewHTTPError(http.StatusForbidden, "admin role required")
	}
	var input struct {
		Name             string `json:"name"`
		Slug             string `json:"slug"`
		Email            string `json:"email"`
		Password         string `json:"password"`
		MonthlyQuota     int64  `json:"monthly_quota"`
		ConcurrencyLimit int    `json:"concurrency_limit"`
	}
	if err := c.Bind(&input); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	if input.Name == "" || input.Slug == "" || input.Email == "" || len(input.Password) < 10 {
		return echo.NewHTTPError(400, "name, slug, email and a 10+ character password are required")
	}
	if input.MonthlyQuota == 0 {
		input.MonthlyQuota = 10000
	}
	if input.ConcurrencyLimit == 0 {
		input.ConcurrencyLimit = 5
	}
	tx, err := s.db.Begin(c.Request().Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request().Context())
	id := uuid.NewString()
	_, err = tx.Exec(c.Request().Context(), `INSERT INTO access.tenants(id,name,slug,monthly_quota,concurrency_limit) VALUES($1,$2,$3,$4,$5)`, id, input.Name, input.Slug, input.MonthlyQuota, input.ConcurrencyLimit)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = tx.Exec(c.Request().Context(), `INSERT INTO access.users(tenant_id,email,password_hash,role) VALUES($1,$2,$3,'customer')`, id, strings.ToLower(input.Email), string(hash))
	if err != nil {
		return err
	}
	if err = tx.Commit(c.Request().Context()); err != nil {
		return err
	}
	slog.Info("tenant created", "tenant_id", id)
	return c.JSON(http.StatusCreated, map[string]string{"id": id})
}

func bearer(value string) string {
	parts := strings.SplitN(value, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}
