package grpcapi

import (
	"context"

	"github.com/bjornnorgaard/golden-path-go/internal/id"
	usersv1 "github.com/bjornnorgaard/golden-path-go/internal/transport/grpcapi/gen/goldenpath/users/v1"
	"github.com/bjornnorgaard/golden-path-go/internal/users"
)

// usersServer adapts the user service to the generated UsersServiceServer.
type usersServer struct{ svc *users.Service }

var _ usersv1.UsersServiceServer = (*usersServer)(nil)

func (s *usersServer) GetUserById(ctx context.Context, req *usersv1.GetUserByIdRequest) (*usersv1.GetUserByIdResponse, error) {
	userID, err := id.Parse[id.User](req.GetId())
	if err != nil {
		return nil, badID()
	}
	u, err := s.svc.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &usersv1.GetUserByIdResponse{User: toUser(u)}, nil
}

func (s *usersServer) GetUserList(ctx context.Context, req *usersv1.GetUserListRequest) (*usersv1.GetUserListResponse, error) {
	list, err := s.svc.List(ctx, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	out := make([]*usersv1.User, len(list))
	for i, u := range list {
		out[i] = toUser(u)
	}
	return &usersv1.GetUserListResponse{Users: out}, nil
}

func (s *usersServer) UpdateUser(ctx context.Context, req *usersv1.UpdateUserRequest) (*usersv1.UpdateUserResponse, error) {
	userID, err := id.Parse[id.User](req.GetId())
	if err != nil {
		return nil, badID()
	}
	u, err := s.svc.Update(ctx, users.UpdateInput{ID: userID, DisplayName: req.DisplayName, GivenName: req.GivenName, FamilyName: req.FamilyName, AvatarURL: req.AvatarUrl})
	if err != nil {
		return nil, err
	}
	return &usersv1.UpdateUserResponse{User: toUser(u)}, nil
}

func (s *usersServer) DeleteUser(ctx context.Context, req *usersv1.DeleteUserRequest) (*usersv1.DeleteUserResponse, error) {
	userID, err := id.Parse[id.User](req.GetId())
	if err != nil {
		return nil, badID()
	}
	if err := s.svc.Delete(ctx, userID); err != nil {
		return nil, err
	}
	return &usersv1.DeleteUserResponse{Id: userID.String()}, nil
}

func toUser(u users.User) *usersv1.User {
	return &usersv1.User{Id: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName, GivenName: u.GivenName, FamilyName: u.FamilyName, AvatarUrl: u.AvatarURL}
}
