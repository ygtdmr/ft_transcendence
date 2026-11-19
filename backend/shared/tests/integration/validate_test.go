package integration_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/melmut-42/ft_transcendence/backend/shared/httpresponse"
	"github.com/melmut-42/ft_transcendence/backend/shared/validate"
)

// createRoomRequest is the wire DTO; roomInput is what the service layer takes.
type createRoomRequest struct {
	Name       string `json:"name"`
	MaxPlayers int    `json:"max_players"`
}

func (r *createRoomRequest) Validate() error {
	return validate.First(
		validate.Required("name", r.Name),
		validate.MaxLen("name", r.Name, 20),
		validate.IntBetween("max_players", int64(r.MaxPlayers), 4, 20),
	)
}

type roomInput struct {
	Name       string
	MaxPlayers int
}

func (r createRoomRequest) toInput() roomInput { return roomInput{r.Name, r.MaxPlayers} }

// spyService counts how often domain code runs: invalid input must leave it at 0.
type spyService struct {
	calls atomic.Int32
	last  atomic.Value
}

func (s *spyService) create(in roomInput) { s.calls.Add(1); s.last.Store(in) }

func validationServer(t *testing.T) (*httptest.Server, *spyService) {
	t.Helper()
	spy := &spyService{}
	fail := func(w http.ResponseWriter, err error) {
		if resp, ok := validate.Response(err); ok {
			httpresponse.WriteError(w, resp)
			return
		}
		httpresponse.WriteError(w, httpresponse.InternalError())
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/rooms", func(c *gin.Context) {
		req, err := validate.DecodeJSON[createRoomRequest](c.Writer, c.Request, validate.Options{MaxBytes: 2048})
		if err != nil {
			fail(c.Writer, err)
			return
		}
		spy.create(req.toInput())
		httpresponse.WriteData(c.Writer, http.StatusCreated, map[string]any{"name": req.Name, "max_players": req.MaxPlayers})
	})
	router.GET("/api/v1/users/:user_id", func(c *gin.Context) {
		id, err := validate.ParsePositiveInt("user_id", c.Param("user_id"))
		if err != nil {
			fail(c.Writer, err)
			return
		}
		spy.create(roomInput{Name: "user", MaxPlayers: int(id)})
		httpresponse.WriteData(c.Writer, http.StatusOK, map[string]any{"user_id": id})
	})
	router.GET("/api/v1/friends", func(c *gin.Context) {
		q := validate.NewQuery(c.Request).Only("limit", "offset")
		limit, offset := q.Int("limit", 20, 1, 100), q.Int("offset", 0, 0, 10000)
		if err := q.Err(); err != nil {
			fail(c.Writer, err)
			return
		}
		spy.create(roomInput{Name: "friends", MaxPlayers: limit + offset})
		httpresponse.WriteData(c.Writer, http.StatusOK, map[string]any{"limit": limit, "offset": offset})
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, spy
}

// try sends a request and returns the response, or the transport error. A server
// may legitimately refuse an oversized upload by closing the connection before
// the client has finished writing, so callers decide whether an error is fine.
func try(method, url, contentType, body string) (*http.Response, string, error) {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b), nil
}

