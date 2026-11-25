package gorn

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Integration tests require an explicitly selected, disposable LOOPBACK MySQL.
// Each test creates/drops only its own random database. No application config,
// production credentials or pre-existing database is used.
var migrationTestSequence atomic.Uint64

type migrationRecorder struct {
	mu        sync.Mutex
	execs     []string
	queries   []string
	failExec  func(string) error
	failQuery func(string) error
	failRows  func(string) error
}

func (r *migrationRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execs = nil
	r.queries = nil
	r.failExec = nil
	r.failQuery = nil
	r.failRows = nil
}
func (r *migrationRecorder) ddl() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, q := range r.execs {
		if isMigrationDDL(q) {
			out = append(out, q)
		}
	}
	return out
}
func isMigrationDDL(q string) bool {
	q = strings.ToUpper(strings.TrimSpace(q))
	return strings.HasPrefix(q, "ALTER ") || strings.HasPrefix(q, "CREATE ") || strings.HasPrefix(q, "DROP ")
}

type migrationTestConnector struct {
	driver.Connector
	r *migrationRecorder
}

func (c migrationTestConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &migrationTestConnection{Conn: conn, r: c.r}, nil
}

type migrationTestConnection struct {
	driver.Conn
	r *migrationRecorder
}

func (c *migrationTestConnection) Prepare(q string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(q)
	if err != nil {
		return nil, err
	}
	return &migrationTestStatement{Stmt: stmt, query: q, rec: c.r}, nil
}

func (c *migrationTestConnection) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	stmt, err := c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	return &migrationTestStatement{Stmt: stmt, query: q, rec: c.r}, nil
}

type migrationTestStatement struct {
	driver.Stmt
	query string
	rec   *migrationRecorder
}

func (s *migrationTestStatement) wrapRows(rows driver.Rows, err error) (driver.Rows, error) {
	s.rec.mu.Lock()
	hook := s.rec.failRows
	s.rec.mu.Unlock()
	if err == nil && hook != nil {
		if failure := hook(s.query); failure != nil {
			return &migrationFaultRows{Rows: rows, err: failure}, nil
		}
	}
	return rows, err
}
func (s *migrationTestStatement) Query(values []driver.Value) (driver.Rows, error) {
	return s.wrapRows(s.Stmt.Query(values))
}
func (s *migrationTestStatement) QueryContext(ctx context.Context, values []driver.NamedValue) (driver.Rows, error) {
	return s.wrapRows(s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, values))
}
func (s *migrationTestStatement) ExecContext(ctx context.Context, values []driver.NamedValue) (driver.Result, error) {
	return s.Stmt.(driver.StmtExecContext).ExecContext(ctx, values)
}

func (c *migrationTestConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.r.mu.Lock()
	c.r.execs = append(c.r.execs, q)
	hook := c.r.failExec
	c.r.mu.Unlock()
	if hook != nil {
		if err := hook(q); err != nil {
			return nil, err
		}
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *migrationTestConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.r.mu.Lock()
	c.r.queries = append(c.r.queries, q)
	hook := c.r.failQuery
	rowHook := c.r.failRows
	c.r.mu.Unlock()
	if hook != nil {
		if err := hook(q); err != nil {
			return nil, err
		}
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err == nil && rowHook != nil {
		if failure := rowHook(q); failure != nil {
			return &migrationFaultRows{Rows: rows, err: failure}, nil
		}
	}
	return rows, err
}

type migrationFaultRows struct {
	driver.Rows
	err error
}

func (r *migrationFaultRows) Next([]driver.Value) error { return r.err }
func (c *migrationTestConnection) CheckNamedValue(v *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(v)
	}
	return driver.ErrSkip
}
func (c *migrationTestConnection) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}
func (c *migrationTestConnection) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
func (c *migrationTestConnection) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}

type migrationTestDB struct {
	*DB
	rec  *migrationRecorder
	name string
}

