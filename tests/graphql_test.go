package tests

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGraphQL_TodoLifecycle(t *testing.T) {
	t.Parallel()
	c := authed(t)

	data, errs := c.graphql(`mutation($i: CreateTodoRequest!) { createTodo(input: $i) { id title isComplete dueBy } }`,
		map[string]any{"i": map[string]any{"title": "created over graphql"}})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	var created struct {
		CreateTodo struct {
			ID, Title  string
			IsComplete bool
		}
	}
	mustDecode(t, data, &created)
	if created.CreateTodo.Title != "created over graphql" || created.CreateTodo.IsComplete {
		t.Fatalf("unexpected: %+v", created)
	}
	todoID := created.CreateTodo.ID

	data, errs = c.graphql(`query($i: GetTodoByIdRequest!) { getTodoById(input: $i) { id title } }`, map[string]any{"i": map[string]any{"id": todoID}})
	if len(errs) > 0 || !strings.Contains(string(data), todoID) {
		t.Fatalf("get failed: data=%s errs=%+v", data, errs)
	}

	data, errs = c.graphql(`mutation($i: ToggleTodoRequest!) { toggleTodo(input: $i) { isComplete } }`, map[string]any{"i": map[string]any{"id": todoID}})
	if len(errs) > 0 || !strings.Contains(string(data), `"isComplete":true`) {
		t.Fatalf("toggle failed: data=%s errs=%+v", data, errs)
	}
	if n := countJobs(t, "todo_completed_notification", uuid.MustParse(todoID)); n != 1 {
		t.Fatalf("completion notification jobs = %d, want 1 (REST and GraphQL share the service)", n)
	}

	_, errs = c.graphql(`mutation($i: DeleteTodoRequest!) { deleteTodo(input: $i) { id } }`, map[string]any{"i": map[string]any{"id": todoID}})
	if len(errs) > 0 {
		t.Fatalf("delete failed: %+v", errs)
	}
}

func TestGraphQL_Errors(t *testing.T) {
	t.Parallel()
	c := authed(t)

	t.Run("business rule violation is BAD_USER_INPUT with per-property errors", func(t *testing.T) {
		_, errs := c.graphql(`mutation { createTodo(input: {title: "ab"}) { id } }`, nil)
		if len(errs) != 1 || errs[0].Extensions["code"] != "BAD_USER_INPUT" {
			t.Fatalf("errs = %+v", errs)
		}
		props, _ := errs[0].Extensions["errors"].(map[string]any)
		if _, ok := props["title"]; !ok {
			t.Fatalf("expected title in %v", props)
		}
	})
	t.Run("unknown todo is NOT_FOUND", func(t *testing.T) {
		_, errs := c.graphql(`query($i: GetTodoByIdRequest!) { getTodoById(input: $i) { id } }`, map[string]any{"i": map[string]any{"id": uuid.NewString()}})
		if len(errs) != 1 || errs[0].Extensions["code"] != "NOT_FOUND" {
			t.Fatalf("errs = %+v", errs)
		}
	})
	t.Run("invalid paging is BAD_USER_INPUT", func(t *testing.T) {
		_, errs := c.graphql(`query { getTodoList(input: {limit: 0}) { todos { id } } }`, nil)
		if len(errs) != 1 || errs[0].Extensions["code"] != "BAD_USER_INPUT" {
			t.Fatalf("errs = %+v", errs)
		}
	})
	t.Run("schema violations are rejected by the executable schema", func(t *testing.T) {
		_, errs := c.graphql(`mutation { createTodo(input: {}) { id } }`, nil)
		if len(errs) == 0 {
			t.Fatal("expected a schema validation error")
		}
	})
}

func TestGraphQL_UsersAndList(t *testing.T) {
	t.Parallel()
	c := authed(t)
	data, errs := c.graphql(`query { getUserList(input: {limit: 1}) { users { id email } } }`, nil)
	if len(errs) > 0 || !strings.Contains(string(data), `"email"`) {
		t.Fatalf("data=%s errs=%+v", data, errs)
	}
	data, errs = c.graphql(`query($i: GetUserByIdRequest!) { getUserById(input: $i) { email } }`, map[string]any{"i": map[string]any{"id": testPrincipal.UserID.String()}})
	if len(errs) > 0 || !strings.Contains(string(data), testPrincipal.Email) {
		t.Fatalf("data=%s errs=%+v", data, errs)
	}
}

func mustDecode(t *testing.T, data json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
}
