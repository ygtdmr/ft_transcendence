package unit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/requestid"
)

func TestRequestIDValidation(t *testing.T) {
	valid := []string{"a", "req-123", "A_b.c-9", strings.Repeat("x", requestid.MaxLength)}
	invalid := []string{"", " ", "has space", "semi;colon", "new\nline", "tab\t", "ünï", "bang!", strings.Repeat("x", requestid.MaxLength+1)}
	for _, id := range valid {
		if !requestid.Valid(id) {
			t.Errorf("Valid(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if requestid.Valid(id) {
			t.Errorf("Valid(%q) = true, want false", id)
		}
	}
}

func TestRequestIDSelect(t *testing.T) {
	// Headers are built with Set/Add like a real request; a map literal would skip
	// canonicalization and make Values() blind to the header.
	one := http.Header{}
	one.Set(requestid.Header, "client-id-1")
	if got := requestid.Select(one); got != "client-id-1" {
		t.Errorf("single valid header = %q, want it preserved", got)
	}

	invalid := http.Header{}
	invalid.Set(requestid.Header, "bad id!")
	repeated := http.Header{}
	repeated.Add(requestid.Header, "first-id")
	repeated.Add(requestid.Header, "second-id")
	for name, h := range map[string]http.Header{"missing": {}, "invalid": invalid, "repeated": repeated} {
		got := requestid.Select(h)
		if !requestid.Valid(got) || got == "bad id!" || got == "first-id" || got == "second-id" {
			t.Errorf("%s header: got %q, want a fresh valid ID", name, got)
		}
	}
	if requestid.New() == requestid.New() {
		t.Error("New returned the same ID twice")
	}
}

func TestRequestIDMiddlewareSetsHeaderAndContext(t *testing.T) {
	var seen string
	handler := requestid.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = requestid.FromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(requestid.Header, "trace-7")
	handler.ServeHTTP(rec, req)

	if seen != "trace-7" || rec.Header().Get(requestid.Header) != "trace-7" {
		t.Errorf("context=%q header=%q, want trace-7 in both", seen, rec.Header().Get(requestid.Header))
	}
	if requestid.FromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context()) != "" {
		t.Error("FromContext must be empty when no ID was set")
	}
}
