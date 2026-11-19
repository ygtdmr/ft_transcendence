package unit_test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
)

func TestWriteDataUsesTheDataEnvelope(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		rec := httptest.NewRecorder()
		httpresponse.WriteData(rec, status, map[string]any{"user_id": "u1"})
		if rec.Code != status || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("status/content-type = %d / %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		if v := httpresponse.Violations(rec.Code, rec.Body.Bytes()); len(v) != 0 {
			t.Errorf("%d: %v", status, v)
		}
	}
	rec := httptest.NewRecorder()
	httpresponse.WriteData(rec, 200, nil)
	if strings.TrimSpace(rec.Body.String()) != `{"data":null}` {
		t.Errorf("nil data body = %q", rec.Body.String())
	}
}

func TestWriteDataRefusesBadStatusAndUnencodableValues(t *testing.T) {
	for name, call := range map[string]func(http.ResponseWriter){
		"204 with data":  func(w http.ResponseWriter) { httpresponse.WriteData(w, 204, "x") },
		"error status":   func(w http.ResponseWriter) { httpresponse.WriteData(w, 400, "x") },
		"unencodable":    func(w http.ResponseWriter) { httpresponse.WriteData(w, 200, math.NaN()) },
		"channel inside": func(w http.ResponseWriter) { httpresponse.WriteData(w, 200, map[string]any{"c": make(chan int)}) },
	} {
		rec := httptest.NewRecorder()
		call(rec)
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		if rec.Code != 500 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != "INTERNAL_ERROR" {
			t.Errorf("%s: got %d %s, want the generic internal error", name, rec.Code, rec.Body.String())
		}
	}
}

func TestWriteNoContentHasNoBodyOrContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	httpresponse.WriteNoContent(rec)
	if rec.Code != 204 || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Errorf("got %d body=%q content-type=%q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	if v := httpresponse.Violations(204, nil); len(v) != 0 {
		t.Errorf("%v", v)
	}
}

func TestTimeMarshalsAsUTC(t *testing.T) {
	zone := time.FixedZone("EET", 2*3600)
	in := httpresponse.Time(time.Date(2026, 11, 5, 19, 4, 5, 123_000_000, zone))
	out, err := json.Marshal(struct {
		CreatedAt httpresponse.Time `json:"created_at"`
	}{in})
	if err != nil || string(out) != `{"created_at":"2026-11-05T17:04:05.123Z"}` {
		t.Fatalf("got %s (%v)", out, err)
	}
	var back struct {
		CreatedAt httpresponse.Time `json:"created_at"`
	}
	if err := json.Unmarshal(out, &back); err != nil || !time.Time(back.CreatedAt).Equal(time.Time(in)) {
		t.Errorf("round trip failed: %v %v", back, err)
	}
}

type codeWriter struct {
	*httptest.ResponseRecorder
	code string
}

func (w *codeWriter) RecordErrorCode(code string) { w.code = code }

func TestWriteErrorReportsItsCodeToRecorders(t *testing.T) {
	w := &codeWriter{ResponseRecorder: httptest.NewRecorder()}
	httpresponse.WriteError(w, &httpresponse.Error{Status: 400, Code: "VALIDATION_ERROR", Message: "bad"})
	if w.code != "VALIDATION_ERROR" || w.Code != 400 {
		t.Errorf("code=%q status=%d", w.code, w.Code)
	}
	// A malformed Error is replaced, and the recorder sees the replacement.
	w = &codeWriter{ResponseRecorder: httptest.NewRecorder()}
	httpresponse.WriteError(w, &httpresponse.Error{Status: 200, Code: "", Message: ""})
	if w.code != "INTERNAL_ERROR" || w.Code != 500 {
		t.Errorf("code=%q status=%d", w.code, w.Code)
	}
	// Plain writers still work.
	plain := httptest.NewRecorder()
	httpresponse.WriteError(plain, httpresponse.InternalError())
	if plain.Code != 500 {
		t.Errorf("plain writer status = %d", plain.Code)
	}
}

func TestViolationsCatchConventionBreaks(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string // substring of one violation; "" means conforming
	}{
		{"valid success", 200, `{"data":{"user_id":"u1","created_at":"2026-11-05T17:04:05Z"}}`, ""},
		{"valid list", 200, `{"data":[{"room_id":"r1"}]}`, ""},
		{"valid error", 400, `{"error":{"code":"VALIDATION_ERROR","message":"bad","details":{}}}`, ""},
		{"camelCase key", 200, `{"data":{"userId":"u1"}}`, "not snake_case"},
		{"nested camelCase", 200, `{"data":{"a":[{"roomId":1}]}}`, "data.a[0].roomId"},
		{"non-UTC timestamp", 200, `{"data":{"created_at":"2026-11-05T19:04:05+02:00"}}`, "UTC"},
		{"naive timestamp", 200, `{"data":{"created_at":"2026-11-05T19:04:05"}}`, "UTC"},
		{"bare payload", 200, `{"user_id":"u1"}`, `exactly one key, "data"`},
		{"extra top key", 200, `{"data":{},"meta":{}}`, `exactly one key, "data"`},
		{"lowercase code", 400, `{"error":{"code":"bad_thing","message":"m","details":{}}}`, "UPPER_SNAKE_CASE"},
		{"missing details", 400, `{"error":{"code":"BAD","message":"m"}}`, "details must be an object"},
		{"empty message", 400, `{"error":{"code":"BAD","message":"","details":{}}}`, "message"},
		{"error wrong envelope", 500, `{"message":"boom"}`, `"error"`},
		{"not json", 200, `<html>`, "not a JSON object"},
		{"204 with body", 204, `{"data":{}}`, "empty body"},
	}
	for _, tc := range cases {
		got := httpresponse.Violations(tc.status, []byte(tc.body))
		if tc.want == "" && len(got) != 0 {
			t.Errorf("%s: unexpected violations %v", tc.name, got)
		}
		if tc.want != "" && !strings.Contains(strings.Join(got, "|"), tc.want) {
			t.Errorf("%s: violations %v do not mention %q", tc.name, got, tc.want)
		}
	}
}
