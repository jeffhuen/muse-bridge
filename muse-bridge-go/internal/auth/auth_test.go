package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMintJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"denied"}`))
	}))
	t.Cleanup(srv.Close)
	old := MintURL
	MintURL = srv.URL
	t.Cleanup(func() { MintURL = old })

	_, err := MintAPIKey(srv.Client(), "identity")
	if err == nil || !strings.Contains(err.Error(), "mint HTTP 401") ||
		!strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want structured detail", err)
	}
}

func TestMintNonJSONErrorKeepsSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		w.Write([]byte(`<html>ingress exploded</html>`))
	}))
	t.Cleanup(srv.Close)
	old := MintURL
	MintURL = srv.URL
	t.Cleanup(func() { MintURL = old })

	_, err := MintAPIKey(srv.Client(), "identity")
	if err == nil || !strings.Contains(err.Error(), "ingress exploded") {
		t.Fatalf("err = %v, want raw snippet", err)
	}
}
