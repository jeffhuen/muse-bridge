package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/models"
)

func TestHealthz(t *testing.T) {
	handler := New(nil, 10, false)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestModelsListing(t *testing.T) {
	handler := New(nil, 10, false)
	for _, path := range []string{"/v1/models", "/models", "/v1/models/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("path %s: expected 200 OK, got %d", path, rec.Code)
		}

		var list models.OpenAIModelList
		if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
			t.Fatalf("decode models: %v", err)
		}
		if len(list.Data) == 0 {
			t.Fatalf("path %s: expected models list, got empty", path)
		}
	}
}