func newMigrationTestDB(t *testing.T) *migrationTestDB {
	t.Helper()
	addr := os.Getenv("GORN_MYSQL_TEST_ADDR")
	if addr == "" {
		t.Skip("set GORN_MYSQL_TEST_ADDR to a disposable loopback MySQL server")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !oneOf(host, "127.0.0.1", "::1") {
		t.Fatalf("GORN_MYSQL_TEST_ADDR must be a numeric loopback address: %q", addr)
	}
	cfg := mysql.NewConfig()
	cfg.User = "root"
	cfg.Passwd = os.Getenv("GORN_MYSQL_TEST_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = addr
	cfg.ParseTime = true
	cfg.Timeout = 5 * time.Second
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("gorn_test_%d_%d", os.Getpid(), migrationTestSequence.Add(1))
	if _, err := admin.Exec("CREATE DATABASE " + quoteMigrationIdentifier(name) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE " + quoteMigrationIdentifier(name)); err != nil {
			t.Errorf("cleanup disposable database %s: %v", name, err)
		}
		admin.Close()
	})
	cfg.DBName = name
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := &migrationRecorder{}
	pool := sql.OpenDB(migrationTestConnector{connector, rec})
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(8)
	db := &DB{h: &DBHandler{DB: pool, Container: pool}, Engine: "mysql", conf: &DBConfig{Schema: name}}
	t.Cleanup(func() { pool.Close() })
	return &migrationTestDB{DB: db, rec: rec, name: name}
}
func (db *migrationTestDB) exec(t *testing.T, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.h.DB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
func (db *migrationTestDB) scalar(t *testing.T, q string, args ...interface{}) string {
	t.Helper()
	var s string
	if err := db.h.DB.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}
func (db *migrationTestDB) definition(t *testing.T, table string) string {
	t.Helper()
	var name, ddl string
	if err := db.h.DB.QueryRow("SHOW CREATE TABLE "+quoteMigrationIdentifier(table)).Scan(&name, &ddl); err != nil {
		t.Fatal(err)
	}
	return ddl
}
func (db *migrationTestDB) plan(t *testing.T, tables []MigrationTable, indexes []*DBIndex, opts ...MigrationOptions) *MigrationPlan {
	t.Helper()
	p, err := db.PlanMigration(context.Background(), tables, indexes, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (db *migrationTestDB) apply(t *testing.T, p *MigrationPlan) {
	t.Helper()
	if err := db.ApplyMigration(context.Background(), p); err != nil {
		t.Fatalf("apply %s: %v", p.SQL(), err)
	}
}
func (db *migrationTestDB) assertNoop(t *testing.T, tables []MigrationTable, indexes []*DBIndex, opts ...MigrationOptions) {
	t.Helper()
	db.rec.reset()
	p := db.plan(t, tables, indexes, opts...)
	if len(p.Steps) != 0 {
		t.Fatalf("expected empty repeat plan:\n%s", p.SQL())
	}
	db.apply(t, p)
	if ddl := db.rec.ddl(); len(ddl) != 0 {
		t.Fatalf("no-op executed DDL: %v", ddl)
	}
}
func migrationIndex(table, name, kind string, columns ...string) *DBIndex {
	idx := &DBIndex{TableName: table, IndexName: name, IndexType: kind}
	for _, c := range columns {
		idx.Columns = append(idx.Columns, &DBIndexColumn{ColumnName: c, ASC: true})
	}
	return idx
}

func TestMySQLMigrationDryRunAndNoop(t *testing.T) {
	db := newMigrationTestDB(t)
	type model struct {
		ID      int    `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
		Name    string `rnsql:"name" rntype:"VARCHAR(40)" rnopt:"NN" rndefault:"'initial'"`
		Enabled bool   `rnsql:"enabled" rntype:"BOOLEAN" rndefault:"TRUE"`
	}
	tables := []MigrationTable{{"people", &model{}}}
	indexes := []*DBIndex{migrationIndex("people", "name_idx", DBIndexTypeIndex, "name")}
	db.rec.reset()
	p := db.plan(t, tables, indexes)
	if len(p.Steps) != 1 {
		t.Fatalf("plan: %+v", p)
	}
	if len(db.rec.execs) != 0 {
		t.Fatalf("dry run executed writes: %v", db.rec.execs)
	}
	if db.scalar(t, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ?", db.name) != "0" {
		t.Fatal("dry run created objects")
	}
	db.apply(t, p)
	db.exec(t, "INSERT INTO people () VALUES ()")
	if db.scalar(t, "SELECT CONCAT(id, ':', name, ':', enabled) FROM people") != "1:initial:1" {
		t.Fatal("defaults / auto increment failed")
	}
	db.assertNoop(t, tables, indexes)
}

func TestMySQLMigrationOnlyChangedColumnPreservesData(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE items (id INT NOT NULL AUTO_INCREMENT PRIMARY KEY, name VARCHAR(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'untitled' COMMENT 'keep me', legacy INT DEFAULT 8, modified TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO items(name) VALUES ('hello'),('한글')")
	type model struct {
		ID   int    `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
		Name string `rnsql:"name" rntype:"VARCHAR(100)" rnopt:"NN"`
	}
	tables := []MigrationTable{{"items", model{}}}
	p := db.plan(t, tables, nil)
	if len(p.Steps) != 1 || strings.Count(p.SQL(), "MODIFY COLUMN") != 1 || strings.Contains(p.SQL(), "MODIFY COLUMN `id`") || strings.Contains(p.SQL(), "DROP") {
		t.Fatal(p.SQL())
	}
	db.apply(t, p)
	ddl := db.definition(t, "items")
	for _, attribute := range []string{"utf8mb4_bin", "DEFAULT 'untitled'", "COMMENT 'keep me'", "`legacy`", "ON UPDATE CURRENT_TIMESTAMP"} {
		if !strings.Contains(strings.ToUpper(ddl), strings.ToUpper(attribute)) {
			t.Fatalf("lost %s: %s", attribute, ddl)
		}
	}
	if db.scalar(t, "SELECT GROUP_CONCAT(CONCAT(id,':',name,':',legacy) ORDER BY id) FROM items") != "1:hello:8,2:한글:8" {
		t.Fatal("data changed")
	}
	db.exec(t, "INSERT INTO items () VALUES ()")
	if db.scalar(t, "SELECT id FROM items WHERE name='untitled'") != "3" {
		t.Fatal("auto increment changed")
	}
	db.assertNoop(t, tables, nil)
}

func TestMySQLMigrationAddColumnsWithData(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t VALUES(1),(2)")
	type model struct {
		ID       int    `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Optional *int   `rnsql:"optional" rntype:"INT"`
		Status   string `rnsql:"status" rntype:"VARCHAR(20)" rnopt:"NN" rndefault:"'new'"`
		Flag     bool   `rnsql:"flag" rntype:"BOOLEAN" rnopt:"NN" rndefault:"FALSE"`
	}
	tables := []MigrationTable{{"t", model{}}}
	p := db.plan(t, tables, nil)
	if len(p.Steps) != 1 || strings.Contains(p.SQL(), "MODIFY") {
		t.Fatal(p.SQL())
	}
	db.apply(t, p)
	if db.scalar(t, "SELECT COUNT(*) FROM t WHERE optional IS NULL AND status='new' AND flag=0") != "2" {
		t.Fatal("wrong backfill")
	}
	db.assertNoop(t, tables, nil)
}

func TestMySQLMigrationPreservesFieldOrderAndUnmanagedObjects(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY, extra INT, value VARCHAR(20), KEY extra_idx(extra)) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE other (id INT, KEY other_idx(id)) ENGINE=InnoDB")
	type model struct {
		Value string `rnsql:"value" rntype:"VARCHAR(20)"`
		ID    int    `rnsql:"id" rntype:"INTEGER" rnopt:"PK"`
	}
	db.assertNoop(t, []MigrationTable{{"t", model{}}}, nil)
	if !strings.Contains(db.definition(t, "other"), "other_idx") {
		t.Fatal("unmanaged index removed")
	}
	if got := db.scalar(t, "SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY ORDINAL_POSITION) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME='t'", db.name); got != "id,extra,value" {
		t.Fatal(got)
	}
}

func TestMySQLMigrationIndexDiffAndScope(t *testing.T) {
	db := newMigrationTestDB(t)
	for _, table := range []string{"a", "b", "c"} {
		db.exec(t, "CREATE TABLE "+table+" (id INT PRIMARY KEY, value VARCHAR(40), other INT, KEY same(value), KEY unmanaged(other)) ENGINE=InnoDB")
	}
	indexes, err := db.GetIndexes()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, idx := range indexes {
		counts[idx.TableName]++
		if len(idx.Columns) != 1 {
			t.Fatalf("cross-table index merged: %+v", idx)
		}
	}
	for _, n := range counts {
		if n != 2 {
			t.Fatal(counts)
		}
	}
	wanted := []*DBIndex{migrationIndex("b", "same", DBIndexTypeIndex, "other", "value")}
	p := db.plan(t, nil, wanted)
	if len(p.Steps) != 1 || p.Steps[0].Table != "b" || !strings.Contains(p.SQL(), "DROP INDEX `same`, ADD INDEX `same`") {
		t.Fatal(p.SQL())
	}
	db.apply(t, p)
	for _, table := range []string{"a", "c"} {
		if !strings.Contains(db.definition(t, table), "KEY `same` (`value`)") {
			t.Fatal(db.definition(t, table))
		}
	}
	if !strings.Contains(db.definition(t, "b"), "unmanaged") {
		t.Fatal("unmentioned index removed")
	}
	db.assertNoop(t, nil, wanted)
	db.assertNoop(t, nil, nil)
}

func TestMySQLMigrationUniqueFailureIsAtomic(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY, value INT, KEY same(value)) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t VALUES(1,7),(2,7)")
	before := db.definition(t, "t")
	p := db.plan(t, nil, []*DBIndex{migrationIndex("t", "same", DBIndexTypeUnique, "value")})
	err := db.ApplyMigration(context.Background(), p)
	var applyErr *MigrationApplyError
	if !errors.As(err, &applyErr) || applyErr.Applied != 0 {
		t.Fatalf("expected duplicate failure: %v", err)
	}
	if db.definition(t, "t") != before {
		t.Fatal("failed replacement removed original index")
	}
	if db.scalar(t, "SELECT COUNT(*) FROM t") != "2" {
		t.Fatal("data changed")
	}
	db.exec(t, "UPDATE t SET value=8 WHERE id=2")
	db.apply(t, p)
}

func TestMySQLMigrationForeignKeyOrderCyclesAndNoop(t *testing.T) {
	db := newMigrationTestDB(t)
	type a struct {
		ID int  `rnsql:"id" rntype:"INT" rnopt:"PK"`
		B  *int `rnsql:"b_id" rntype:"INT" FK:"z_parent.id"`
		B2 *int `rnsql:"b2_id" rntype:"INT" FK:"z_parent.id"`
	}
	type b struct {
		ID int  `rnsql:"id" rntype:"INT" rnopt:"PK"`
		A  *int `rnsql:"a_id" rntype:"INT" FK:"a_child.id"`
	}
	tables := []MigrationTable{{"z_parent", b{}}, {"a_child", a{}}}
	p := db.plan(t, tables, nil)
	if len(p.Steps) != 4 {
		t.Fatal(p.SQL())
	}
	db.apply(t, p)
	db.exec(t, "INSERT INTO z_parent(id) VALUES(1)")
	db.exec(t, "INSERT INTO a_child(id,b_id,b2_id) VALUES(2,1,1)")
	if _, err := db.h.DB.Exec("INSERT INTO a_child(id,b_id) VALUES(3,99)"); err == nil {
		t.Fatal("FK checks disabled")
	}
	db.assertNoop(t, tables, nil)
}

func TestMySQLMigrationForeignKeyPreservesNamesActionsAndBackingIndex(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE parent (id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE child (id INT PRIMARY KEY, parent_id INT, KEY shared(parent_id), CONSTRAINT legacy_fk FOREIGN KEY(parent_id) REFERENCES parent(id) ON DELETE CASCADE ON UPDATE CASCADE) ENGINE=InnoDB")
	type model struct {
		ID     int  `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Parent *int `rnsql:"parent_id" rntype:"INT" FK:"parent.id"`
	}
	db.assertNoop(t, []MigrationTable{{"child", model{}}}, nil)
	if err := db.DropForeignKey("child", "legacy_fk"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.definition(t, "child"), "KEY `shared`") {
		t.Fatal("supporting index dropped")
	}
	// A matching constraint/index name is also not permission to drop the index.
	db.exec(t, "ALTER TABLE child ADD CONSTRAINT shared FOREIGN KEY(parent_id) REFERENCES parent(id)")
	if err := db.DropForeignKey("child", "shared"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.definition(t, "child"), "KEY `shared`") {
		t.Fatal("same-named index dropped")
	}
}

func TestMySQLMigrationForeignKeyOrphansFailAndRecover(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE parent (id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE child (id INT PRIMARY KEY, parent_id INT) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO child VALUES(1,99)")
	type model struct {
		ID     int  `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Parent *int `rnsql:"parent_id" rntype:"INT" FK:"parent.id"`
	}
	tables := []MigrationTable{{"child", model{}}}
	p := db.plan(t, tables, nil)
	if err := db.ApplyMigration(context.Background(), p); err == nil {
		t.Fatal("orphan FK accepted")
	}
	db.exec(t, "INSERT INTO parent VALUES(99)")
	db.apply(t, p)
	db.assertNoop(t, tables, nil)
}

func TestMySQLMigrationForeignKeyReplacementFailurePreservesOldConstraint(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE p (id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE q (id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO p VALUES(1)")
	db.exec(t, "CREATE TABLE child (id INT PRIMARY KEY, ref INT, CONSTRAINT original FOREIGN KEY(ref) REFERENCES p(id)) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO child VALUES(1,1)")
	type model struct {
		ID  int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Ref int `rnsql:"ref" rntype:"INT" FK:"q.id"`
	}
	if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"child", model{}}}, nil); !errors.Is(err, ErrUnsafeMigration) {
		t.Fatal(err)
	}
	p := db.plan(t, []MigrationTable{{"child", model{}}}, nil, MigrationOptions{AllowUnsafeChanges: true})
	before := db.definition(t, "child")
	if err := db.ApplyMigration(context.Background(), p); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if db.definition(t, "child") != before {
		t.Fatal("old constraint lost")
	}
}

func TestMySQLMigrationDestructiveChangesRequirePermission(t *testing.T) {
	t.Run("drop_column", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY, extra VARCHAR(30)) ENGINE=InnoDB")
		db.exec(t, "INSERT INTO t VALUES(1,'keep')")
		type model struct {
			ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		}
		tables := []MigrationTable{{"t", model{}}}
		db.assertNoop(t, tables, nil)
		p := db.plan(t, tables, nil, MigrationOptions{AllowDropColumns: true})
		if len(p.Steps) != 1 || !p.Steps[0].Destructive {
			t.Fatal(p)
		}
		db.apply(t, p)
		if db.scalar(t, "SELECT id FROM t") != "1" {
			t.Fatal("row lost")
		}
		db.assertNoop(t, tables, nil, MigrationOptions{AllowDropColumns: true})
	})
	t.Run("narrowing", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (value VARCHAR(100)) ENGINE=InnoDB")
		db.exec(t, "INSERT INTO t VALUES('long value')")
		type model struct {
			Value string `rnsql:"value" rntype:"VARCHAR(3)"`
		}
		tables := []MigrationTable{{"t", model{}}}
		if _, err := db.PlanMigration(context.Background(), tables, nil); !errors.Is(err, ErrUnsafeMigration) {
			t.Fatal(err)
		}
		p := db.plan(t, tables, nil, MigrationOptions{AllowUnsafeChanges: true})
		before := db.definition(t, "t")
		if err := db.ApplyMigration(context.Background(), p); err == nil {
			t.Fatal("truncation succeeded")
		}
		if db.definition(t, "t") != before || db.scalar(t, "SELECT value FROM t") != "long value" {
			t.Fatal("failure changed data/schema")
		}
	})
	t.Run("not_null_add", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY) ENGINE=InnoDB")
		type model struct {
			ID    int `rnsql:"id" rntype:"INT" rnopt:"PK"`
			Value int `rnsql:"value" rntype:"INT" rnopt:"NN"`
		}
		if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"t", model{}}}, nil); !errors.Is(err, ErrUnsafeMigration) {
			t.Fatal(err)
		}
	})
}

func TestMySQLMigrationDriftTamperAndDML(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY AUTO_INCREMENT) ENGINE=InnoDB")
	type model struct {
		ID    int `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
		Value int `rnsql:"value" rntype:"INT"`
	}
	tables := []MigrationTable{{"t", model{}}}
	p := db.plan(t, tables, nil)
	db.exec(t, "INSERT INTO t () VALUES ()")
	db.apply(t, p) // counter drift is allowed
	p = db.plan(t, tables, nil)
	db.exec(t, "ALTER TABLE t ADD COLUMN external_change INT")
	db.rec.reset()
	if err := db.ApplyMigration(context.Background(), p); !errors.Is(err, ErrMigrationDrift) {
		t.Fatal(err)
	}
	if len(db.rec.ddl()) != 0 {
		t.Fatal("stale plan executed DDL")
	}
	p = db.plan(t, tables, nil)
	p.Warnings = append(p.Warnings, "tamper")
	if err := db.ApplyMigration(context.Background(), p); !errors.Is(err, ErrMigrationPlanChanged) {
		t.Fatal(err)
	}
}

func TestMySQLMigrationSessionRestorationAndSingleConnection(t *testing.T) {
	db := newMigrationTestDB(t)
	db.h.DB.SetMaxOpenConns(1)
	db.h.DB.SetMaxIdleConns(1)
	db.exec(t, "SET SESSION sql_mode='NO_BACKSLASH_ESCAPES', foreign_key_checks=0, lock_wait_timeout=23")
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	p := db.plan(t, []MigrationTable{{"t", model{}}}, nil)
	db.rec.failExec = func(q string) error {
		if isMigrationDDL(q) {
			return errors.New("injected DDL failure")
		}
		return nil
	}
	if err := db.ApplyMigration(context.Background(), p); err == nil {
		t.Fatal("injected failure ignored")
	}
	db.rec.failExec = nil
	if got := db.scalar(t, "SELECT CONCAT(@@SESSION.sql_mode,':',@@SESSION.foreign_key_checks,':',@@SESSION.lock_wait_timeout)"); got != "NO_BACKSLASH_ESCAPES:0:23" {
		t.Fatal(got)
	}
	db.apply(t, p)
	if db.scalar(t, "SELECT @@SESSION.foreign_key_checks") != "0" {
		t.Fatal("original setting lost")
	}
	for _, q := range db.rec.execs {
		if strings.Contains(q, "foreign_key_checks = 1") {
			return
		}
	}
	t.Fatal("migration did not force validation on its own connection")
}

func TestMySQLMigrationLockConcurrencyAndRelease(t *testing.T) {
	db := newMigrationTestDB(t)
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	p := db.plan(t, []MigrationTable{{"t", model{}}}, nil)
	conn, err := db.h.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var acquired int
	if err := conn.QueryRowContext(context.Background(), "SELECT GET_LOCK(?,0)", migrationLockName(db.name)).Scan(&acquired); err != nil || acquired != 1 {
		t.Fatal(err)
	}
	if err := db.ApplyMigration(context.Background(), p); !errors.Is(err, ErrMigrationBusy) {
		t.Fatal(err)
	}
	var released int
	if err := conn.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(?)", migrationLockName(db.name)).Scan(&released); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	db.apply(t, p)
	if db.scalar(t, "SELECT IS_FREE_LOCK(?)", migrationLockName(db.name)) != "1" {
		t.Fatal("lock leaked")
	}
}

func TestMySQLMigrationFailureAtEveryStepAndResume(t *testing.T) {
	type parent struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	type child struct {
		ID     int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Parent int `rnsql:"parent_id" rntype:"INT" FK:"parent.id"`
	}
	tables := []MigrationTable{{"parent", parent{}}, {"child", child{}}}
	for failAt := 0; failAt < 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			db := newMigrationTestDB(t)
			p := db.plan(t, tables, nil)
			if len(p.Steps) != 3 {
				t.Fatal(p.SQL())
			}
			count := 0
			db.rec.failExec = func(q string) error {
				if isMigrationDDL(q) {
					current := count
					count++
					if current == failAt {
						return errors.New("injected migration failure")
					}
				}
				return nil
			}
			err := db.ApplyMigration(context.Background(), p)
			var applied *MigrationApplyError
			if !errors.As(err, &applied) || applied.Applied != failAt || applied.Step != failAt {
				t.Fatalf("incorrect partial progress: %v", err)
			}
			db.rec.reset()
			retry := db.plan(t, tables, nil)
			if len(retry.Steps) != 3-failAt {
				t.Fatalf("resume planned completed DDL: %s", retry.SQL())
			}
			db.apply(t, retry)
			db.assertNoop(t, tables, nil)
		})
	}
}

func TestMySQLMigrationExactMetadataLookups(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE `aXb` (`cXd` INT, KEY `iXj` (`cXd`)) ENGINE=InnoDB")
	for name, fn := range map[string]func() (bool, error){"table": func() (bool, error) { return db.HasTable("a_b") }, "column": func() (bool, error) { return db.HasColumn("aXb", "c_d") }, "index": func() (bool, error) { return db.HasIndex("aXb", "i_j") }} {
		ok, err := fn()
		if err != nil || ok {
			t.Fatalf("%s wildcard lookup: %v %v", name, ok, err)
		}
	}
	cols, err := db.GetColumns("a_b")
	if err != nil || len(cols) != 0 {
		t.Fatalf("columns: %v %v", cols, err)
	}
	if ok, err := db.HasTable("aXb"); err != nil || !ok {
		t.Fatal(ok, err)
	}
}

func TestMySQLMigrationContextAndMetadataTimeout(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t VALUES(1)")
	type model struct {
		ID   int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		More int `rnsql:"more" rntype:"INT"`
	}
	tables := []MigrationTable{{"t", model{}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.PlanMigration(ctx, tables, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p := db.plan(t, tables, nil, MigrationOptions{LockWaitTimeout: time.Second})
	tx, err := db.h.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int
	if err := tx.QueryRow("SELECT id FROM t").Scan(&id); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = db.ApplyMigration(context.Background(), p)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("metadata lock timeout did not work: %v", err)
	}
	tx.Rollback()
	db.apply(t, p)
}

func TestMySQLMigrationLegacyEntryPoints(t *testing.T) {
	db := newMigrationTestDB(t)
	type model struct {
		ID    int    `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
		Value string `rnsql:"value" rntype:"VARCHAR(20)"`
	}
	if err := db.Migration("t", model{}); err != nil {
		t.Fatal(err)
	}
	db.exec(t, "INSERT INTO t(value) VALUES('hello')")
	db.rec.reset()
	if err := db.AlterTable("t", model{}); err != nil {
		t.Fatal(err)
	}
	if len(db.rec.ddl()) != 0 {
		t.Fatal(db.rec.ddl())
	}
	idx := migrationIndex("t", "value_idx", DBIndexTypeIndex, "value")
	if err := db.MigrationIndex([]*DBIndex{idx}); err != nil {
		t.Fatal(err)
	}
	db.rec.reset()
	if err := db.MigrationIndex([]*DBIndex{idx}); err != nil {
		t.Fatal(err)
	}
	if len(db.rec.ddl()) != 0 {
		t.Fatal(db.rec.ddl())
	}
	txdb, err := db.BeginTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer txdb.RollbackTx()
	if err := txdb.Migration("t", model{}); err == nil {
		t.Fatal("DDL accepted in transaction")
	}
}
