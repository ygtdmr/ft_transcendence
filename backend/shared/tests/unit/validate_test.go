package unit_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/validate"
)

type roomDTO struct {
	Name       string `json:"name"`
	MaxPlayers int    `json:"max_players"`
	Mode       string `json:"mode"`
}

func (d *roomDTO) Validate() error {
	return validate.First(
		validate.Required("name", d.Name),
		validate.MaxLen("name", d.Name, 20),
		validate.NoControlChars("name", d.Name),
		validate.IntBetween("max_players", int64(d.MaxPlayers), 4, 20),
		validate.OneOf("mode", d.Mode, "CLASSIC", "TEAM"),
	)
}

func post(contentType, body string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodPost, "/rooms", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return httptest.NewRecorder(), req
}

func decode(contentType, body string, opts validate.Options) (roomDTO, error) {
	w, r := post(contentType, body)
	return validate.DecodeJSON[roomDTO](w, r, opts)
}

func TestDecodeJSONReturnsValidatedData(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON; charset=UTF-8"} {
		got, err := decode(ct, `{"name":"Blue","max_players":8,"mode":"TEAM"}`, validate.Options{})
		if err != nil || got != (roomDTO{"Blue", 8, "TEAM"}) {
			t.Errorf("%q: got %+v, %v", ct, got, err)
		}
	}
	// Whitespace around the object is fine.
	if _, err := decode("application/json", "  \n{\"name\":\"a\",\"max_players\":4,\"mode\":\"TEAM\"}\n ", validate.Options{}); err != nil {
		t.Errorf("surrounding whitespace rejected: %v", err)
	}
}

func TestDecodeJSONRejectsMalformedAndUnexpectedStructureWith400(t *testing.T) {
	valid := `{"name":"a","max_players":4,"mode":"TEAM"}`
	cases := map[string]struct{ contentType, body string }{
		"no content type":      {"", valid},
		"text/plain":           {"text/plain", valid},
		"form encoded":         {"application/x-www-form-urlencoded", valid},
		"utf-16 charset":       {"application/json; charset=utf-16", valid},
		"extra media param":    {"application/json; boundary=x", valid},
		"empty body":           {"application/json", ""},
		"whitespace body":      {"application/json", "   "},
		"truncated":            {"application/json", `{"name":"a"`},
		"not json":             {"application/json", `name=a`},
		"top-level array":      {"application/json", `[{"name":"a"}]`},
		"top-level string":     {"application/json", `"x"`},
		"top-level null":       {"application/json", `null`},
		"unknown field":        {"application/json", `{"name":"a","max_players":4,"mode":"TEAM","admin":true}`},
		"duplicate key":        {"application/json", `{"name":"a","name":"b","max_players":4,"mode":"TEAM"}`},
		"duplicate other case": {"application/json", `{"name":"a","NAME":"b","max_players":4,"mode":"TEAM"}`},
		"trailing object":      {"application/json", valid + valid},
		"trailing garbage":     {"application/json", valid + ` x`},
		"wrong type":           {"application/json", `{"name":"a","max_players":"four","mode":"TEAM"}`},
		"number as string":     {"application/json", `{"name":"a","max_players":"4","mode":"TEAM"}`},
	}
	for name, tc := range cases {
		_, err := decode(tc.contentType, tc.body, validate.Options{})
		resp, ok := validate.Response(err)
		if !ok || resp.Status != http.StatusBadRequest || resp.Code != "VALIDATION_ERROR" || len(resp.Details) != 0 {
			t.Errorf("%s: got %v / %+v, want 400 VALIDATION_ERROR with no details", name, err, resp)
		}
	}
}

func TestDecodeJSONRejectsDuplicateKeysInNestedObjects(t *testing.T) {
	for name, body := range map[string]string{
		"nested":      `{"inner":{"a":1,"a":2}}`,
		"in an array": `{"list":[{"b":1,"b":2}]}`,
	} {
		w, r := post("application/json", body)
		if _, err := validate.DecodeJSON[nestedDTO](w, r, validate.Options{}); err == nil {
			t.Errorf("%s: duplicate key accepted", name)
		}
	}
}

type nestedDTO struct {
	Inner struct {
		A int `json:"a"`
	} `json:"inner"`
	List []struct {
		B int `json:"b"`
	} `json:"list"`
}

func (nestedDTO) Validate() error { return nil }

