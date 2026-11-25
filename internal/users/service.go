// Package users holds the user business rules.
package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bjornnorgaard/golden-path-go/internal/db/store"
	"github.com/bjornnorgaard/golden-path-go/internal/domain"
	"github.com/bjornnorgaard/golden-path-go/internal/id"
	"github.com/bjornnorgaard/golden-path-go/internal/paging"
)

// User is the domain representation of a user profile.
type User struct {
	ID          id.UserID
	Email       string
	DisplayName *string
	GivenName   *string
	FamilyName  *string
	AvatarURL   *string
}

// Service implements the user use cases.
type Service struct {
	q      *store.Queries
	paging paging.Options
}

// NewService wires the service.
func NewService(pool *pgxpool.Pool, p paging.Options) *Service {
	return &Service{q: store.New(pool), paging: p}
}

// Get returns a user or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID id.UserID) (User, error) {
	row, err := s.q.GetUser(ctx, userID)
	if err != nil {
		return User{}, notFound(err, "get user")
	}
	return fromRow(row), nil
}

// List returns a page of users ordered by ID.
func (s *Service) List(ctx context.Context, limit, offset *int32) ([]User, error) {
	l, o, err := paging.Resolve(limit, offset, s.paging)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListUsers(ctx, store.ListUsersParams{Limit: l, Offset: o})
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

// UpdateInput is the input of Update. Email is managed by sign-in and cannot change.
type UpdateInput struct {
	ID          id.UserID
	DisplayName *string
	GivenName   *string
	FamilyName  *string
	AvatarURL   *string
}

// Update replaces a user's profile fields.
func (s *Service) Update(ctx context.Context, in UpdateInput) (User, error) {
	row, err := s.q.UpdateUser(ctx, store.UpdateUserParams{
		ID: in.ID, DisplayName: in.DisplayName, GivenName: in.GivenName, FamilyName: in.FamilyName, AvatarUrl: in.AvatarURL,
	})
	if err != nil {
		return User{}, notFound(err, "update user")
	}
	return fromRow(row), nil
}

// Delete removes a user and, by cascade, their todos.
func (s *Service) Delete(ctx context.Context, userID id.UserID) error {
	n, err := s.q.DeleteUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Provision creates the user for an email on first sign-in and returns the existing one afterwards.
func (s *Service) Provision(ctx context.Context, email string, displayName, givenName, familyName, avatarURL *string) (User, error) {
	row, err := s.q.UpsertUserByEmail(ctx, store.UpsertUserByEmailParams{
		ID: id.New[id.User](), Email: email, DisplayName: displayName, GivenName: givenName, FamilyName: familyName, AvatarUrl: avatarURL,
	})
	if err != nil {
		return User{}, fmt.Errorf("provision user: %w", err)
	}
	return fromRow(row), nil
}

func notFound(err error, op string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

func fromRow(r store.User) User {
	return User{ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, GivenName: r.GivenName, FamilyName: r.FamilyName, AvatarURL: r.AvatarUrl}
}
