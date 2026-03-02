package openapigo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	Do(context.Background(), client, ep, NoRequest{})

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
	Do(context.Background(), client, ep, NoRequest{})
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
	for i := 0; i < 50; i++ {
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
