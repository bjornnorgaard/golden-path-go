package graphql

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	gqlgraphql "github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/domain"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	"github.com/bjornnorgaard/golden-path-go/internal/todos"
	"github.com/bjornnorgaard/golden-path-go/internal/users"
)

// Resolver implements the generated ResolverRoot. Like the REST adapters it is a
// thin mapping layer over the services; the compiler enforces that every field
// in the schema has a resolver.
type Resolver struct {
	Todos *todos.Service
	Users *users.Service
}

var _ ResolverRoot = (*Resolver)(nil)

func (r *Resolver) Query() QueryResolver       { return &queryResolver{r} }
func (r *Resolver) Mutation() MutationResolver { return &mutationResolver{r} }

type queryResolver struct{ *Resolver }
type mutationResolver struct{ *Resolver }

// Handler returns the GraphQL HTTP handler. introspection should only be on when docs are enabled.
func Handler(r *Resolver, log *slog.Logger, introspection bool) http.Handler {
	srv := handler.New(NewExecutableSchema(Config{Resolvers: r}))
	srv.AddTransport(transport.POST{})
	if introspection {
		srv.Use(extension.Introspection{})
	}
	srv.SetErrorPresenter(func(ctx context.Context, err error) *gqlerror.Error {
		var gqlErr *gqlerror.Error
		if errors.As(err, &gqlErr) && gqlErr.Extensions != nil && gqlErr.Extensions["code"] != nil {
			return gqlErr
		}
		presented := gqlgraphql.DefaultErrorPresenter(ctx, err)
		if presented.Extensions == nil || presented.Extensions["code"] == nil {
			// Not one of ours and not a GraphQL validation/parse error: do not leak internals.
			log.ErrorContext(ctx, "unhandled graphql error", "error", err)
			return &gqlerror.Error{Path: presented.Path, Message: "An unexpected error occurred.", Extensions: map[string]any{"code": "INTERNAL"}}
		}
		return presented
	})
	return srv
}

// Playground returns the GraphQL playground page, served at the given endpoint.
func Playground(endpoint string) http.Handler { return playground.Handler("GraphQL", endpoint) }

// ---- Queries ----

func (r *queryResolver) GetTodoByID(ctx context.Context, in GetTodoByIDRequest) (*GetTodoByIDResponse, error) {
	todoID, err := parseTodoID(in.ID)
	if err != nil {
		return nil, err
	}
	t, err := r.Todos.Get(ctx, todoID)
	if err != nil {
		return nil, translate(err)
	}
	return &GetTodoByIDResponse{ID: t.ID.String(), Title: t.Title, DueBy: t.DueBy, IsComplete: t.IsComplete}, nil
}

func (r *queryResolver) GetTodoList(ctx context.Context, in GetTodoListRequest) (*GetTodoListResponse, error) {
	list, err := r.Todos.List(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, translate(err)
	}
	items := make([]*GetTodoListItem, len(list))
	for i, t := range list {
		items[i] = &GetTodoListItem{ID: t.ID.String(), Title: t.Title, DueBy: t.DueBy, IsComplete: t.IsComplete}
	}
	return &GetTodoListResponse{Todos: items}, nil
}

func (r *queryResolver) GetUserByID(ctx context.Context, in GetUserByIDRequest) (*GetUserByIDResponse, error) {
	userID, err := parseUserID(in.ID)
	if err != nil {
		return nil, err
	}
	u, err := r.Users.Get(ctx, userID)
	if err != nil {
		return nil, translate(err)
	}
	return &GetUserByIDResponse{ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, GivenName: u.GivenName, FamilyName: u.FamilyName, AvatarURL: u.AvatarURL}, nil
}

func (r *queryResolver) GetUserList(ctx context.Context, in GetUserListRequest) (*GetUserListResponse, error) {
	list, err := r.Users.List(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, translate(err)
	}
	items := make([]*GetUserListItem, len(list))
	for i, u := range list {
		items[i] = &GetUserListItem{ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, GivenName: u.GivenName, FamilyName: u.FamilyName, AvatarURL: u.AvatarURL}
	}
	return &GetUserListResponse{Users: items}, nil
}

// ---- Mutations ----

