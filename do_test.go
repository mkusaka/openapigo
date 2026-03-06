package openapigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Pet struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type GetPetRequest struct {
	PetID string `path:"petId"`
}

type ListPetsRequest struct {
	Limit  *int   `query:"limit"`
	Status string `query:"status"`
}

type CreatePetRequest struct {
	Body Pet `body:"application/json"`
}

func TestDo_GetWithPathParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pets/42" {
			t.Errorf("path = %q, want /pets/42", r.URL.Path)
		}
		if r.Method != "GET" {
			t.Errorf("method = %q, want GET", r.Method)
		}
		json.NewEncoder(w).Encode(Pet{ID: 42, Name: "Fido"})
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[GetPetRequest, Pet]("GET", "/pets/{petId}")

	resp, err := Do(context.Background(), client, ep, GetPetRequest{PetID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != 42 || resp.Name != "Fido" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestDo_GetWithQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("status") != "active" {
			t.Errorf("status = %q", r.URL.Query().Get("status"))
		}
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("limit = %q", r.URL.Query().Get("limit"))
		}
		json.NewEncoder(w).Encode([]Pet{{ID: 1, Name: "Fido"}})
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[ListPetsRequest, []Pet]("GET", "/pets")

	limit := 10
	resp, err := Do(context.Background(), client, ep, ListPetsRequest{
		Limit:  &limit,
		Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*resp) != 1 {
		t.Errorf("len = %d", len(*resp))
	}
}

func TestDo_PostWithJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var pet Pet
		json.Unmarshal(body, &pet)
		if pet.Name != "Rex" {
			t.Errorf("name = %q", pet.Name)
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(Pet{ID: 1, Name: "Rex"})
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[CreatePetRequest, Pet]("POST", "/pets").WithSuccessCodes(201)

	resp, err := Do(context.Background(), client, ep, CreatePetRequest{
		Body: Pet{Name: "Rex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Name != "Rex" {
		t.Errorf("name = %q", resp.Name)
	}
}

func TestDo_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"not found"}`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/pets/999")

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != 404 {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestDo_TypedErrorHandler(t *testing.T) {
	type NotFoundError struct {
		Message string `json:"message"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(NotFoundError{Message: "pet not found"})
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/pets/999").WithErrors(
		ErrorHandler{
			StatusCode: 404,
			Parse: func(code int, header http.Header, body []byte) error {
				var e NotFoundError
				json.Unmarshal(body, &e)
				return errors.New(e.Message)
			},
		},
	)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "pet not found" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestDo_Middleware(t *testing.T) {
	var called []string
	mw1 := MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		called = append(called, "mw1-before")
		resp, err := next(req)
		called = append(called, "mw1-after")
		return resp, err
	})
	mw2 := MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		called = append(called, "mw2-before")
		resp, err := next(req)
		called = append(called, "mw2-after")
		return resp, err
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithMiddleware(mw1, mw2))
	ep := NewEndpoint[NoRequest, struct{}]("GET", "/")
	if _, err := Do(context.Background(), client, ep, NoRequest{}); err != nil {
		t.Fatal(err)
	}

	want := []string{"mw1-before", "mw2-before", "mw2-after", "mw1-after"}
	if len(called) != len(want) {
		t.Fatalf("called = %v, want %v", called, want)
	}
	for i := range want {
		if called[i] != want[i] {
			t.Errorf("called[%d] = %q, want %q", i, called[i], want[i])
		}
	}
}

