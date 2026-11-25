// Package todos holds the todo business rules. It is transport-agnostic: REST and
// GraphQL are thin adapters over this service and must not contain rules of their own.
package todos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/bjornnorgaard/golden-path-go/internal/db/store"
	"github.com/bjornnorgaard/golden-path-go/internal/domain"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	"github.com/bjornnorgaard/golden-path-go/internal/jobs"
	"github.com/bjornnorgaard/golden-path-go/internal/paging"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/telemetry"
)

const (
	minTitleLength = 3
	maxTitleLength = 500 // matches the title column
)

// Todo is the domain representation of a todo.
type Todo struct {
	ID         id.TodoID
	Title      string
	DueBy      *time.Time
	IsComplete bool
}

// Service implements the todo use cases.
type Service struct {
	pool    *pgxpool.Pool
	q       *store.Queries
	jobs    jobs.Enqueuer
	paging  paging.Options
	metrics *telemetry.Metrics
}

// NewService wires the service. All dependencies are explicit.
func NewService(pool *pgxpool.Pool, enqueuer jobs.Enqueuer, p paging.Options, m *telemetry.Metrics) *Service {
	return &Service{pool: pool, q: store.New(pool), jobs: enqueuer, paging: p, metrics: m}
}

// CreateInput is the input of Create.
type CreateInput struct {
	OwnerID id.UserID
	Title   string
	DueBy   *time.Time
}

// Create adds an incomplete todo and, if it has a due date, schedules its reminder
// in the same transaction.
func (s *Service) Create(ctx context.Context, in CreateInput) (Todo, error) {
	if err := validateTitle(in.Title); err != nil {
		return Todo{}, err
	}
	dueBy := utc(in.DueBy)

	var created store.Todo
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		created, err = s.q.WithTx(tx).CreateTodo(ctx, store.CreateTodoParams{
			ID: id.New[id.Todo](), Title: in.Title, DueBy: dueBy, OwnerUserID: in.OwnerID,
		})
		if err != nil {
			return err
		}
		if dueBy != nil {
			// A past due date is scheduled in the past, which River runs immediately.
			_, err = s.jobs.InsertTx(ctx, tx, jobs.DueReminderArgs{TodoID: created.ID}, &river.InsertOpts{ScheduledAt: *dueBy})
		}
		return err
	})
	if err != nil {
		return Todo{}, fmt.Errorf("create todo: %w", err)
	}
	s.metrics.TodoCreated(ctx)
	return fromRow(created), nil
}

// Get returns a todo or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, todoID id.TodoID) (Todo, error) {
	row, err := s.q.GetTodo(ctx, todoID)
	if err != nil {
		return Todo{}, notFound(err, "get todo")
	}
	return fromRow(row), nil
}

// List returns a page of todos ordered by ID.
func (s *Service) List(ctx context.Context, limit, offset *int32) ([]Todo, error) {
	l, o, err := paging.Resolve(limit, offset, s.paging)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListTodos(ctx, store.ListTodosParams{Limit: l, Offset: o})
	if err != nil {
		return nil, fmt.Errorf("list todos: %w", err)
	}
	out := make([]Todo, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

// UpdateInput is the input of Update.
type UpdateInput struct {
	ID         id.TodoID
	Title      string
	DueBy      *time.Time
	IsComplete bool
}

// Update replaces a todo's title, due date and completion state.
func (s *Service) Update(ctx context.Context, in UpdateInput) (Todo, error) {
	if err := validateTitle(in.Title); err != nil {
		return Todo{}, err
	}
	var updated store.Todo
	var becameComplete bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := q.GetTodoForUpdate(ctx, in.ID)
		if err != nil {
			return err
		}
		updated, err = q.UpdateTodo(ctx, store.UpdateTodoParams{ID: in.ID, Title: in.Title, DueBy: utc(in.DueBy), IsComplete: in.IsComplete})
		if err != nil {
			return err
		}
		becameComplete = updated.IsComplete && !current.IsComplete
		if becameComplete {
			_, err = s.jobs.InsertTx(ctx, tx, jobs.CompletedNotificationArgs{TodoID: updated.ID}, nil)
		}
		return err
	})
	if err != nil {
		return Todo{}, notFound(err, "update todo")
	}
	if becameComplete {
		s.metrics.TodoCompleted(ctx)
	}
	return fromRow(updated), nil
}

// Toggle flips a todo's completion state atomically.
func (s *Service) Toggle(ctx context.Context, todoID id.TodoID) (Todo, error) {
	var toggled store.Todo
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		toggled, err = s.q.WithTx(tx).ToggleTodo(ctx, todoID)
		if err != nil {
			return err
		}
		if toggled.IsComplete {
			_, err = s.jobs.InsertTx(ctx, tx, jobs.CompletedNotificationArgs{TodoID: toggled.ID}, nil)
		}
		return err
	})
	if err != nil {
		return Todo{}, notFound(err, "toggle todo")
	}
	if toggled.IsComplete {
		s.metrics.TodoCompleted(ctx)
	}
	return fromRow(toggled), nil
}

// Delete removes a todo, returning domain.ErrNotFound if it does not exist.
func (s *Service) Delete(ctx context.Context, todoID id.TodoID) error {
	n, err := s.q.DeleteTodo(ctx, todoID)
	if err != nil {
		return fmt.Errorf("delete todo: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func validateTitle(title string) error {
	trimmed := strings.TrimSpace(title)
	switch {
	case utf8.RuneCountInString(trimmed) < minTitleLength:
		return domain.ValidationError{"title": {fmt.Sprintf("title must be at least %d non-whitespace characters", minTitleLength)}}
	case utf8.RuneCountInString(title) > maxTitleLength:
		return domain.ValidationError{"title": {fmt.Sprintf("title must be at most %d characters", maxTitleLength)}}
	}
	return nil
}

// utc normalises a due date to UTC; the time of day matters because it drives the reminder job.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func notFound(err error, op string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

func fromRow(r store.Todo) Todo {
	return Todo{ID: r.ID, Title: r.Title, DueBy: r.DueBy, IsComplete: r.IsComplete}
}
