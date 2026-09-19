package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sumanth/cipherion-ai/internal/platform"
)

func TestProxyReturnsSingleGatewayCORSHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Add("Access-Control-Allow-Origin", "http://localhost:3000")
		writer.Header().Add("Access-Control-Allow-Origin", "http://localhost:3000")
		writer.Header().Add("Access-Control-Allow-Credentials", "true")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	gateway := &gateway{targets: map[string]*url.URL{"access": target}}
	e := platform.NewServer("test-gateway")
	e.GET("/proxy", gateway.proxy("access", "/upstream"))

	request := httptest.NewRequest(http.MethodGet, "/proxy", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "http://localhost:3000" {
		t.Fatalf("expected one gateway CORS origin header, got %v", got)
	}
	if got := response.Header().Values("Access-Control-Allow-Credentials"); len(got) != 1 || got[0] != "true" {
		t.Fatalf("expected one gateway CORS credentials header, got %v", got)
	}
}
