package gorn

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrMigrationDrift       = errors.New("gorn: database schema changed; generate a new migration plan")
	ErrMigrationPlanChanged = errors.New("gorn: migration plan was modified or was not created by PlanMigration")
	ErrMigrationBusy        = errors.New("gorn: another migration holds the database migration lock")
	ErrUnsafeMigration      = errors.New("gorn: migration requires explicit permission for an unsafe change")
)

// MigrationTable identifies one managed table. Model uses rnsql/rntype/rnopt/FK
// tags, with optional rndefault, FKOnDelete and FKOnUpdate tags.
type MigrationTable struct {
	Name  string
	Model interface{}
}

// MigrationOptions defaults to preserving objects absent from the input.
// Permissions affect only explicitly supplied tables, never the entire schema.
type MigrationOptions struct {
	AllowDropColumns     bool
	AllowDropIndexes     bool
	AllowDropForeignKeys bool
	AllowUnsafeChanges   bool
	// Algorithm and Lock are MySQL ALTER TABLE clauses, e.g. INSTANT or
	// INPLACE/NONE. Unsupported combinations fail; there is no silent fallback.
	Algorithm string
	Lock      string
	// LockWaitTimeout bounds MySQL metadata-lock waits (default: 5 seconds).
	// Context cancellation additionally bounds the entire operation.
	LockWaitTimeout time.Duration
}

type MigrationStep struct {
	Table       string `json:"table"`
	SQL         string `json:"sql"`
	Reason      string `json:"reason"`
	Destructive bool   `json:"destructive"`
}

// MigrationPlan is a read-only preview. Steps can be displayed or JSON encoded,
// but must not be edited. ApplyMigration accepts the original in-memory plan,
// not deserialized SQL. A plan is bound to the server, schema and schema state.
type MigrationPlan struct {
	Schema            string          `json:"schema"`
	ServerVersion     string          `json:"server_version"`
	SchemaFingerprint string          `json:"schema_fingerprint"`
	Steps             []MigrationStep `json:"steps"`
	Warnings          []string        `json:"warnings,omitempty"`
	options           MigrationOptions
	mode, seal        string
}

// SQL renders the planned DDL without executing it.
func (p *MigrationPlan) SQL() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	for _, step := range p.Steps {
		b.WriteString(step.SQL)
		b.WriteString(";\n")
	}
	return b.String()
}

func (p *MigrationPlan) signature() string {
	b, _ := json.Marshal(struct {
		Plan    *MigrationPlan
		Options MigrationOptions
		Mode    string
	}{p, p.options, p.mode})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MigrationApplyError reports partial progress. MySQL DDL is atomic per
// statement, NOT across the whole plan. A disconnected client can have an
// unknown outcome for the failed statement; always inspect and re-plan.
type MigrationApplyError struct {
	Step    int // zero-based failed step
	Applied int // statements whose success was acknowledged
	SQL     string
	Err     error
}

func (e *MigrationApplyError) Error() string {
	return fmt.Sprintf("gorn: migration step %d failed after %d acknowledged steps: %v", e.Step+1, e.Applied, e.Err)
}
func (e *MigrationApplyError) Unwrap() error { return e.Err }

func migrationOptions(opts []MigrationOptions) (MigrationOptions, error) {
	if len(opts) > 1 {
		return MigrationOptions{}, fmt.Errorf("gorn: supply at most one MigrationOptions")
	}
	var o MigrationOptions
	if len(opts) == 1 {
		o = opts[0]
	}
	o.Algorithm = strings.ToUpper(o.Algorithm)
	o.Lock = strings.ToUpper(o.Lock)
	if !oneOf(o.Algorithm, "", "DEFAULT", "INSTANT", "INPLACE", "COPY") || !oneOf(o.Lock, "", "DEFAULT", "NONE", "SHARED", "EXCLUSIVE") {
		return o, fmt.Errorf("gorn: invalid migration algorithm or lock")
	}
	if o.Algorithm == "INSTANT" && !oneOf(o.Lock, "", "DEFAULT") {
		return o, fmt.Errorf("gorn: INSTANT requires LOCK=DEFAULT")
	}
	if o.LockWaitTimeout == 0 {
		o.LockWaitTimeout = 5 * time.Second
	}
	if o.LockWaitTimeout < time.Second || o.LockWaitTimeout > 24*time.Hour {
		return o, fmt.Errorf("gorn: LockWaitTimeout must be between 1 second and 24 hours")
	}
	return o, nil
}

func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}

