// Package db owns the database contract: SQL migrations and queries are the
// hand-written source of truth, and typed Go code is generated from them.
package db

//go:generate go tool sqlc generate
