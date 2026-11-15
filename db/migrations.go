// Package db embeds the database migration sources shipped with the application.
package db

import "embed"

// Migrations retains the migrations/*.sql paths expected by the versioned
// loader. SQL is compiled into the executable; runtime startup does not read
// migration files from the working directory.
//
//go:embed migrations/*.sql
var Migrations embed.FS
