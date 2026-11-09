package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/controller"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/server"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

func reportHandler(pool *pgxpool.Pool) http.Handler {
	gin.SetMode(gin.TestMode)
	reports := service.NewReports(userpostgres.NewReports(pool), 24*time.Hour)
	health := controller.NewHealth(service.NewHealth(userpostgres.NewHealth(pool)))
	return server.New("0", health, slog.New(slog.NewTextHandler(io.Discard, nil)), controller.NewReports(reports, fakeSessions{})).Handler
}

func postReport(handler http.Handler, reporter, target int64, body string) friendResponse {
	return friendCall(handler, http.MethodPost, fmt.Sprintf("/api/v1/users/%d/reports", target), body, session(reporter))
}

// TestReportStoredPostgres checks a filed report on real records.
//
// Expected output:
//
//   - 201 returns the stored id and the public reason.
//   - The row holds the session's reporter, not one from the body, with the
//     stored reason, trimmed details and room.
//   - No outbox event is recorded, so the target is not notified.
func TestReportStoredPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := reportHandler(pool)

	response := postReport(handler, 1, 2, `{"reason":"HARASSMENT","details":"  insults  ","room_id":1001,"reporter_id":2}`)
	if response.status != http.StatusCreated {
		t.Fatalf("report = %d %s", response.status, response.raw)
	}
	var data struct {
		ReportID       int64  `json:"report_id"`
		ReportedUserID int64  `json:"reported_user_id"`
		Reason         string `json:"reason"`
		CreatedAt      string `json:"created_at"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil || data.ReportedUserID != 2 || data.Reason != "HARASSMENT" || data.CreatedAt == "" {
		t.Fatalf("data = %s", response.Data)
	}

	var reporter, target, room int64
	var reason, details string
	err := pool.QueryRow(context.Background(), `SELECT reporter_id, target_id, reason, details, room_id FROM reports WHERE id = $1`, data.ReportID).
		Scan(&reporter, &target, &reason, &details, &room)
	if err != nil || reporter != 1 || target != 2 || reason != "harassment" || details != "insults" || room != 1001 {
		t.Fatalf("row = %d %d %s %q %d, %v", reporter, target, reason, details, room, err)
	}
	if got := count(t, pool, `SELECT count(*) FROM outbox`); got != 0 {
		t.Fatalf("outbox rows = %d", got)
	}
}

// TestReportWindowPostgres checks repeats and what the window covers.
//
// Expected output:
//
//   - A repeat is 409 with retry_after_seconds inside the window, and stores nothing.
//   - The opposite direction and another target are separate.
//   - A new report is accepted once the window has passed.
func TestReportWindowPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2, 3)
	handler := reportHandler(pool)
	const body = `{"reason":"SPAM"}`

	if response := postReport(handler, 1, 2, body); response.status != http.StatusCreated {
		t.Fatalf("first = %d %s", response.status, response.raw)
	}
	repeat := postReport(handler, 1, 2, `{"reason":"OTHER"}`)
	expectCode(t, repeat, http.StatusConflict, "ALREADY_REPORTED")
	retry, _ := repeat.Error.Details["retry_after_seconds"].(float64)
	if retry <= 0 || retry > 86400 || repeat.Error.Details["user_id"] != float64(2) {
		t.Fatalf("details = %v", repeat.Error.Details)
	}
	if got := count(t, pool, `SELECT count(*) FROM reports`); got != 1 {
		t.Fatalf("reports after repeat = %d", got)
	}

	for _, pair := range [][2]int64{{2, 1}, {1, 3}} {
		if response := postReport(handler, pair[0], pair[1], body); response.status != http.StatusCreated {
			t.Fatalf("report %v = %d %s", pair, response.status, response.raw)
		}
	}

	mustExec(t, pool, `UPDATE reports SET created_at = now() - interval '25 hours' WHERE reporter_id = 1 AND target_id = 2`)
	if response := postReport(handler, 1, 2, body); response.status != http.StatusCreated {
		t.Fatalf("after the window = %d %s", response.status, response.raw)
	}
}

// TestReportRefusalsStoreNothingPostgres checks every error against real records.
//
// Expected output: each refusal has its code and no report row exists.
func TestReportRefusalsStoreNothingPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := reportHandler(pool)
	mustExec(t, pool, `INSERT INTO deletion_operations (operation_id, user_id) VALUES ('3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53', 2)`)

	expectCode(t, postReport(handler, 1, 404, `{"reason":"SPAM"}`), http.StatusNotFound, "USER_NOT_FOUND")
	expectCode(t, postReport(handler, 1, 2, `{"reason":"SPAM"}`), http.StatusNotFound, "USER_NOT_FOUND")
	expectCode(t, postReport(handler, 1, 1, `{"reason":"SPAM"}`), http.StatusUnprocessableEntity, "CANNOT_REPORT_SELF")
	expectCode(t, postReport(handler, 1, 2, `{"reason":"NOPE"}`), http.StatusUnprocessableEntity, "VALIDATION_ERROR")
	expectCode(t, friendCall(handler, http.MethodPost, "/api/v1/users/2/reports", `{"reason":"SPAM"}`, ""), http.StatusUnauthorized, "UNAUTHORIZED")
	if got := count(t, pool, `SELECT count(*) FROM reports`); got != 0 {
		t.Fatalf("reports = %d", got)
	}
}

// TestReportConcurrentPostgres checks racing identical reports.
//
// Expected output: one 201, the others 409, and one stored row.
func TestReportConcurrentPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	handler := reportHandler(pool)

	var reports []func() int
	for range 12 {
		reports = append(reports, func() int { return postReport(handler, 1, 2, `{"reason":"SPAM"}`).status })
	}
	created := 0
	for _, status := range runTogether(reports...) {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("concurrent report = %d", status)
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM reports`); created != 1 || got != 1 {
		t.Fatalf("created = %d, rows = %d; want one each", created, got)
	}
}

// TestReportNotExposedPostgres checks that no public route shows a report.
//
// Expected output: the friend and block routes never mention the report's text.
func TestReportNotExposedPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 1, 2)
	social := blockHandler(t, pool)
	befriend(t, social, 1, 2)
	const secret = "private-moderation-note"
	if response := postReport(reportHandler(pool), 1, 2, fmt.Sprintf(`{"reason":"OTHER","details":%q}`, secret)); response.status != http.StatusCreated {
		t.Fatalf("report = %d %s", response.status, response.raw)
	}
	for _, path := range []string{"/api/v1/friends", friendRequestsPath, "/api/v1/users/search?q=player"} {
		for _, viewer := range []int64{1, 2} {
			if response := friendCall(social, http.MethodGet, path, "", session(viewer)); response.status != http.StatusOK ||
				strings.Contains(response.raw, secret) || strings.Contains(response.raw, "report") {
				t.Fatalf("%s for %d = %d %s", path, viewer, response.status, response.raw)
			}
		}
	}
}
