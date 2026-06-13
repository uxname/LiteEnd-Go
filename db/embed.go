// Package db exposes embedded SQL migrations so they ship inside the binary
// (no goose CLI needed in the runtime image).
package db

import "embed"

// Migrations holds the goose-format migration files.
//
//go:embed migrations/*.sql
var Migrations embed.FS
