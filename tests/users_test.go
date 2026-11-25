package tests

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/bjornnorgaard/golden-path-go/internal/transport/rest/usersapi"
)

func provision(t *testing.T, email string) uuid.UUID {
	t.Helper()
	u, err := suite.app.Users.Provision(context.Background(), email, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID.UUID()
}

func TestGetUserById(t *testing.T) {
	t.Parallel()
	c := authed(t)
	got := post[usersapi.GetUserByIdResponse](c, "/users/get-by-id", usersapi.GetUserByIdRequest{Id: testPrincipal.UserID.String()}, http.StatusOK)
	if got.Email != testPrincipal.Email {
		t.Fatalf("email = %q, want %q", got.Email, testPrincipal.Email)
	}
	post[any](c, "/users/get-by-id", usersapi.GetUserByIdRequest{Id: uuid.NewString()}, http.StatusNotFound)
	post[validationErrors](c, "/users/get-by-id", usersapi.GetUserByIdRequest{Id: "nope"}, http.StatusBadRequest)
}

func TestGetUserList(t *testing.T) {
	t.Parallel()
	c := authed(t)
	limit := int32(1)
	got := post[usersapi.GetUserListResponse](c, "/users/get-list", usersapi.GetUserListRequest{Limit: &limit}, http.StatusOK)
	if len(got.Users) != 1 {
		t.Fatalf("len = %d, want 1", len(got.Users))
	}
	zero := int32(0)
	post[validationErrors](c, "/users/get-list", usersapi.GetUserListRequest{Limit: &zero}, http.StatusBadRequest)
}

func TestUpdateUser(t *testing.T) {
	t.Parallel()
	c := authed(t)
	userID := provision(t, "update-"+uuid.NewString()+"@example.com")
	name, given := "New Name", "New"

	got := post[usersapi.UpdateUserResponse](c, "/users/update",
		usersapi.UpdateUserRequest{Id: userID.String(), DisplayName: &name, GivenName: &given}, http.StatusOK)
	if got.DisplayName == nil || *got.DisplayName != name || got.FamilyName != nil {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Email == "" {
		t.Fatal("email must be returned and is not changeable")
	}
	post[any](c, "/users/update", usersapi.UpdateUserRequest{Id: uuid.NewString()}, http.StatusNotFound)
}

func TestDeleteUser_CascadesToTheirTodos(t *testing.T) {
	t.Parallel()
	c := authed(t)
	userID := provision(t, "delete-"+uuid.NewString()+"@example.com")
	todoID := uuid.New()
	if _, err := suite.pool.Exec(context.Background(),
		`INSERT INTO todos (id, title, is_complete, owner_user_id) VALUES ($1, 'owned by doomed user', false, $2)`, todoID, userID); err != nil {
		t.Fatal(err)
	}

	post[usersapi.DeleteUserResponse](c, "/users/delete", usersapi.DeleteUserRequest{Id: userID.String()}, http.StatusOK)

	var n int
	if err := suite.pool.QueryRow(context.Background(), `SELECT count(*) FROM todos WHERE id = $1`, todoID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("todo should be cascade-deleted: n=%d err=%v", n, err)
	}
	post[any](c, "/users/delete", usersapi.DeleteUserRequest{Id: userID.String()}, http.StatusNotFound)
	post[validationErrors](c, "/users/delete", usersapi.DeleteUserRequest{Id: "nope"}, http.StatusBadRequest)
}