func TestDecodeJSONEnforcesTheSizeLimit(t *testing.T) {
	big := `{"name":"` + strings.Repeat("x", 5000) + `","max_players":4,"mode":"TEAM"}`
	if _, err := decode("application/json", big, validate.Options{MaxBytes: 1024}); err == nil {
		t.Fatal("a body over the limit must be rejected")
	} else if resp, _ := validate.Response(err); resp == nil || resp.Status != 400 {
		t.Errorf("oversized body: %v", err)
	}
	if _, err := decode("application/json", big, validate.Options{}); err != nil {
		if _, isField := err.(*validate.FieldError); !isField {
			t.Errorf("under the default 64KiB limit the body must be read and validated, got %v", err)
		}
	}
	// The default limit applies when none is given.
	huge := `{"name":"` + strings.Repeat("x", validate.DefaultMaxBytes) + `"}`
	if _, err := decode("application/json", huge, validate.Options{}); err == nil {
		t.Error("the default limit was not enforced")
	}
}

func TestDecodeJSONFieldConstraintsAre422WithTheFirstFailingField(t *testing.T) {
	cases := []struct{ body, field, message string }{
		{`{"name":"","max_players":4,"mode":"TEAM"}`, "name", "name is required."},
		{`{"name":"` + strings.Repeat("a", 21) + `","max_players":4,"mode":"TEAM"}`, "name", "name must be at most 20 characters."},
		{`{"name":"bad\u0000name","max_players":4,"mode":"TEAM"}`, "name", "name must not contain control characters."},
		{`{"name":"a","max_players":3,"mode":"TEAM"}`, "max_players", "max_players must be an integer in 4..20."},
		{`{"name":"a","max_players":4,"mode":"team"}`, "mode", "mode must be one of: CLASSIC, TEAM."},
		// Several problems: the first check in Validate wins, deterministically.
		{`{"name":"","max_players":99,"mode":"x"}`, "name", "name is required."},
	}
	for _, tc := range cases {
		got, err := decode("application/json", tc.body, validate.Options{})
		resp, ok := validate.Response(err)
		if !ok || resp.Status != http.StatusUnprocessableEntity || resp.Code != "VALIDATION_ERROR" ||
			resp.Message != tc.message || resp.Details["field"] != tc.field {
			t.Errorf("%s: got %+v", tc.body, resp)
		}
		if got != (roomDTO{}) {
			t.Errorf("invalid input must not leak a partially filled value: %+v", got)
		}
	}
}

func TestErrorsNeverEchoRequestData(t *testing.T) {
	const marker = "attacker-controlled-<script>-9f3"
	bodies := []string{
		`{"name":"` + marker + strings.Repeat("a", 30) + `","max_players":4,"mode":"TEAM"}`,
		`{"name":"a","max_players":4,"mode":"` + marker + `"}`,
		`{"` + marker + `":1}`,
		`{"name":"` + marker + `","max_players":"` + marker + `"}`,
	}
	for _, body := range bodies {
		_, err := decode("application/json", body, validate.Options{})
		resp, _ := validate.Response(err)
		if resp == nil {
			t.Fatalf("no validation error for %s", body)
		}
		w := httptest.NewRecorder()
		httpresponse.WriteError(w, resp)
		if strings.Contains(w.Body.String(), marker) || strings.Contains(err.Error(), marker) {
			t.Errorf("request data was reflected: %s", w.Body.String())
		}
	}
}

func TestResponseIgnoresForeignErrors(t *testing.T) {
	if resp, ok := validate.Response(errors.New("sql: connection refused")); ok || resp != nil {
		t.Error("only this package's errors are public")
	}
	wrapped := errorsJoin(&validate.FieldError{Field: "f", Message: "f is bad."})
	if resp, ok := validate.Response(wrapped); !ok || resp.Details["field"] != "f" {
		t.Error("wrapped validation errors must still be recognised")
	}
	if validate.First(nil, nil) != nil {
		t.Error("First of nothing must be nil (not a typed nil)")
	}
}

func errorsJoin(err error) error { return errors.Join(errors.New("context"), err) }

