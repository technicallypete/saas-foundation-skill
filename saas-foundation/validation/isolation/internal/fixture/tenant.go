package fixture

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tenant is the server-resolved context applied to a transaction. Empty
// fields are applied as empty settings, which RLS treats as "no context".
type Tenant struct {
	OrganizationID string
	UserID         string
	GroupID        string
}

// SetTenant applies tenant context to the current transaction only
// (set_config(..., is_local => true)), so it resets at COMMIT or ROLLBACK and
// can never leak to the next user of a pooled connection.
func SetTenant(ctx context.Context, tx pgx.Tx, tn Tenant) error {
	_, err := tx.Exec(ctx,
		`SELECT set_config('app.organization_id', $1, true),
		        set_config('app.user_id', $2, true),
		        set_config('app.group_id', $3, true)`,
		tn.OrganizationID, tn.UserID, tn.GroupID)
	return err
}

// WithTenantTx is the pattern consumers copy: begin, apply transaction-local
// context, run fn, then commit (or roll back on error).
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, tn Tenant, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := SetTenant(ctx, tx, tn); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InTenantRollback runs fn with tenant context and always rolls back, so
// tests leave the database unchanged.
func InTenantRollback(ctx context.Context, pool *pgxpool.Pool, tn Tenant, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := SetTenant(ctx, tx, tn); err != nil {
		return err
	}
	return fn(tx)
}
