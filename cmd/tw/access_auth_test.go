package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentCanReadLoginInputsButCannotCompleteHumanAction(t *testing.T) {
	const token = "operator-test-access-token"
	auth := newConsoleAuth(token)
	handler := auth.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, test := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/p/test/api/access", 204},
		{"GET", "/p/test/api/access/ac_x/material", 204},
		{"GET", "/p/test/api/artifacts", 204},
		{"GET", "/p/test/api/operator-requests", 204},
		{"POST", "/p/test/api/intents/in_x/operator", 204},
		{"POST", "/p/test/api/intents/in_x/operator/complete", 404},
		{"POST", "/p/test/api/access", 404},
		{"POST", "/p/test/api/access/ac_x/browser", 404},
		{"POST", "/p/test/api/access/ac_x/lease", 404},
		{"DELETE", "/p/test/api/access/ac_x", 404},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Header.Set("Authorization", "Bearer "+agentAPIToken(token))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%s %s: %d want %d", test.method, test.path, rec.Code, test.want)
		}
	}
	request := httptest.NewRequest("POST", "/p/test/api/intents/in_x/operator/complete", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatal("operator cannot complete human action")
	}
}
