package todosapi

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/domain"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/httpx"
	"github.com/bjornnorgaard/golden-path-go/internal/todos"
)

// Server adapts the todo service to the generated StrictServerInterface.
// It contains no business rules: it maps transport types to service calls and
// service errors to the responses the contract documents.
type Server struct {
	svc *todos.Service
}

// The compiler is the guide: adding an operation to the contract and regenerating
// breaks this assertion until the operation is implemented.
var _ StrictServerInterface = (*Server)(nil)

// NewServer creates the adapter.
func NewServer(svc *todos.Service) *Server { return &Server{svc: svc} }

// Mount registers the contract's routes on base. Every request is validated
// against the contract before it reaches an operation.
func Mount(base ServeMux, s *Server, h httpx.RESTHandlers) {
	spec, err := GetSwagger()
	if err != nil {
		panic("todosapi: embedded contract is invalid: " + err.Error()) // generated code; cannot happen at runtime
	}
	strict := NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  h.RequestError,
		ResponseErrorHandlerFunc: h.ResponseError,
	})
	var middlewares []MiddlewareFunc
	for _, v := range h.Validators(spec) {
		middlewares = append(middlewares, v)
	}
	HandlerWithOptions(strict, StdHTTPServerOptions{BaseRouter: base, Middlewares: middlewares})
}

func (s *Server) CreateTodo(ctx context.Context, req CreateTodoRequestObject) (CreateTodoResponseObject, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, errors.New("create todo reached without an authenticated principal")
	}
	t, err := s.svc.Create(ctx, todos.CreateInput{OwnerID: p.UserID, Title: req.Body.Title, DueBy: req.Body.DueBy})
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return CreateTodo400JSONResponse(ve), nil
	case err != nil:
		return nil, err
	}
	return CreateTodo200JSONResponse(toCreate(t)), nil
}

func (s *Server) GetTodoById(ctx context.Context, req GetTodoByIdRequestObject) (GetTodoByIdResponseObject, error) {
	todoID, bad := parseID(req.Body.Id)
	if bad != nil {
		return GetTodoById400JSONResponse(bad), nil
	}
	t, err := s.svc.Get(ctx, todoID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetTodoById404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return GetTodoById200JSONResponse(toGet(t)), nil
}

func (s *Server) GetTodoList(ctx context.Context, req GetTodoListRequestObject) (GetTodoListResponseObject, error) {
	list, err := s.svc.List(ctx, req.Body.Limit, req.Body.Offset)
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return GetTodoList400JSONResponse(ve), nil
	case err != nil:
		return nil, err
	}
	items := make([]GetTodoListItem, len(list))
	for i, t := range list {
		items[i] = GetTodoListItem(toGet(t))
	}
	return GetTodoList200JSONResponse{Todos: items}, nil
}

func (s *Server) UpdateTodo(ctx context.Context, req UpdateTodoRequestObject) (UpdateTodoResponseObject, error) {
	todoID, bad := parseID(req.Body.Id)
	if bad != nil {
		return UpdateTodo400JSONResponse(bad), nil
	}
	t, err := s.svc.Update(ctx, todos.UpdateInput{ID: todoID, Title: req.Body.Title, DueBy: req.Body.DueBy, IsComplete: req.Body.IsComplete})
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return UpdateTodo400JSONResponse(ve), nil
	case errors.Is(err, domain.ErrNotFound):
		return UpdateTodo404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return UpdateTodo200JSONResponse(toGet(t)), nil
}

func (s *Server) ToggleTodo(ctx context.Context, req ToggleTodoRequestObject) (ToggleTodoResponseObject, error) {
	todoID, bad := parseID(req.Body.Id)
	if bad != nil {
		return ToggleTodo400JSONResponse(bad), nil
	}
	t, err := s.svc.Toggle(ctx, todoID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return ToggleTodo404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return ToggleTodo200JSONResponse(toGet(t)), nil
}

func (s *Server) DeleteTodo(ctx context.Context, req DeleteTodoRequestObject) (DeleteTodoResponseObject, error) {
	todoID, bad := parseID(req.Body.Id)
	if bad != nil {
		return DeleteTodo400JSONResponse(bad), nil
	}
	err := s.svc.Delete(ctx, todoID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return DeleteTodo404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return DeleteTodo200JSONResponse{Id: todoID.UUID()}, nil
}

const notFoundText = "No todo exists with the supplied ID."

func parseID(s string) (id.TodoID, ValidationErrors) {
	parsed, err := id.Parse[id.Todo](s)
	if err != nil {
		return id.TodoID{}, ValidationErrors{"id": {"id must be a valid UUID"}}
	}
	return parsed, nil
}

func toGet(t todos.Todo) GetTodoByIdResponse {
	return GetTodoByIdResponse{Id: uuid.UUID(t.ID), Title: t.Title, DueBy: t.DueBy, IsComplete: t.IsComplete}
}

func toCreate(t todos.Todo) CreateTodoResponse {
	return CreateTodoResponse(toGet(t))
}
