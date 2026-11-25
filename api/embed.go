// Package api embeds the hand-written API contracts so they can be served
// (docs UI) and used for request validation at runtime.
package api

import "embed"

// Contracts holds every OpenAPI and GraphQL contract file.
//
//go:embed openapi/*.yaml graphql/*.graphql
var Contracts embed.FS
