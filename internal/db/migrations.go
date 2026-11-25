package db

import "embed"

// Migrations holds the embedded goose SQL migrations.
//
//go:embed migrations/*.sql
var Migrations embed.FS
