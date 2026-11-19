package service

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type scheduledRunnerPlanRepo struct {
	ScheduledTestPlanRepository
	listDue func(context.Context) ([]*ScheduledTestPlan, error)
}

func (repo scheduledRunnerPlanRepo) ListDue(ctx context.Context, _ time.Time) ([]*ScheduledTestPlan, error) {
	return repo.listDue(ctx)
}

func TestScheduledTestRunnerScansWhileHoldingPostgresLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	lockID := hashAdvisoryLockID(scheduledTestRunnerLockKey)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).WithArgs(lockID).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
	mock.ExpectQuery("SELECT due_plans").WillReturnError(errors.New("scan failed"))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).WithArgs(lockID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	planRepo := scheduledRunnerPlanRepo{listDue: func(ctx context.Context) ([]*ScheduledTestPlan, error) {
		return nil, db.QueryRowContext(ctx, "SELECT due_plans").Scan(new(int))
	}}
	runner := NewScheduledTestRunnerService(planRepo, nil, nil, nil, nil, db)
	runner.runDuePlans(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScheduledTestRunnerSkipsWithoutPostgresLock(t *testing.T) {
	for _, scenario := range []struct {
		name string
		mock func(*sqlmock.ExpectedQuery)
	}{
		{name: "lock busy", mock: func(query *sqlmock.ExpectedQuery) {
			query.WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(false))
		}},
		{name: "lock query failed", mock: func(query *sqlmock.ExpectedQuery) {
			query.WillReturnError(errors.New("lock query failed"))
		}},
		{name: "database unavailable"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var db *sql.DB
			if scenario.mock != nil {
				mockDB, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() { _ = mockDB.Close() })
				db = mockDB
				query := mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
					WithArgs(hashAdvisoryLockID(scheduledTestRunnerLockKey))
				scenario.mock(query)
				t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
			}
			planRepo := scheduledRunnerPlanRepo{listDue: func(context.Context) ([]*ScheduledTestPlan, error) {
				t.Fatal("scanned plans without holding the lock")
				return nil, nil
			}}
			runner := NewScheduledTestRunnerService(planRepo, nil, nil, nil, nil, db)
			runner.runDuePlans(context.Background())
		})
	}
}
