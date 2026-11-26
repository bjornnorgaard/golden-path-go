package grpcapi

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bjornnorgaard/golden-path-go/internal/auth"
	"github.com/bjornnorgaard/golden-path-go/internal/domain"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	todosv1 "github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi/gen/goldenpath/todos/v1"
	"github.com/bjornnorgaard/golden-path-go/internal/todos"
)

// todosServer adapts the todo service to the generated TodosServiceServer. It maps
// types only; errors are translated by the interceptor chain (see translateErrors).
type todosServer struct{ svc *todos.Service }

// The compiler is the guide: adding an RPC to the contract and regenerating breaks
// this assertion until the RPC is implemented.
var _ todosv1.TodosServiceServer = (*todosServer)(nil)

func (s *todosServer) CreateTodo(ctx context.Context, req *todosv1.CreateTodoRequest) (*todosv1.CreateTodoResponse, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, errors.New("create todo reached without an authenticated principal")
	}
	dueBy, err := optionalTime(req.GetDueBy())
	if err != nil {
		return nil, err
	}
	t, err := s.svc.Create(ctx, todos.CreateInput{OwnerID: p.UserID, Title: req.GetTitle(), DueBy: dueBy})
	if err != nil {
		return nil, err
	}
	return &todosv1.CreateTodoResponse{Todo: toTodo(t)}, nil
}

func (s *todosServer) GetTodoById(ctx context.Context, req *todosv1.GetTodoByIdRequest) (*todosv1.GetTodoByIdResponse, error) {
	todoID, err := id.Parse[id.Todo](req.GetId())
	if err != nil {
		return nil, badID()
	}
	t, err := s.svc.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	return &todosv1.GetTodoByIdResponse{Todo: toTodo(t)}, nil
}

func (s *todosServer) GetTodoList(ctx context.Context, req *todosv1.GetTodoListRequest) (*todosv1.GetTodoListResponse, error) {
	list, err := s.svc.List(ctx, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	out := make([]*todosv1.Todo, len(list))
	for i, t := range list {
		out[i] = toTodo(t)
	}
	return &todosv1.GetTodoListResponse{Todos: out}, nil
}

func (s *todosServer) UpdateTodo(ctx context.Context, req *todosv1.UpdateTodoRequest) (*todosv1.UpdateTodoResponse, error) {
	todoID, err := id.Parse[id.Todo](req.GetId())
	if err != nil {
		return nil, badID()
	}
	dueBy, err := optionalTime(req.GetDueBy())
	if err != nil {
		return nil, err
	}
	t, err := s.svc.Update(ctx, todos.UpdateInput{ID: todoID, Title: req.GetTitle(), DueBy: dueBy, IsComplete: req.GetIsComplete()})
	if err != nil {
		return nil, err
	}
	return &todosv1.UpdateTodoResponse{Todo: toTodo(t)}, nil
}

func (s *todosServer) ToggleTodo(ctx context.Context, req *todosv1.ToggleTodoRequest) (*todosv1.ToggleTodoResponse, error) {
	todoID, err := id.Parse[id.Todo](req.GetId())
	if err != nil {
		return nil, badID()
	}
	t, err := s.svc.Toggle(ctx, todoID)
	if err != nil {
		return nil, err
	}
	return &todosv1.ToggleTodoResponse{Todo: toTodo(t)}, nil
}

func (s *todosServer) DeleteTodo(ctx context.Context, req *todosv1.DeleteTodoRequest) (*todosv1.DeleteTodoResponse, error) {
	todoID, err := id.Parse[id.Todo](req.GetId())
	if err != nil {
		return nil, badID()
	}
	if err := s.svc.Delete(ctx, todoID); err != nil {
		return nil, err
	}
	return &todosv1.DeleteTodoResponse{Id: todoID.String()}, nil
}

func toTodo(t todos.Todo) *todosv1.Todo {
	out := &todosv1.Todo{Id: t.ID.String(), Title: t.Title, IsComplete: t.IsComplete}
	if t.DueBy != nil {
		out.DueBy = timestamppb.New(*t.DueBy)
	}
	return out
}

// optionalTime converts an absent or valid timestamp; an out-of-range one is a validation error.
func optionalTime(ts *timestamppb.Timestamp) (*time.Time, error) {
	if ts == nil {
		return nil, nil //nolint:nilnil // nil means "no due date"
	}
	if err := ts.CheckValid(); err != nil {
		return nil, domain.ValidationError{"dueBy": {"dueBy must be a valid timestamp"}}
	}
	t := ts.AsTime()
	return &t, nil
}
