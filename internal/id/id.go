// Package id provides compile-time-distinct identifier types.
//
// ID[T] is a UUID tagged with a phantom type, so a TodoID can never be passed
// where a UserID is expected. It implements the interfaces needed by encoding/json
// (as a string), database/sql and pgx (as a uuid), so it can be used as-is in
// generated sqlc code (see internal/db/sqlc.yaml overrides).
package id

import (
	"database/sql/driver"
	"fmt"

	"github.com/google/uuid"
)

// ID is a UUID that is distinct per tag type T.
type ID[T any] uuid.UUID

// Tag types. Declaring a new identifier type is two lines: a tag and an alias.
type (
	Todo struct{}
	User struct{}

	TodoID = ID[Todo]
	UserID = ID[User]
)

// New returns a new time-ordered (UUIDv7) identifier.
func New[T any]() ID[T] {
	return ID[T](uuid.Must(uuid.NewV7()))
}

// Parse parses the canonical textual form of an identifier.
func Parse[T any](s string) (ID[T], error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ID[T]{}, fmt.Errorf("invalid id %q: %w", s, err)
	}
	return ID[T](u), nil
}

// UUID returns the identifier as a plain uuid.UUID, for use at transport boundaries.
func (i ID[T]) UUID() uuid.UUID { return uuid.UUID(i) }

// From wraps a plain uuid.UUID.
func From[T any](u uuid.UUID) ID[T] { return ID[T](u) }

// IsZero reports whether the identifier is the zero value.
func (i ID[T]) IsZero() bool { return i == ID[T]{} }

func (i ID[T]) String() string { return uuid.UUID(i).String() }

func (i ID[T]) MarshalText() ([]byte, error) { return uuid.UUID(i).MarshalText() }

func (i *ID[T]) UnmarshalText(b []byte) error {
	var u uuid.UUID
	if err := u.UnmarshalText(b); err != nil {
		return err
	}
	*i = ID[T](u)
	return nil
}

// Value implements driver.Valuer.
func (i ID[T]) Value() (driver.Value, error) { return uuid.UUID(i).String(), nil }

// Scan implements sql.Scanner.
func (i *ID[T]) Scan(src any) error {
	var u uuid.UUID
	if err := u.Scan(src); err != nil {
		return err
	}
	*i = ID[T](u)
	return nil
}