func TestMatchRuleNamesTheDocumentedPattern(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9]{6}$`)
	if validate.Match("room_code", "AB12cd", re) != nil {
		t.Error("valid code rejected")
	}
	err := validate.Match("room_code", "nope", re)
	if err == nil || err.Error() != "room_code must match ^[A-Za-z0-9]{6}$." {
		t.Errorf("message = %v, want the wording the Bruno contract uses", err)
	}
}

func TestLengthRulesCountCharactersNotBytes(t *testing.T) {
	if validate.MaxLen("name", "üüüüü", 5) != nil || validate.MaxLen("name", "üüüüüü", 5) == nil {
		t.Error("MaxLen must count runes")
	}
	if validate.MinLen("name", "ü", 2) == nil || validate.MinLen("name", "üü", 2) != nil {
		t.Error("MinLen must count runes")
	}
}

func TestPositiveIntAcceptsOnlyTheCanonicalForm(t *testing.T) {
	for raw, want := range map[string]int64{"1": 1, "42": 42, "9223372036854775807": 9223372036854775807} {
		if n, err := validate.ParsePositiveInt("user_id", raw); err != nil || n != want {
			t.Errorf("%q = %d, %v", raw, n, err)
		}
	}
	for _, raw := range []string{"", "0", "-1", "+1", "01", "00", " 1", "1 ", "1.0", "1e3", "0x10", "zero", "abc", "９", "9223372036854775808", "1\n", "1%00"} {
		_, err := validate.ParsePositiveInt("user_id", raw)
		resp, ok := validate.Response(err)
		if !ok || resp.Status != 422 || resp.Message != "user_id must be a positive integer." || resp.Details["field"] != "user_id" {
			t.Errorf("%q accepted or wrongly reported: %v", raw, err)
		}
	}
}

func TestParseIntBounds(t *testing.T) {
	if n, err := validate.ParseInt("limit", "50", 1, 100); err != nil || n != 50 {
		t.Errorf("%d %v", n, err)
	}
	for _, raw := range []string{"0", "101", "-5", "1.5", "05", "", "x"} {
		if _, err := validate.ParseInt("limit", raw, 1, 100); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	if n, err := validate.ParseInt("delta", "-3", -10, 10); err != nil || n != -3 {
		t.Errorf("negative in range: %d %v", n, err)
	}
}

func queryFor(raw string) *validate.Query {
	return validate.NewQuery(&http.Request{URL: &url.URL{RawQuery: raw}})
}

func TestQueryReadsTypedParametersWithDefaults(t *testing.T) {
	q := queryFor("limit=25&q=ann&sort=NAME&online=true")
	limit := q.Int("limit", 20, 1, 100)
	offset := q.Int("offset", 0, 0, 10000)
	search := q.String("q", "", 32)
	sort := q.Enum("sort", "ID", "ID", "NAME")
	online := q.Bool("online", false)
	other := q.Bool("other", true)
	if q.Err() != nil || limit != 25 || offset != 0 || search != "ann" || sort != "NAME" || !online || !other {
		t.Errorf("got %d %d %q %q %v %v err=%v", limit, offset, search, sort, online, other, q.Err())
	}
}

func TestQueryRejectsBadAmbiguousAndUnknownParameters(t *testing.T) {
	cases := map[string]struct {
		raw   string
		read  func(*validate.Query)
		field string
	}{
		"limit too big":      {"limit=500", func(q *validate.Query) { q.Int("limit", 20, 1, 100) }, "limit"},
		"limit not a number": {"limit=ten", func(q *validate.Query) { q.Int("limit", 20, 1, 100) }, "limit"},
		"limit empty":        {"limit=", func(q *validate.Query) { q.Int("limit", 20, 1, 100) }, "limit"},
		"limit repeated":     {"limit=1&limit=500", func(q *validate.Query) { q.Int("limit", 20, 1, 100) }, "limit"},
		"enum":               {"sort=DROP", func(q *validate.Query) { q.Enum("sort", "ID", "ID", "NAME") }, "sort"},
		"bool":               {"online=1", func(q *validate.Query) { q.Bool("online", false) }, "online"},
		"too long":           {"q=" + strings.Repeat("a", 40), func(q *validate.Query) { q.String("q", "", 32) }, "q"},
		"control char":       {"q=a%0Ab", func(q *validate.Query) { q.String("q", "", 32) }, "q"},
		"unknown":            {"limit=5&debug=1", func(q *validate.Query) { q.Only("limit") }, "query"},
		"malformed":          {"limit=%zz", func(q *validate.Query) { q.Int("limit", 20, 1, 100) }, "query"},
	}
	for name, tc := range cases {
		q := queryFor(tc.raw)
		tc.read(q)
		resp, ok := validate.Response(q.Err())
		if !ok || resp.Status != 422 || resp.Details["field"] != tc.field {
			t.Errorf("%s (%q): got %v", name, tc.raw, q.Err())
		}
		if strings.Contains(resp.Message, "debug") {
			t.Errorf("%s: the unknown parameter name was echoed: %s", name, resp.Message)
		}
	}
}

func TestQueryKeepsTheFirstError(t *testing.T) {
	q := queryFor("limit=zzz&offset=-1")
	q.Int("limit", 20, 1, 100)
	q.Int("offset", 0, 0, 100)
	if resp, _ := validate.Response(q.Err()); resp == nil || resp.Details["field"] != "limit" {
		t.Errorf("the first failing parameter must be reported, got %v", q.Err())
	}
}
