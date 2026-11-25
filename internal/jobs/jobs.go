// Package jobs defines the background jobs (River, backed by Postgres) and their workers.
//
// Job arguments are plain typed structs, so enqueuing a job with the wrong
// payload is a compile error. Services enqueue inside their own database
// transaction, so a job exists if and only if the change that caused it committed.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/bjornnorgaard/golden-path-go/internal/db/store"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/telemetry"
)

// Enqueuer is the part of the River client that services use.
type Enqueuer interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// DueReminderArgs triggers the reminder for a todo that has become due.
type DueReminderArgs struct {
	TodoID id.TodoID `json:"todo_id"`
}

func (DueReminderArgs) Kind() string { return "todo_due_reminder" }

// CompletedNotificationArgs triggers the notification for a completed todo.
type CompletedNotificationArgs struct {
	TodoID id.TodoID `json:"todo_id"`
}

func (CompletedNotificationArgs) Kind() string { return "todo_completed_notification" }

// SweepOverdueArgs triggers the periodic overdue sweep.
type SweepOverdueArgs struct{}

func (SweepOverdueArgs) Kind() string { return "sweep_overdue_todos" }

// DueReminderWorker handles DueReminderArgs.
type DueReminderWorker struct {
	river.WorkerDefaults[DueReminderArgs]
	Q       *store.Queries
	Log     *slog.Logger
	Metrics *telemetry.Metrics
}

func (w *DueReminderWorker) Work(ctx context.Context, job *river.Job[DueReminderArgs]) error {
	todo, err := w.Q.GetTodo(ctx, job.Args.TodoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.Log.DebugContext(ctx, "skipping due reminder: todo no longer exists", "todo_id", job.Args.TodoID)
			return nil
		}
		return err
	}
	if todo.IsComplete {
		w.Log.DebugContext(ctx, "skipping due reminder: todo already completed", "todo_id", todo.ID)
		return nil
	}
	w.Log.WarnContext(ctx, "reminder: todo is now due", "todo_id", todo.ID, "title", todo.Title)
	w.Metrics.DueReminderSent(ctx)
	return nil
}

// CompletedNotificationWorker handles CompletedNotificationArgs.
type CompletedNotificationWorker struct {
	river.WorkerDefaults[CompletedNotificationArgs]
	Q       *store.Queries
	Log     *slog.Logger
	Metrics *telemetry.Metrics
}

func (w *CompletedNotificationWorker) Work(ctx context.Context, job *river.Job[CompletedNotificationArgs]) error {
	todo, err := w.Q.GetTodo(ctx, job.Args.TodoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	w.Log.InfoContext(ctx, "notification: todo was completed", "todo_id", todo.ID, "title", todo.Title)
	w.Metrics.CompletionNotificationSent(ctx)
	return nil
}

// SweepOverdueWorker handles SweepOverdueArgs.
type SweepOverdueWorker struct {
	river.WorkerDefaults[SweepOverdueArgs]
	Q       *store.Queries
	Log     *slog.Logger
	Metrics *telemetry.Metrics
	Now     func() time.Time
}

func (w *SweepOverdueWorker) Work(ctx context.Context, _ *river.Job[SweepOverdueArgs]) error {
	now := w.Now()
	overdue, err := w.Q.ListOverdueTodos(ctx, &now)
	if err != nil {
		return err
	}
	for _, t := range overdue {
		w.Log.WarnContext(ctx, "overdue: todo is past its due date", "todo_id", t.ID, "title", t.Title, "due_by", t.DueBy)
		w.Metrics.OverdueDetected(ctx)
	}
	return nil
}

// NewClient builds the River client. It can always enqueue; call Start on it to
// also run the workers in this process.
func NewClient(pool *pgxpool.Pool, log *slog.Logger, metrics *telemetry.Metrics) (*river.Client[pgx.Tx], error) {
	q := store.New(pool)
	workers := river.NewWorkers()
	river.AddWorker(workers, &DueReminderWorker{Q: q, Log: log, Metrics: metrics})
	river.AddWorker(workers, &CompletedNotificationWorker{Q: q, Log: log, Metrics: metrics})
	river.AddWorker(workers, &SweepOverdueWorker{Q: q, Log: log, Metrics: metrics, Now: time.Now})

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return SweepOverdueArgs{}, nil },
				&river.PeriodicJobOpts{},
			),
		},
		Logger: log,
	})
	if err != nil {
		return nil, fmt.Errorf("create river client: %w", err)
	}
	return client, nil
}
