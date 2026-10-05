package rls

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"example.invalid/archetype/isolation/internal/fixture"
)

// TestMain runs a preflight before any case (Appendix A case 1):
//   - the four logins (runtime, migrator, retention, init superuser) are
//     configured and resolve to four distinct roles with the expected names;
//   - the runtime login passes the fixture.RuntimeDB guard.
//
// Without integration settings the preflight is skipped and every database
// test skips itself through fixture.LoadEnv (or fails when the harness sets
// ARCHETYPE_REQUIRE_INTEGRATION=1).
func TestMain(m *testing.M) {
	if err := preflight(); err != nil {
		fmt.Fprintf(os.Stderr, "rls preflight failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func preflight() error {
	tb := &preflightTB{}
	env, ok, err := tb.run(func() any { return fixture.LoadEnv(tb) })
	if err != nil || !ok {
		return err // missing settings: tests decide skip vs fail
	}
	e := env.(fixture.Env)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Identity resolution only: each login runs one catalog query and is
	// closed. These are not assertion connections; every assertion uses a
	// guarded helper.
	logins := []struct{ user, password, wantRole string }{
		{e.RuntimeRole(), e.RuntimePassword, e.RuntimeRole()},
		{e.MigratorRole(), e.MigratorPassword, e.MigratorRole()},
		{e.RetentionRole(), e.RetentionPassword, e.RetentionRole()},
		{"postgres", e.SuperuserPassword, "postgres"},
	}
	seen := map[string]bool{}
	for _, l := range logins {
		role, err := resolveLogin(ctx, e, l.user, l.password)
		if err != nil {
			return fmt.Errorf("login %s: %w", l.user, err)
		}
		if role != l.wantRole {
			return fmt.Errorf("login %s resolved to role %s, want %s", l.user, role, l.wantRole)
		}
		seen[role] = true
	}
	if len(seen) != 4 {
		return fmt.Errorf("the four logins resolve to %d distinct roles, want 4", len(seen))
	}

	// The runtime login must pass the RuntimeDB guard (run on acquire).
	defer tb.cleanup()
	_, _, err = tb.run(func() any {
		pool := fixture.RuntimeDB(tb)
		conn, err := pool.Acquire(ctx)
		if err != nil {
			tb.Fatalf("runtime guard rejected the runtime login: %v", err)
		}
		conn.Release()
		return nil
	})
	return err
}

func resolveLogin(ctx context.Context, e fixture.Env, user, password string) (string, error) {
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		return "", err
	}
	cfg.Host, cfg.Port, cfg.Database = e.Host, uint16(e.Port), e.Database
	cfg.User, cfg.Password = user, password
	cfg.ConnectTimeout = 5 * time.Second
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var sessionUser, currentUser string
	if err := conn.QueryRow(ctx, `SELECT session_user::text, current_user::text`).Scan(&sessionUser, &currentUser); err != nil {
		return "", err
	}
	if sessionUser != currentUser {
		return "", fmt.Errorf("session_user %s differs from current_user %s", sessionUser, currentUser)
	}
	return currentUser, nil
}

// preflightTB adapts the fixture helpers (which take a testing.TB) to
// TestMain, where no *testing.T exists. Only the methods the helpers call
// are implemented; Fatal and Skip unwind through a sentinel panic.
type preflightTB struct {
	testing.TB
	cleanups []func()
	err      error
	skipped  bool
}

type preflightAbort struct{}

func (p *preflightTB) run(fn func() any) (v any, ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, isAbort := r.(preflightAbort); !isAbort {
				panic(r)
			}
			v, ok, err = nil, false, p.err
		}
	}()
	return fn(), true, nil
}

func (p *preflightTB) cleanup() {
	for i := len(p.cleanups) - 1; i >= 0; i-- {
		p.cleanups[i]()
	}
}

func (p *preflightTB) Helper()                      {}
func (p *preflightTB) Name() string                 { return "TestMain/preflight" }
func (p *preflightTB) Cleanup(f func())             { p.cleanups = append(p.cleanups, f) }
func (p *preflightTB) Logf(string, ...any)          {}
func (p *preflightTB) Log(...any)                   {}
func (p *preflightTB) Fatal(args ...any)            { p.fail(errors.New(fmt.Sprint(args...))) }
func (p *preflightTB) Fatalf(f string, args ...any) { p.fail(fmt.Errorf(f, args...)) }
func (p *preflightTB) Errorf(f string, args ...any) { p.fail(fmt.Errorf(f, args...)) }
func (p *preflightTB) Skip(...any)                  { p.skipped = true; panic(preflightAbort{}) }
func (p *preflightTB) Skipf(string, ...any)         { p.skipped = true; panic(preflightAbort{}) }
func (p *preflightTB) fail(err error)               { p.err = err; panic(preflightAbort{}) }
