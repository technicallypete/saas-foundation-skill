package fixture

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Env is the fixture environment supplied by validation/run.sh.
type Env struct {
	Host              string
	Port              int
	Database          string
	SuperuserPassword string
	MigratorPassword  string
	RuntimePassword   string
	RetentionPassword string
}

// Role names derive from the database name exactly as the rendered
// infra/postgres/init/10-roles.sh derives them.
func (e Env) OwnerRole() string        { return e.Database + "_owner" }
func (e Env) MigratorRole() string     { return e.Database + "_migrator" }
func (e Env) RuntimeRole() string      { return e.Database + "_runtime" }
func (e Env) RetentionRole() string    { return e.Database + "_retention" }
func (e Env) PolicyReaderRole() string { return e.Database + "_policy_reader" }

// integrationRequired is set by the harness: missing integration settings
// then fail tests instead of skipping them.
func integrationRequired() bool { return os.Getenv("ARCHETYPE_REQUIRE_INTEGRATION") == "1" }

// LoadEnv returns the database environment. Without it, tests skip unless
// the harness requires integration, in which case they fail.
func LoadEnv(t testing.TB) Env {
	t.Helper()
	e := Env{
		Host:              os.Getenv("ARCHETYPE_PG_HOST"),
		Database:          os.Getenv("ARCHETYPE_DB_NAME"),
		SuperuserPassword: os.Getenv("ARCHETYPE_PG_SUPERUSER_PASSWORD"),
		MigratorPassword:  os.Getenv("ARCHETYPE_DB_MIGRATOR_PASSWORD"),
		RuntimePassword:   os.Getenv("ARCHETYPE_DB_RUNTIME_PASSWORD"),
		RetentionPassword: os.Getenv("ARCHETYPE_DB_RETENTION_PASSWORD"),
		Port:              5432,
	}
	if p := os.Getenv("ARCHETYPE_PG_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("ARCHETYPE_PG_PORT: %v", err)
		}
		e.Port = n
	}
	if e.Host == "" || e.Database == "" || e.SuperuserPassword == "" || e.MigratorPassword == "" || e.RuntimePassword == "" || e.RetentionPassword == "" {
		if integrationRequired() {
			t.Fatal("database integration settings are missing but ARCHETYPE_REQUIRE_INTEGRATION=1")
		}
		t.Skip("database integration settings not provided (set by validation/run.sh)")
	}
	return e
}

func (e Env) poolConfig(user, password string, maxConns int32) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Host = e.Host
	cfg.ConnConfig.Port = uint16(e.Port)
	cfg.ConnConfig.Database = e.Database
	cfg.ConnConfig.User = user
	cfg.ConnConfig.Password = password
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = "archetype-isolation"
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	return cfg, nil
}

// identity is what a guard observes about a connection.
type identity struct {
	SessionUser, CurrentUser               string
	Super, BypassRLS, CreateRole, CreateDB bool
}

func observe(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (identity, error) {
	var id identity
	err := q.QueryRow(ctx, `
		SELECT session_user::text, current_user::text, r.rolsuper, r.rolbypassrls, r.rolcreaterole, r.rolcreatedb
		FROM pg_roles r WHERE r.rolname = current_user`).
		Scan(&id.SessionUser, &id.CurrentUser, &id.Super, &id.BypassRLS, &id.CreateRole, &id.CreateDB)
	return id, err
}

func memberOf(ctx context.Context, conn *pgx.Conn, role string) (bool, error) {
	var exists, member bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	err := conn.QueryRow(ctx, `SELECT pg_has_role(current_user, $1, 'MEMBER')`, role).Scan(&member)
	return member, err
}

// RuntimeDB returns a pool for the application runtime role. Every acquire
// re-verifies the identity: session_user = current_user = <db>_runtime, no
// superuser, BYPASSRLS, CREATEROLE or CREATEDB, and no membership in the
// owner, migrator, retention or policy-reader roles. A violation fails the
// acquire, so no runtime assertion can run on a privileged connection.
func RuntimeDB(t testing.TB) *pgxpool.Pool { return runtimePool(t, 0) }

// RuntimeDBSingle is RuntimeDB with exactly one connection, for
// pooled-connection reuse tests.
func RuntimeDBSingle(t testing.TB) *pgxpool.Pool { return runtimePool(t, 1) }

func runtimePool(t testing.TB, maxConns int32) *pgxpool.Pool {
	t.Helper()
	e := LoadEnv(t)
	cfg, err := e.poolConfig(e.RuntimeRole(), e.RuntimePassword, maxConns)
	if err != nil {
		t.Fatal(err)
	}
	privileged := []string{e.OwnerRole(), e.MigratorRole(), e.RetentionRole(), e.PolicyReaderRole()}
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		id, err := observe(ctx, conn)
		if err != nil {
			return false, fmt.Errorf("runtime guard: %w", err)
		}
		if id.SessionUser != e.RuntimeRole() || id.CurrentUser != e.RuntimeRole() {
			return false, fmt.Errorf("runtime guard: connected as session_user=%s current_user=%s, want %s", id.SessionUser, id.CurrentUser, e.RuntimeRole())
		}
		if id.Super || id.BypassRLS || id.CreateRole || id.CreateDB {
			return false, fmt.Errorf("runtime guard: %s has a privileged attribute (super=%t bypassrls=%t createrole=%t createdb=%t)", id.CurrentUser, id.Super, id.BypassRLS, id.CreateRole, id.CreateDB)
		}
		for _, r := range privileged {
			m, err := memberOf(ctx, conn, r)
			if err != nil {
				return false, fmt.Errorf("runtime guard: %w", err)
			}
			if m {
				return false, fmt.Errorf("runtime guard: %s is a member of %s", id.CurrentUser, r)
			}
		}
		return true, nil
	}
	return newPool(t, cfg)
}