func send(t *testing.T, method, url, contentType, body string) (*http.Response, string) {
	t.Helper()
	resp, b, err := try(method, url, contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// TestInvalidPathMatchesTheBrunoExampleExactly compares against the literal
// response in the contract (Users & Profile / get-public-profile, "Invalid path ID").
func TestInvalidPathMatchesTheBrunoExampleExactly(t *testing.T) {
	srv, spy := validationServer(t)
	resp, body := send(t, "GET", srv.URL+"/api/v1/users/zero", "", "")

	const bruno = `{"error":{"code":"VALIDATION_ERROR","message":"user_id must be a positive integer.","details":{"field":"user_id"}}}`
	if resp.StatusCode != 422 || strings.TrimSpace(body) != bruno {
		t.Fatalf("got %d %s\nwant 422 %s", resp.StatusCode, body, bruno)
	}
	if spy.calls.Load() != 0 {
		t.Error("the service layer ran for an invalid path value")
	}
}

func TestInvalidInputNeverReachesTheServiceLayer(t *testing.T) {
	srv, spy := validationServer(t)
	valid := `{"name":"Blue","max_players":8}`

	for name, tc := range map[string]struct {
		method, path, contentType, body string
		status                          int
	}{
		"malformed json":      {"POST", "/api/v1/rooms", "application/json", `{"name":`, 400},
		"wrong media type":    {"POST", "/api/v1/rooms", "text/plain", valid, 400},
		"unknown field":       {"POST", "/api/v1/rooms", "application/json", `{"name":"a","max_players":4,"role":"admin"}`, 400},
		"duplicate field":     {"POST", "/api/v1/rooms", "application/json", `{"name":"a","max_players":4,"max_players":20}`, 400},
		"trailing object":     {"POST", "/api/v1/rooms", "application/json", valid + valid, 400},
		"oversized body":      {"POST", "/api/v1/rooms", "application/json", `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, 400},
		"field out of range":  {"POST", "/api/v1/rooms", "application/json", `{"name":"a","max_players":99}`, 422},
		"missing name":        {"POST", "/api/v1/rooms", "application/json", `{"max_players":8}`, 422},
		"path id zero":        {"GET", "/api/v1/users/0", "", "", 422},
		"path id padded":      {"GET", "/api/v1/users/007", "", "", 422},
		"query limit too big": {"GET", "/api/v1/friends?limit=1000", "", "", 422},
		"query repeated":      {"GET", "/api/v1/friends?limit=1&limit=2", "", "", 422},
		"query unknown":       {"GET", "/api/v1/friends?admin=1", "", "", 422},
	} {
		resp, body, err := try(tc.method, srv.URL+tc.path, tc.contentType, tc.body)
		if err != nil {
			if name == "oversized body" {
				continue // refused by closing the connection mid-upload: also a rejection
			}
			t.Errorf("%s: %v", name, err)
			continue
		}
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", name, resp.StatusCode, tc.status, body)
		}
		if v := httpresponse.Violations(resp.StatusCode, []byte(body)); len(v) != 0 {
			t.Errorf("%s: response violates the conventions: %v", name, v)
		}
		if tc.status == 400 && strings.Contains(body, `"field"`) {
			t.Errorf("%s: a 400 must carry no details.field: %s", name, body)
		}
	}
	if n := spy.calls.Load(); n != 0 {
		t.Fatalf("the service layer ran %d times for invalid requests", n)
	}
}

func TestOnlyValidatedDataReachesTheServiceLayer(t *testing.T) {
	srv, spy := validationServer(t)

	resp, body := send(t, "POST", srv.URL+"/api/v1/rooms", "application/json; charset=utf-8", `{"name":"Blue","max_players":8}`)
	if resp.StatusCode != 201 || len(httpresponse.Violations(resp.StatusCode, []byte(body))) != 0 {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	if in, _ := spy.last.Load().(roomInput); spy.calls.Load() != 1 || in != (roomInput{"Blue", 8}) {
		t.Errorf("service saw %d calls, last input %+v; the DTO must be mapped field for field", spy.calls.Load(), in)
	}

	resp, body = send(t, "GET", srv.URL+"/api/v1/friends?limit=5&offset=10", "", "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"limit":5`) || !strings.Contains(body, `"offset":10`) {
		t.Errorf("valid query: %d %s", resp.StatusCode, body)
	}
}

func TestAHugeBodyIsRejectedWithoutBeingReadInFull(t *testing.T) {
	srv, _ := validationServer(t)
	// 200 MiB streamed from a generator: if the server tried to read it all this
	// would take far longer than the assertion allows.
	body := io.LimitReader(zeroReader{}, 200<<20)
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/rooms", body)
	req.Header.Set("Content-Type", "application/json")

	begin := time.Now()
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("status %d, want 400", resp.StatusCode)
		}
	} // a reset connection is also an acceptable way to refuse the upload
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Errorf("handling a 200MiB upload took %s; the limit must stop the read early", elapsed)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}
