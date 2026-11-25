package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bjornnorgaard/golden-path-go/internal/transport/rest/todosapi"
)

func createTodo(t *testing.T, title string, dueBy *time.Time) todosapi.CreateTodoResponse {
	t.Helper()
	return post[todosapi.CreateTodoResponse](authed(t), "/todos/create", todosapi.CreateTodoRequest{Title: title, DueBy: dueBy}, http.StatusOK)
}

func countJobs(t *testing.T, kind string, todoID uuid.UUID) int {
	t.Helper()
	var n int
	err := suite.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'todo_id' = $2`, kind, todoID.String()).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateTodo_Success(t *testing.T) {
	t.Parallel()
	title := "A todo title that satisfies the contract minimum"
	created := createTodo(t, title, nil)
	if created.Title != title || created.IsComplete || created.Id == uuid.Nil {
		t.Fatalf("unexpected response: %+v", created)
	}

	// Persisted, and owned by the authenticated user.
	var owner uuid.UUID
	if err := suite.pool.QueryRow(context.Background(), `SELECT owner_user_id FROM todos WHERE id = $1`, created.Id).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != testPrincipal.UserID.UUID() {
		t.Fatalf("owner = %s, want %s", owner, testPrincipal.UserID)
	}

	fetched := post[todosapi.GetTodoByIdResponse](authed(t), "/todos/get-by-id", todosapi.GetTodoByIdRequest{Id: created.Id.String()}, http.StatusOK)
	if fetched.Id != created.Id || fetched.Title != title {
		t.Fatalf("fetched = %+v, want id %s title %q", fetched, created.Id, title)
	}
}

func TestCreateTodo_WithDueDate_SchedulesReminderAtDueTime(t *testing.T) {
	t.Parallel()
	due := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	created := createTodo(t, "Due in two days", &due)
	if created.DueBy == nil || !created.DueBy.Equal(due) {
		t.Fatalf("dueBy = %v, want %v", created.DueBy, due)
	}

	var scheduled time.Time
	err := suite.pool.QueryRow(context.Background(),
		`SELECT scheduled_at FROM river_job WHERE kind = 'todo_due_reminder' AND args->>'todo_id' = $1`, created.Id.String()).Scan(&scheduled)
	if err != nil {
		t.Fatalf("reminder job not enqueued: %v", err)
	}
	if !scheduled.Equal(due) {
		t.Fatalf("reminder scheduled at %v, want %v", scheduled, due)
	}
}

func TestCreateTodo_InvalidTitle_ReturnsValidationProblem(t *testing.T) {
	t.Parallel()
	for name, title := range map[string]string{
		"too short":        "ab",
		"only whitespace":  "      ",
		"padded too short": "  ab  ",
		"too long":         strings.Repeat("x", 501),
	} {
		t.Run(name, func(t *testing.T) {
			errs := post[validationErrors](authed(t), "/todos/create", todosapi.CreateTodoRequest{Title: title}, http.StatusBadRequest)
			if len(errs["title"]) == 0 {
				t.Fatalf("expected a title error, got %v", errs)
			}
		})
	}
}

func TestCreateTodo_ContractViolations_AreRejectedBeforeTheHandler(t *testing.T) {
	t.Parallel()
	c := authed(t)
	for name, body := range map[string]any{
		"missing title": map[string]any{},
		"wrong type":    map[string]any{"title": 42},
		"bad dueBy":     map[string]any{"title": "valid title", "dueBy": "tomorrow-ish"},
	} {
		t.Run(name, func(t *testing.T) {
			post[validationErrors](c, "/todos/create", body, http.StatusBadRequest)
		})
	}
}

func TestGetTodoById(t *testing.T) {
	t.Parallel()
	c := authed(t)
	t.Run("unknown id is 404", func(t *testing.T) {
		post[any](c, "/todos/get-by-id", todosapi.GetTodoByIdRequest{Id: uuid.NewString()}, http.StatusNotFound)
	})
	t.Run("malformed id is 400", func(t *testing.T) {
		errs := post[validationErrors](c, "/todos/get-by-id", todosapi.GetTodoByIdRequest{Id: "not-a-uuid"}, http.StatusBadRequest)
		if len(errs["id"]) == 0 {
			t.Fatalf("expected an id error, got %v", errs)
		}
	})
}

func TestGetTodoList(t *testing.T) {
	t.Parallel()
	for range 3 {
		createTodo(t, "list me please", nil)
	}
	c := authed(t)
	i := func(v int32) *int32 { return &v }

	t.Run("limit is honoured", func(t *testing.T) {
		got := post[todosapi.GetTodoListResponse](c, "/todos/get-list", todosapi.GetTodoListRequest{Limit: i(2)}, http.StatusOK)
		if len(got.Todos) != 2 {
			t.Fatalf("len = %d, want 2", len(got.Todos))
		}
	})
	t.Run("omitted paging uses defaults", func(t *testing.T) {
		got := post[todosapi.GetTodoListResponse](c, "/todos/get-list", todosapi.GetTodoListRequest{}, http.StatusOK)
		if len(got.Todos) == 0 || len(got.Todos) > 20 {
			t.Fatalf("len = %d, want 1..20 (default page size)", len(got.Todos))
		}
	})
	t.Run("invalid paging is 400", func(t *testing.T) {
		for name, req := range map[string]todosapi.GetTodoListRequest{
			"limit 0":         {Limit: i(0)},
			"limit above max": {Limit: i(201)},
			"negative offset": {Offset: i(-1)},
		} {
			t.Run(name, func(t *testing.T) { post[validationErrors](c, "/todos/get-list", req, http.StatusBadRequest) })
		}
	})
}

func TestGetTodoList_ClampsToConfiguredMaximum(t *testing.T) {
	// Not parallel: needs more than MaxPageSize rows to be observable.
	for range 105 {
		if _, err := suite.pool.Exec(context.Background(),
			`INSERT INTO todos (id, title, is_complete, owner_user_id) VALUES (gen_random_uuid(), 'bulk row', false, $1)`, testPrincipal.UserID); err != nil {
			t.Fatal(err)
		}
	}
	limit := int32(150) // valid per contract (max 200) but above the configured maximum of 100
	got := post[todosapi.GetTodoListResponse](authed(t), "/todos/get-list", todosapi.GetTodoListRequest{Limit: &limit}, http.StatusOK)
	if len(got.Todos) != 100 {
		t.Fatalf("len = %d, want 100", len(got.Todos))
	}
}

func TestUpdateTodo(t *testing.T) {
	t.Parallel()
	c := authed(t)
	created := createTodo(t, "before the update", nil)

	updated := post[todosapi.UpdateTodoResponse](c, "/todos/update",
		todosapi.UpdateTodoRequest{Id: created.Id.String(), Title: "after the update", IsComplete: true}, http.StatusOK)
	if updated.Title != "after the update" || !updated.IsComplete {
		t.Fatalf("unexpected update response: %+v", updated)
	}
	if n := countJobs(t, "todo_completed_notification", created.Id); n != 1 {
		t.Fatalf("completion notification jobs = %d, want 1", n)
	}

	// Updating an already-complete todo must not notify again.
	post[todosapi.UpdateTodoResponse](c, "/todos/update",
		todosapi.UpdateTodoRequest{Id: created.Id.String(), Title: "still complete", IsComplete: true}, http.StatusOK)
	if n := countJobs(t, "todo_completed_notification", created.Id); n != 1 {
		t.Fatalf("completion notification jobs = %d after no-op completion, want 1", n)
	}

	t.Run("invalid title is 400", func(t *testing.T) {
		post[validationErrors](c, "/todos/update", todosapi.UpdateTodoRequest{Id: created.Id.String(), Title: "ab"}, http.StatusBadRequest)
	})
	t.Run("unknown id is 404", func(t *testing.T) {
		post[any](c, "/todos/update", todosapi.UpdateTodoRequest{Id: uuid.NewString(), Title: "valid title"}, http.StatusNotFound)
	})
}

func TestToggleTodo(t *testing.T) {
	t.Parallel()
	c := authed(t)
	created := createTodo(t, "toggle me", nil)

	on := post[todosapi.ToggleTodoResponse](c, "/todos/toggle", todosapi.ToggleTodoRequest{Id: created.Id.String()}, http.StatusOK)
	if !on.IsComplete {
		t.Fatal("first toggle should complete the todo")
	}
	if n := countJobs(t, "todo_completed_notification", created.Id); n != 1 {
		t.Fatalf("completion notification jobs = %d, want 1", n)
	}
	off := post[todosapi.ToggleTodoResponse](c, "/todos/toggle", todosapi.ToggleTodoRequest{Id: created.Id.String()}, http.StatusOK)
	if off.IsComplete {
		t.Fatal("second toggle should reopen the todo")
	}
	if n := countJobs(t, "todo_completed_notification", created.Id); n != 1 {
		t.Fatalf("reopening must not notify; jobs = %d, want 1", n)
	}

	post[any](c, "/todos/toggle", todosapi.ToggleTodoRequest{Id: uuid.NewString()}, http.StatusNotFound)
	post[validationErrors](c, "/todos/toggle", todosapi.ToggleTodoRequest{Id: "nope"}, http.StatusBadRequest)
}

func TestDeleteTodo(t *testing.T) {
	t.Parallel()
	c := authed(t)
	created := createTodo(t, "delete me please", nil)

	deleted := post[todosapi.DeleteTodoResponse](c, "/todos/delete", todosapi.DeleteTodoRequest{Id: created.Id.String()}, http.StatusOK)
	if deleted.Id != created.Id {
		t.Fatalf("deleted id = %s, want %s", deleted.Id, created.Id)
	}
	post[any](c, "/todos/get-by-id", todosapi.GetTodoByIdRequest{Id: created.Id.String()}, http.StatusNotFound)
	post[any](c, "/todos/delete", todosapi.DeleteTodoRequest{Id: created.Id.String()}, http.StatusNotFound)
	post[validationErrors](c, "/todos/delete", todosapi.DeleteTodoRequest{Id: "nope"}, http.StatusBadRequest)
}