func TestDo_DefaultHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "val" {
			t.Errorf("X-Custom = %q", r.Header.Get("X-Custom"))
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL), WithDefaultHeader("X-Custom", "val"))
	ep := NewEndpoint[NoRequest, struct{}]("GET", "/")
	if _, err := Do(context.Background(), client, ep, NoRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, NoContent]("DELETE", "/pets/1").WithSuccessCodes(204)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDo_ConcurrentRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Pet{ID: 1, Name: "Fido"})
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/pets/1")

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Do(context.Background(), client, ep, NoRequest{})
			if err != nil {
				t.Errorf("concurrent Do error: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestDoSimple(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Pet{ID: 1, Name: "Fido"})
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/pets/1")

	resp, err := DoSimple(context.Background(), client, ep)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Name != "Fido" {
		t.Errorf("name = %q", resp.Name)
	}
}

func TestDo_ErrorRangeMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"detail":"forbidden"}`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/x").WithErrors(
		ErrorHandler{
			StatusCode: -4, // matches any 4XX
			Parse: func(code int, header http.Header, body []byte) error {
				return errors.New("4xx range handler")
			},
		},
	)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "4xx range handler" {
		t.Errorf("error = %q, want %q", err.Error(), "4xx range handler")
	}
}

func TestDo_ErrorDefaultHandler(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`internal`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/x").WithErrors(
		ErrorHandler{
			StatusCode: 0, // default handler
			Parse: func(code int, header http.Header, body []byte) error {
				return fmt.Errorf("default handler: %d", code)
			},
		},
	)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "default handler: 500" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestDo_ErrorPriority(t *testing.T) {
	// Registers exact (404), range (-4), and default (0) handlers.
	// A 404 should hit the exact handler, not range or default.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/x").WithErrors(
		ErrorHandler{
			StatusCode: -4,
			Parse: func(code int, header http.Header, body []byte) error {
				return errors.New("range")
			},
		},
		ErrorHandler{
			StatusCode: 0,
			Parse: func(code int, header http.Header, body []byte) error {
				return errors.New("default")
			},
		},
		ErrorHandler{
			StatusCode: 404,
			Parse: func(code int, header http.Header, body []byte) error {
				return errors.New("exact")
			},
		},
	)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "exact" {
		t.Errorf("error = %q, want %q", err.Error(), "exact")
	}
}

func TestDo_ErrorRangeFallsToDefault(t *testing.T) {
	// 500 error with only a -4 (4XX) range handler and a default handler.
	// Should skip -4 and use default.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/x").WithErrors(
		ErrorHandler{
			StatusCode: -4,
			Parse: func(code int, header http.Header, body []byte) error {
				return errors.New("4xx range")
			},
		},
		ErrorHandler{
			StatusCode: 0,
			Parse: func(code int, header http.Header, body []byte) error {
				return errors.New("default")
			},
		},
	)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "default" {
		t.Errorf("error = %q, want %q", err.Error(), "default")
	}
}

func TestDo_Error5xxRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/x").WithErrors(
		ErrorHandler{
			StatusCode: -5,
			Parse: func(code int, header http.Header, body []byte) error {
				return fmt.Errorf("5xx: %d", code)
			},
		},
	)

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "5xx: 503" {
		t.Errorf("error = %q", err.Error())
	}
}

func TestDo_ErrorNoHandlerFallback(t *testing.T) {
	// No error handlers registered — should fall back to APIError.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		w.Write([]byte(`bad gateway`))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, Pet]("GET", "/x")

	_, err := Do(context.Background(), client, ep, NoRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 502 {
		t.Errorf("status = %d, want 502", apiErr.StatusCode)
	}
	if string(apiErr.Body) != "bad gateway" {
		t.Errorf("body = %q", apiErr.Body)
	}
}

func TestDo_QueryExplodeFalse(t *testing.T) {
	type Req struct {
		Tags []string `query:"tags,noexplode"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("tags")
		if got != "a,b,c" {
			t.Errorf("tags = %q, want %q", got, "a,b,c")
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[Req, struct{}]("GET", "/items")
	if _, err := Do(context.Background(), client, ep, Req{Tags: []string{"a", "b", "c"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_QueryExplodeTrue(t *testing.T) {
	type Req struct {
		Tags []string `query:"tags"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query()["tags"]
		if len(got) != 2 || got[0] != "x" || got[1] != "y" {
			t.Errorf("tags = %v, want [x y]", got)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[Req, struct{}]("GET", "/items")
	if _, err := Do(context.Background(), client, ep, Req{Tags: []string{"x", "y"}}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_CookieParam(t *testing.T) {
	type CookieReq struct {
		SessionID string `cookie:"session_id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session_id")
		if err != nil {
			t.Fatalf("missing cookie: %v", err)
		}
		if c.Value != "abc123" {
			t.Errorf("cookie value = %q, want %q", c.Value, "abc123")
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[CookieReq, struct{}]("GET", "/session")
	if _, err := Do(context.Background(), client, ep, CookieReq{SessionID: "abc123"}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_MultipartBody(t *testing.T) {
	type UploadBody struct {
		Name    string `json:"name"`
		Content []byte `json:"content"`
	}
	type UploadReq struct {
		Body UploadBody `body:"multipart/form-data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type = %q, want multipart/form-data", ct)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if r.FormValue("name") != "test.txt" {
			t.Errorf("name = %q", r.FormValue("name"))
		}
		file, _, err := r.FormFile("content")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "hello" {
			t.Errorf("content = %q", data)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[UploadReq, struct{}]("POST", "/upload").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, UploadReq{
		Body: UploadBody{Name: "test.txt", Content: []byte("hello")},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_BinaryBody(t *testing.T) {
	type BinaryReq struct {
		Body []byte `body:"application/octet-stream"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("Content-Type = %q", ct)
		}
		data, _ := io.ReadAll(r.Body)
		if string(data) != "binary data" {
			t.Errorf("body = %q", data)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[BinaryReq, struct{}]("POST", "/upload")
	if _, err := Do(context.Background(), client, ep, BinaryReq{Body: []byte("binary data")}); err != nil {
		t.Fatal(err)
	}
}

func TestDoRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("raw body"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[NoRequest, any]("GET", "/raw")

	resp, err := DoRaw(context.Background(), client, ep, NoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "raw body" {
		t.Errorf("body = %q", body)
	}
}

func TestDo_FormTagPriority(t *testing.T) {
	// form tag should take precedence over json tag for form/multipart bodies.
	type FormBody struct {
		UserName string `form:"user_name" json:"username"`
		Age      int    `form:"user_age" json:"age"`
	}
	type FormReq struct {
		Body FormBody `body:"application/x-www-form-urlencoded"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.FormValue("user_name"); got != "alice" {
			t.Errorf("user_name = %q, want %q", got, "alice")
		}
		if got := r.FormValue("user_age"); got != "30" {
			t.Errorf("user_age = %q, want %q", got, "30")
		}
		// json tag names should NOT be used.
		if got := r.FormValue("username"); got != "" {
			t.Errorf("username (json tag) unexpectedly present: %q", got)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[FormReq, struct{}]("POST", "/form").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, FormReq{
		Body: FormBody{UserName: "alice", Age: 30},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_MultipartFormTag(t *testing.T) {
	// form tag should take precedence over json tag for multipart bodies.
	type UploadBody struct {
		FileName string `form:"file_name" json:"filename"`
		Data     []byte `form:"file_data" json:"data"`
	}
	type UploadReq struct {
		Body UploadBody `body:"multipart/form-data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("file_name"); got != "test.txt" {
			t.Errorf("file_name = %q, want %q", got, "test.txt")
		}
		file, _, err := r.FormFile("file_data")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "content" {
			t.Errorf("file_data = %q, want %q", data, "content")
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[UploadReq, struct{}]("POST", "/upload").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, UploadReq{
		Body: UploadBody{FileName: "test.txt", Data: []byte("content")},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_UnionBody_JSON(t *testing.T) {
	type Pet struct {
		Name string `json:"name"`
	}
	type UnionBody struct {
		JSON      *Pet `body:"application/json"`
		Multipart *Pet `body:"multipart/form-data"`
	}
	type UnionReq struct {
		Body UnionBody `body:"*"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var pet Pet
		if err := json.NewDecoder(r.Body).Decode(&pet); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if pet.Name != "Fido" {
			t.Errorf("name = %q, want Fido", pet.Name)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[UnionReq, struct{}]("POST", "/pets").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, UnionReq{
		Body: UnionBody{JSON: &Pet{Name: "Fido"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_UnionBody_Multipart(t *testing.T) {
	type UploadForm struct {
		Name string `json:"name"`
	}
	type UnionBody struct {
		JSON      *UploadForm `body:"application/json"`
		Multipart *UploadForm `body:"multipart/form-data"`
	}
	type UnionReq struct {
		Body UnionBody `body:"*"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type = %q, want multipart/form-data", ct)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("name"); got != "pic.jpg" {
			t.Errorf("name = %q, want pic.jpg", got)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[UnionReq, struct{}]("POST", "/upload").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, UnionReq{
		Body: UnionBody{Multipart: &UploadForm{Name: "pic.jpg"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_UnionBody_Ambiguous(t *testing.T) {
	type Pet struct {
		Name string `json:"name"`
	}
	type UnionBody struct {
		JSON      *Pet `body:"application/json"`
		Multipart *Pet `body:"multipart/form-data"`
	}
	type UnionReq struct {
		Body UnionBody `body:"*"`
	}
	client := NewClient(WithBaseURL("http://localhost"))
	ep := NewEndpoint[UnionReq, struct{}]("POST", "/pets").WithSuccessCodes(200)
	// Both fields set → should fail.
	_, err := Do(context.Background(), client, ep, UnionReq{
		Body: UnionBody{
			JSON:      &Pet{Name: "Fido"},
			Multipart: &Pet{Name: "Fido"},
		},
	})
	if !errors.Is(err, ErrAmbiguousMediaType) {
		t.Errorf("err = %v, want ErrAmbiguousMediaType", err)
	}
}

func TestDo_UnionBody_Empty(t *testing.T) {
	type Pet struct {
		Name string `json:"name"`
	}
	type UnionBody struct {
		JSON      *Pet `body:"application/json"`
		Multipart *Pet `body:"multipart/form-data"`
	}
	type UnionReq struct {
		Body UnionBody `body:"*"`
	}
	client := NewClient(WithBaseURL("http://localhost"))
	ep := NewEndpoint[UnionReq, struct{}]("POST", "/pets").WithSuccessCodes(200)
	// Neither field set → ErrNoMediaType.
	_, err := Do(context.Background(), client, ep, UnionReq{
		Body: UnionBody{},
	})
	if !errors.Is(err, ErrNoMediaType) {
		t.Errorf("err = %v, want ErrNoMediaType", err)
	}
}

func TestDo_MultipartWithFile(t *testing.T) {
	type UploadBody struct {
		Photo       File   `json:"photo"`
		Description string `json:"description"`
	}
	type UploadReq struct {
		Body UploadBody `body:"multipart/form-data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type = %q, want multipart/form-data", ct)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if got := r.FormValue("description"); got != "a cute cat" {
			t.Errorf("description = %q, want %q", got, "a cute cat")
		}
		file, header, err := r.FormFile("photo")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		if header.Filename != "cat.jpg" {
			t.Errorf("filename = %q, want cat.jpg", header.Filename)
		}
		data, _ := io.ReadAll(file)
		if string(data) != "jpeg-data" {
			t.Errorf("file content = %q, want jpeg-data", data)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[UploadReq, struct{}]("POST", "/upload").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, UploadReq{
		Body: UploadBody{
			Photo:       FileFromBytes("cat.jpg", []byte("jpeg-data")),
			Description: "a cute cat",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_TextPlain(t *testing.T) {
	type TextReq struct {
		Body string `body:"text/plain"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", ct)
		}
		data, _ := io.ReadAll(r.Body)
		if string(data) != "hello world" {
			t.Errorf("body = %q, want %q", data, "hello world")
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[TextReq, struct{}]("POST", "/text").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, TextReq{
		Body: "hello world",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDo_UnionBody_TypedNilReader(t *testing.T) {
	// A typed-nil io.Reader (e.g., (*bytes.Buffer)(nil)) should NOT be treated
	// as a non-nil field in a union body struct. It should be treated as nil,
	// and since no field is set, ErrNoMediaType should be returned (not a panic).
	type UnionBody struct {
		Raw  io.Reader `body:"application/octet-stream"`
		Text *string   `body:"text/plain"`
	}
	type UnionReq struct {
		Body UnionBody `body:"*"`
	}

	var buf *bytes.Buffer // typed-nil
	client := NewClient(WithBaseURL("http://localhost"))
	ep := NewEndpoint[UnionReq, struct{}]("POST", "/u").WithSuccessCodes(200)
	// Should not panic — typed-nil should be treated as nil → ErrNoMediaType.
	_, err := Do(context.Background(), client, ep, UnionReq{
		Body: UnionBody{Raw: buf},
	})
	if !errors.Is(err, ErrNoMediaType) {
		t.Errorf("err = %v, want ErrNoMediaType", err)
	}
}

func TestDo_FileReaderClosed(t *testing.T) {
	// File.Reader should be closed after multipart upload if it implements io.ReadCloser.
	type closeTracker struct {
		io.Reader
		closed bool
	}
	type UploadBody struct {
		Photo File `json:"photo"`
	}
	type UploadReq struct {
		Body UploadBody `body:"multipart/form-data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	tracker := &closeTracker{Reader: strings.NewReader("data")}
	// Wrap as io.ReadCloser.
	rc := struct {
		io.Reader
		io.Closer
	}{
		Reader: tracker,
		Closer: closerFunc(func() error { tracker.closed = true; return nil }),
	}
	client := NewClient(WithBaseURL(srv.URL))
	ep := NewEndpoint[UploadReq, struct{}]("POST", "/upload").WithSuccessCodes(200)
	if _, err := Do(context.Background(), client, ep, UploadReq{
		Body: UploadBody{Photo: File{Name: "x.txt", Reader: rc}},
	}); err != nil {
		t.Fatal(err)
	}
	if !tracker.closed {
		t.Error("File.Reader was not closed after upload")
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
