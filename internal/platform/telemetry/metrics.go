package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Metrics are the application-level counters. Create once with NewMetrics and
// pass it to whoever records; with no provider installed, recording is a no-op.
type Metrics struct {
	todosCreated        metric.Int64Counter
	todosCompleted      metric.Int64Counter
	dueRemindersSent    metric.Int64Counter
	completionNotifSent metric.Int64Counter
	overdueDetected     metric.Int64Counter
}

// NewMetrics creates the counters on the global meter provider.
func NewMetrics() (*Metrics, error) {
	m := otel.Meter("github.com/bjornnorgaard/golden-path-go")
	var (
		mm  Metrics
		err error
	)
	if mm.todosCreated, err = m.Int64Counter("todos.created", metric.WithDescription("Todos created")); err != nil {
		return nil, err
	}
	if mm.todosCompleted, err = m.Int64Counter("todos.completed", metric.WithDescription("Todos marked complete")); err != nil {
		return nil, err
	}
	if mm.dueRemindersSent, err = m.Int64Counter("todos.due_reminders_sent", metric.WithDescription("Due reminders sent")); err != nil {
		return nil, err
	}
	if mm.completionNotifSent, err = m.Int64Counter("todos.completion_notifications_sent", metric.WithDescription("Completion notifications sent")); err != nil {
		return nil, err
	}
	if mm.overdueDetected, err = m.Int64Counter("todos.overdue_detected", metric.WithDescription("Overdue todos found by the sweep")); err != nil {
		return nil, err
	}
	return &mm, nil
}

func (m *Metrics) TodoCreated(ctx context.Context)                { m.todosCreated.Add(ctx, 1) }
func (m *Metrics) TodoCompleted(ctx context.Context)              { m.todosCompleted.Add(ctx, 1) }
func (m *Metrics) DueReminderSent(ctx context.Context)            { m.dueRemindersSent.Add(ctx, 1) }
func (m *Metrics) CompletionNotificationSent(ctx context.Context) { m.completionNotifSent.Add(ctx, 1) }
func (m *Metrics) OverdueDetected(ctx context.Context)            { m.overdueDetected.Add(ctx, 1) }