// RetentionDB returns a pool for the audit-retention role, guarded on every
// acquire: current_user = <db>_retention, not superuser or BYPASSRLS, and
// not a member of the owner or runtime roles.
func RetentionDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	e := LoadEnv(t)
	cfg, err := e.poolConfig(e.RetentionRole(), e.RetentionPassword, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		id, err := observe(ctx, conn)
		if err != nil {
			return false, fmt.Errorf("retention guard: %w", err)
		}
		if id.CurrentUser != e.RetentionRole() || id.Super || id.BypassRLS {
			return false, fmt.Errorf("retention guard: unexpected identity %+v", id)
		}
		for _, r := range []string{e.OwnerRole(), e.RuntimeRole()} {
			m, err := memberOf(ctx, conn, r)
			if err != nil {
				return false, fmt.Errorf("retention guard: %w", err)
			}
			if m {
				return false, fmt.Errorf("retention guard: %s is a member of %s", id.CurrentUser, r)
			}
		}
		return true, nil
	}
	return newPool(t, cfg)
}

// SuperuserSetup returns a pool for the image's init superuser. It asserts
// rolsuper = true so it can never be mistaken for a test connection. It may
// be used only by seed helpers and TestControl_* tests
// (enforced by TestGuard_ConnectionScope).
func SuperuserSetup(t testing.TB) *pgxpool.Pool {
	t.Helper()
	e := LoadEnv(t)
	cfg, err := e.poolConfig("postgres", e.SuperuserPassword, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		id, err := observe(ctx, conn)
		if err != nil {
			return false, err
		}
		if !id.Super {
			return false, fmt.Errorf("superuser guard: %s is not a superuser", id.CurrentUser)
		}
		return true, nil
	}
	return newPool(t, cfg)
}

// OwnerTx runs fn inside a transaction on the migrator login with
// SET LOCAL ROLE <db>_owner, after verifying session_user = <db>_migrator,
// current_user = <db>_owner, and that neither role is superuser or
// BYPASSRLS. The transaction is always rolled back.
func OwnerTx(t testing.TB, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	e := LoadEnv(t)
	cfg, err := e.poolConfig(e.MigratorRole(), e.MigratorPassword, 1)
	if err != nil {
		t.Fatal(err)
	}
	pool := newPool(t, cfg)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("owner tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{e.OwnerRole()}.Sanitize()); err != nil {
		t.Fatalf("owner tx: SET LOCAL ROLE: %v", err)
	}
	id, err := observe(ctx, tx)
	if err != nil {
		t.Fatalf("owner guard: %v", err)
	}
	if id.SessionUser != e.MigratorRole() || id.CurrentUser != e.OwnerRole() || id.Super || id.BypassRLS {
		t.Fatalf("owner guard: unexpected identity %+v", id)
	}
	var migratorPrivileged bool
	if err := tx.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = $1`, e.MigratorRole()).Scan(&migratorPrivileged); err != nil || migratorPrivileged {
		t.Fatalf("owner guard: migrator role is privileged or unreadable (err=%v)", err)
	}
	fn(ctx, tx)
}

func newPool(t testing.TB, cfg *pgxpool.Config) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("pool %s: %v", cfg.ConnConfig.User, err)
	}
	t.Cleanup(pool.Close)
	return pool
}