func (d *DB) migrationConnection(ctx context.Context) (*sql.Conn, error) {
	if d == nil || d.h == nil || d.h.DB == nil || d.conf == nil {
		return nil, fmt.Errorf("gorn: migration requires an open database")
	}
	if d.Engine != "mysql" {
		return nil, fmt.Errorf("gorn: migration supports MySQL only")
	}
	if d.isTransaction {
		return nil, fmt.Errorf("gorn: MySQL migrations cannot run in a transaction (DDL implicitly commits)")
	}
	return d.h.DB.Conn(ctx)
}

// PlanMigration is a dry run: it executes SELECT/SHOW only, with no DDL, DML,
// advisory lock, session-variable writes or migration-history table creation.
// All models are planned together so new tables can reference each other,
// including cycles, without disabling foreign-key checks.
func (d *DB) PlanMigration(ctx context.Context, tables []MigrationTable, indexes []*DBIndex, opts ...MigrationOptions) (*MigrationPlan, error) {
	o, err := migrationOptions(opts)
	if err != nil {
		return nil, err
	}
	conn, err := d.migrationConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	s, err := readMigrationSchema(ctx, conn, d.conf.Schema)
	if err != nil {
		return nil, err
	}
	p, err := buildMigrationPlan(s, tables, indexes, o)
	if err != nil {
		return nil, err
	}
	// Catch concurrent schema changes during the multi-query inspection.
	check, err := readMigrationSchema(ctx, conn, d.conf.Schema)
	if err != nil {
		return nil, err
	}
	if check.fingerprint() != p.SchemaFingerprint {
		return nil, ErrMigrationDrift
	}
	p.seal = p.signature()
	return p, nil
}

func migrationLockName(schema string) string {
	h := sha256.Sum256([]byte(schema))
	return "gorn:migrate:" + hex.EncodeToString(h[:24])
}

// ApplyMigration executes exactly the previewed steps on one pinned connection.
// A schema-wide advisory lock serializes cooperating gorn migrators. External
// DDL tools must coordinate using the same lock; application DML remains live.
func (d *DB) ApplyMigration(ctx context.Context, plan *MigrationPlan) (retErr error) {
	if plan == nil || plan.seal == "" || plan.signature() != plan.seal {
		return ErrMigrationPlanChanged
	}
	conn, err := d.migrationConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var originalMode string
	var originalFK, originalWait int64
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode, @@SESSION.foreign_key_checks, @@SESSION.lock_wait_timeout").Scan(&originalMode, &originalFK, &originalWait); err != nil {
		return err
	}
	lockName := migrationLockName(plan.Schema)
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&acquired); err != nil {
		return err
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return ErrMigrationBusy
	}
	defer func() {
		// A canceled caller must not prevent cleanup. Discard the connection if
		// settings or the lock cannot be restored; never return a dirty session.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, restoreErr := conn.ExecContext(cleanupCtx, "SET SESSION sql_mode = ?, foreign_key_checks = ?, lock_wait_timeout = ?", originalMode, originalFK, originalWait)
		var released sql.NullInt64
		releaseErr := conn.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
		if releaseErr == nil && (!released.Valid || released.Int64 != 1) {
			releaseErr = fmt.Errorf("gorn: migration lock was lost")
		}
		if cleanupErr := errors.Join(restoreErr, releaseErr); cleanupErr != nil {
			_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
			retErr = errors.Join(retErr, fmt.Errorf("gorn: migration connection cleanup: %w", cleanupErr))
		}
	}()
	mode := plan.mode
	if !strings.Contains(mode, "STRICT_ALL_TABLES") {
		if mode != "" {
			mode += ","
		}
		mode += "STRICT_ALL_TABLES"
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode = ?, foreign_key_checks = 1, lock_wait_timeout = ?", mode, int64(plan.options.LockWaitTimeout/time.Second)); err != nil {
		return err
	}
	s, err := readMigrationSchema(ctx, conn, d.conf.Schema)
	if err != nil {
		return err
	}
	if s.Name != plan.Schema || s.fingerprint() != plan.SchemaFingerprint {
		return ErrMigrationDrift
	}
	for i, step := range plan.Steps {
		if _, err := conn.ExecContext(ctx, step.SQL); err != nil {
			return &MigrationApplyError{Step: i, Applied: i, SQL: step.SQL, Err: err}
		}
	}
	return nil
}

// Migrate plans and applies a migration. Use PlanMigration + ApplyMigration when
// a human or another policy needs to inspect the exact SQL before execution.
func (d *DB) Migrate(ctx context.Context, tables []MigrationTable, indexes []*DBIndex, opts ...MigrationOptions) (*MigrationPlan, error) {
	p, err := d.PlanMigration(ctx, tables, indexes, opts...)
	if err != nil {
		return nil, err
	}
	return p, d.ApplyMigration(ctx, p)
}
