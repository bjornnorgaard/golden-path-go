package usersapi

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/bjornnorgaard/golden-path-go/internal/domain"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	"github.com/bjornnorgaard/golden-path-go/internal/platform/httpx"
	"github.com/bjornnorgaard/golden-path-go/internal/users"
)

// Server adapts the user service to the generated StrictServerInterface.
type Server struct {
	svc *users.Service
}

var _ StrictServerInterface = (*Server)(nil)

// NewServer creates the adapter.
func NewServer(svc *users.Service) *Server { return &Server{svc: svc} }

// Mount registers the contract's routes on base, validating requests against the contract.
func Mount(base ServeMux, s *Server, h httpx.RESTHandlers) {
	spec, err := GetSwagger()
	if err != nil {
		panic("usersapi: embedded contract is invalid: " + err.Error()) // generated code; cannot happen at runtime
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

const notFoundText = "No user exists with the supplied ID."

func (s *Server) GetUserById(ctx context.Context, req GetUserByIdRequestObject) (GetUserByIdResponseObject, error) {
	userID, bad := parseID(req.Body.Id)
	if bad != nil {
		return GetUserById400JSONResponse(bad), nil
	}
	u, err := s.svc.Get(ctx, userID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetUserById404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return GetUserById200JSONResponse(toGet(u)), nil
}

func (s *Server) GetUserList(ctx context.Context, req GetUserListRequestObject) (GetUserListResponseObject, error) {
	list, err := s.svc.List(ctx, req.Body.Limit, req.Body.Offset)
	var ve domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return GetUserList400JSONResponse(ve), nil
	case err != nil:
		return nil, err
	}
	items := make([]GetUserListItem, len(list))
	for i, u := range list {
		items[i] = GetUserListItem(toGet(u))
	}
	return GetUserList200JSONResponse{Users: items}, nil
}

func (s *Server) UpdateUser(ctx context.Context, req UpdateUserRequestObject) (UpdateUserResponseObject, error) {
	userID, bad := parseID(req.Body.Id)
	if bad != nil {
		return UpdateUser400JSONResponse(bad), nil
	}
	u, err := s.svc.Update(ctx, users.UpdateInput{
		ID: userID, DisplayName: req.Body.DisplayName, GivenName: req.Body.GivenName,
		FamilyName: req.Body.FamilyName, AvatarURL: req.Body.AvatarUrl,
	})
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return UpdateUser404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return UpdateUser200JSONResponse(toGet(u)), nil
}

func (s *Server) DeleteUser(ctx context.Context, req DeleteUserRequestObject) (DeleteUserResponseObject, error) {
	userID, bad := parseID(req.Body.Id)
	if bad != nil {
		return DeleteUser400JSONResponse(bad), nil
	}
	err := s.svc.Delete(ctx, userID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return DeleteUser404TextResponse(notFoundText), nil
	case err != nil:
		return nil, err
	}
	return DeleteUser200JSONResponse{Id: userID.UUID()}, nil
}

func parseID(s string) (id.UserID, ValidationErrors) {
	parsed, err := id.Parse[id.User](s)
	if err != nil {
		return id.UserID{}, ValidationErrors{"id": {"id must be a valid UUID"}}
	}
	return parsed, nil
}

func toGet(u users.User) GetUserByIdResponse {
	return GetUserByIdResponse{
		Id: uuid.UUID(u.ID), Email: u.Email, DisplayName: u.DisplayName,
		GivenName: u.GivenName, FamilyName: u.FamilyName, AvatarUrl: u.AvatarURL,
	}
}
