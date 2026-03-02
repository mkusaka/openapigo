package openapigo

import (
	"net/http"
	"sync"
	"testing"
)

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient()
	if c.httpClient != http.DefaultClient {
		t.Error("default httpClient should be http.DefaultClient")
	}
	if c.baseURL != "" {
		t.Errorf("default baseURL should be empty, got %q", c.baseURL)
	}
	if c.codec == nil {
		t.Error("codec should not be nil")
	}
}

func TestNewClient_Options(t *testing.T) {
	hc := &http.Client{}
	c := NewClient(
		WithBaseURL("https://api.example.com"),
		WithHTTPClient(hc),
		WithDefaultHeader("X-Custom", "val"),
	)
	if c.baseURL != "https://api.example.com" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.httpClient != hc {
		t.Error("httpClient not set")
	}
	if c.headers.Get("X-Custom") != "val" {
		t.Error("header not set")
	}
}

func TestNewClient_HeaderClone(t *testing.T) {
	// Verify that headers are cloned during construction (ADR-024).
	h := make(http.Header)
	h.Set("X-Before", "1")

	c := NewClient(func(c *Client) {
		c.headers = h
	})

	// Mutating the original header should not affect the client.
	h.Set("X-After", "2")
	if c.headers.Get("X-After") != "" {
		t.Error("client headers should be independent of original")
	}
}

func TestNewClient_ConcurrentRead(t *testing.T) {
	c := NewClient(
		WithBaseURL("https://api.example.com"),
		WithDefaultHeader("Authorization", "Bearer token"),
	)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.baseURL
			_ = c.headers.Get("Authorization")
			_ = c.codec
		}()
	}
	wg.Wait()
}

func TestEndpoint_Basic(t *testing.T) {
	e := NewEndpoint[NoRequest, NoContent]("GET", "/pets/{petId}")
	if e.Method() != "GET" {
		t.Errorf("Method = %q", e.Method())
	}
	if e.Path() != "/pets/{petId}" {
		t.Errorf("Path = %q", e.Path())
	}
}

func TestEndpoint_IsSuccessStatus(t *testing.T) {
	e := NewEndpoint[NoRequest, NoContent]("GET", "/").WithSuccessCodes(200, 201)
	if !e.isSuccessStatus(200) {
		t.Error("200 should be success")
	}
	if !e.isSuccessStatus(201) {
		t.Error("201 should be success")
	}
	if e.isSuccessStatus(404) {
		t.Error("404 should not be success")
	}
}

func TestEndpoint_WithErrors_Copy(t *testing.T) {
	e1 := NewEndpoint[NoRequest, NoContent]("GET", "/")
	e2 := e1.WithErrors(ErrorHandler{StatusCode: 404})
	// e1 should not be affected.
	if len(e1.errors) != 0 {
		t.Error("original endpoint should not have errors")
	}
	if len(e2.errors) != 1 {
		t.Error("new endpoint should have 1 error handler")
	}
}
