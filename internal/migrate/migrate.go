// Package migrate owns the database schema: the migration files, embedded so
// the binary carries them, and the runner that applies them at startup.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations
var migrationFiles embed.FS

// Two independent sets, each with its own migrations table. The session
// schema is copied from the session module and keeps its own numbering, so
// upgrading that module stays a matter of copying files in rather than
// renumbering everything after them.
var migrationSets = []struct {
	dir   string
	table string
}{
	{dir: "migrations/session", table: "session_schema_migrations"},
	{dir: "migrations/app", table: "schema_migrations"},
}

// minimumPostgres is a hard floor, not a preference: the session package's
// "SessionUsers" table uses casefold(), which does not exist before
// PostgreSQL 18. Checking here turns a confusing mid-migration syntax error
// into one sentence at startup.
const minimumPostgres = 18

func checkPostgresVersion(ctx context.Context, databaseURL string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to check the server version: %w", err)
	}
	defer conn.Close(ctx)

	// SHOW returns text whatever the setting holds, so this parses rather
	// than scanning straight into an int.
	var raw string
	if err := conn.QueryRow(ctx, "SHOW server_version_num").Scan(&raw); err != nil {
		return fmt.Errorf("read the server version: %w", err)
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("parse the server version %q: %w", raw, err)
	}
	if major := version / 10000; major < minimumPostgres {
		return fmt.Errorf("PostgreSQL %d or newer is required, this server is %d: "+
			"the session schema uses casefold()", minimumPostgres, major)
	}
	return nil
}

// Run brings every migration set up to date. It is safe to run on every
// start: sets already at their newest version are no-ops.
func Run(databaseURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := checkPostgresVersion(ctx, databaseURL); err != nil {
		return err
	}
	for _, set := range migrationSets {
		if err := runMigrations(databaseURL, set.dir, set.table); err != nil {
			return fmt.Errorf("migrate %s: %w", set.dir, err)
		}
	}
	return nil
}

func runMigrations(databaseURL, dir, table string) error {
	source, err := iofs.New(migrationFiles, dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	target, err := migrationURL(databaseURL, table)
	if err != nil {
		return err
	}
	migrator, err := migrate.NewWithSourceInstance("iofs", source, target)
	if err != nil {
		return fmt.Errorf("open migrator: %w", err)
	}
	defer migrator.Close()

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// migrationURL rewrites the connection string for golang-migrate, which
// selects its driver by scheme, and points it at this set's migrations table.
func migrationURL(databaseURL, table string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	parsed.Scheme = "pgx5"
	query := parsed.Query()
	query.Set("x-migrations-table", table)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
