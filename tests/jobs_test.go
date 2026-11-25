package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bjornnorgaard/golden-path-go/internal/config"
	"github.com/bjornnorgaard/golden-path-go/internal/transport/rest/todosapi"
)

// Runs the real workers: a due-in-the-past todo gets its reminder, and completing a todo sends its notification.
func TestBackgroundJobs_RunTheWorkers(t *testing.T) {
	e, err := newEnv(context.Background(), func(c *config.Config) { c.JobsEnabled = true })
	if err != nil {
		t.Fatal(err)
	}
	defer e.close()
	if err := e.app.StartJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.app.StopJobs(context.Background()) }()

	c := authed(t).on(e)
	past := time.Now().Add(-time.Minute)
	created := post[todosapi.CreateTodoResponse](c, "/todos/create", todosapi.CreateTodoRequest{Title: "overdue todo", DueBy: &past}, http.StatusOK)

	eventually(t, 20*time.Second, "the due reminder to be logged", func() bool {
		return strings.Contains(logs.String(), "reminder: todo is now due") && strings.Contains(logs.String(), created.Id.String())
	})

	post[todosapi.ToggleTodoResponse](c, "/todos/toggle", todosapi.ToggleTodoRequest{Id: created.Id.String()}, http.StatusOK)
	eventually(t, 20*time.Second, "the completion notification to be logged", func() bool {
		return strings.Contains(logs.String(), "notification: todo was completed")
	})
}
