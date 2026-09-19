package platform

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port            string
	DatabaseURL     string
	DBOSDatabaseURL string
	RedisURL        string
	JWTSecret       string
	InternalSecret  string
	AccessURL       string
	OperationsURL   string
	UsageURL        string
	OpenRouterKey   string
	PrimaryModel    string
	FallbackModel   string
	PublicBaseURL   string
	AdminEmail      string
	AdminPassword   string
	RequestTimeout  time.Duration
}

func LoadConfig() Config {
	return Config{
		Port:            env("PORT", "8080"),
		DatabaseURL:     env("DATABASE_URL", "postgres://nexora:nexora_dev@localhost:5432/nexora?sslmode=disable"),
		DBOSDatabaseURL: env("DBOS_SYSTEM_DATABASE_URL", env("DATABASE_URL", "postgres://nexora:nexora_dev@localhost:5432/nexora?sslmode=disable")),
		RedisURL:        env("REDIS_URL", "redis://localhost:6379/0"),
		JWTSecret:       env("JWT_SECRET", "development-only-secret-change-me"),
		InternalSecret:  env("INTERNAL_SECRET", "development-internal-secret"),
		AccessURL:       env("ACCESS_URL", "http://localhost:8081"),
		OperationsURL:   env("OPERATIONS_URL", "http://localhost:8082"),
		UsageURL:        env("USAGE_URL", "http://localhost:8083"),
		OpenRouterKey:   os.Getenv("OPENROUTER_API_KEY"),
		PrimaryModel:    env("OPENROUTER_PRIMARY_MODEL", "google/gemini-2.5-flash"),
		FallbackModel:   env("OPENROUTER_FALLBACK_MODEL", "meta-llama/llama-3.3-70b-instruct"),
		PublicBaseURL:   env("PUBLIC_BASE_URL", "http://localhost:8080"),
		AdminEmail:      env("ADMIN_EMAIL", "admin@nexora.local"),
		AdminPassword:   env("ADMIN_PASSWORD", "ChangeMe123!"),
		RequestTimeout:  time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 30)) * time.Second,
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}
