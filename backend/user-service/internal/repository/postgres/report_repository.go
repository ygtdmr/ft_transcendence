package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.ReportStore = (*ReportRepository)(nil)

// ReportRepository stores reports.
type ReportRepository struct {
	pool *pgxpool.Pool
}

// NewReports returns a report store; pool ownership stays with the caller.
func NewReports(pool *pgxpool.Pool) *ReportRepository {
	return &ReportRepository{pool: pool}
}

// FileReport implements service.ReportStore. users.file_report checks the
// window and inserts under one lock, so racing reports store one row. The
// target is not locked, because a report outlives the profile it names.
func (r *ReportRepository) FileReport(ctx context.Context, report service.NewReport) (service.StoredReport, error) {
	var available bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM profiles p WHERE p.user_id = $1
		AND NOT EXISTS (SELECT 1 FROM deletion_operations d WHERE d.user_id = p.user_id))`, report.TargetID).Scan(&available)
	if err != nil {
		return service.StoredReport{}, classify(err)
	}
	if !available {
		return service.StoredReport{}, service.ErrUserNotFound
	}

	var id, retryAfter *int64
	var createdAt *time.Time
	err = r.pool.QueryRow(ctx, `SELECT out_report_id, out_created_at, retry_after_seconds
		FROM file_report($1, $2, $3, $4, $5, make_interval(secs => $6))`,
		report.ReporterID, report.TargetID, report.Reason, report.Details, report.RoomID, report.Window.Seconds()).
		Scan(&id, &createdAt, &retryAfter)
	if err != nil {
		return service.StoredReport{}, classify(err)
	}
	if id == nil {
		return service.StoredReport{}, &service.AlreadyReportedError{RetryAfterSeconds: *retryAfter}
	}

	return service.StoredReport{ID: *id, CreatedAt: createdAt.UTC()}, nil
}