func (r *mutationResolver) CreateTodo(ctx context.Context, in CreateTodoRequest) (*CreateTodoResponse, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, errors.New("create todo reached without an authenticated principal")
	}
	t, err := r.Todos.Create(ctx, todos.CreateInput{OwnerID: p.UserID, Title: in.Title, DueBy: in.DueBy})
	if err != nil {
		return nil, translate(err)
	}
	return &CreateTodoResponse{ID: t.ID.String(), Title: t.Title, DueBy: t.DueBy, IsComplete: t.IsComplete}, nil
}

func (r *mutationResolver) UpdateTodo(ctx context.Context, in UpdateTodoRequest) (*UpdateTodoResponse, error) {
	todoID, err := parseTodoID(in.ID)
	if err != nil {
		return nil, err
	}
	t, err := r.Todos.Update(ctx, todos.UpdateInput{ID: todoID, Title: in.Title, DueBy: in.DueBy, IsComplete: in.IsComplete})
	if err != nil {
		return nil, translate(err)
	}
	return &UpdateTodoResponse{ID: t.ID.String(), Title: t.Title, DueBy: t.DueBy, IsComplete: t.IsComplete}, nil
}

func (r *mutationResolver) ToggleTodo(ctx context.Context, in ToggleTodoRequest) (*ToggleTodoResponse, error) {
	todoID, err := parseTodoID(in.ID)
	if err != nil {
		return nil, err
	}
	t, err := r.Todos.Toggle(ctx, todoID)
	if err != nil {
		return nil, translate(err)
	}
	return &ToggleTodoResponse{ID: t.ID.String(), Title: t.Title, DueBy: t.DueBy, IsComplete: t.IsComplete}, nil
}

func (r *mutationResolver) DeleteTodo(ctx context.Context, in DeleteTodoRequest) (*DeleteTodoResponse, error) {
	todoID, err := parseTodoID(in.ID)
	if err != nil {
		return nil, err
	}
	if err := r.Todos.Delete(ctx, todoID); err != nil {
		return nil, translate(err)
	}
	return &DeleteTodoResponse{ID: todoID.String()}, nil
}

func (r *mutationResolver) UpdateUser(ctx context.Context, in UpdateUserRequest) (*UpdateUserResponse, error) {
	userID, err := parseUserID(in.ID)
	if err != nil {
		return nil, err
	}
	u, err := r.Users.Update(ctx, users.UpdateInput{ID: userID, DisplayName: in.DisplayName, GivenName: in.GivenName, FamilyName: in.FamilyName, AvatarURL: in.AvatarURL})
	if err != nil {
		return nil, translate(err)
	}
	return &UpdateUserResponse{ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, GivenName: u.GivenName, FamilyName: u.FamilyName, AvatarURL: u.AvatarURL}, nil
}

func (r *mutationResolver) DeleteUser(ctx context.Context, in DeleteUserRequest) (*DeleteUserResponse, error) {
	userID, err := parseUserID(in.ID)
	if err != nil {
		return nil, err
	}
	if err := r.Users.Delete(ctx, userID); err != nil {
		return nil, translate(err)
	}
	return &DeleteUserResponse{ID: userID.String()}, nil
}

// ---- helpers ----

func parseTodoID(s string) (id.TodoID, error) {
	v, err := id.Parse[id.Todo](s)
	if err != nil {
		return v, badInput(map[string][]string{"id": {"id must be a valid UUID"}})
	}
	return v, nil
}

func parseUserID(s string) (id.UserID, error) {
	v, err := id.Parse[id.User](s)
	if err != nil {
		return v, badInput(map[string][]string{"id": {"id must be a valid UUID"}})
	}
	return v, nil
}

func badInput(errs map[string][]string) error {
	return &gqlerror.Error{Message: "The request violates the business rules.", Extensions: map[string]any{"code": "BAD_USER_INPUT", "errors": errs}}
}

// translate maps service errors to GraphQL errors; anything unrecognised is returned as-is
// and masked by the error presenter.
func translate(err error) error {
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return badInput(ve)
	case errors.Is(err, domain.ErrNotFound):
		return &gqlerror.Error{Message: "Not found.", Extensions: map[string]any{"code": "NOT_FOUND"}}
	default:
		return err
	}
}
