package gorn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMySQLMigrationDefaultChangesAndSpecialStrings(t *testing.T) {
	for _, mode := range []string{"STRICT_TRANS_TABLES", "STRICT_TRANS_TABLES,NO_BACKSLASH_ESCAPES", "STRICT_TRANS_TABLES,ANSI_QUOTES"} {
		t.Run(mode, func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.h.DB.SetMaxOpenConns(1)
			db.h.DB.SetMaxIdleConns(1)
			db.exec(t, "SET SESSION sql_mode=?", mode)
			db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY, value VARCHAR(40) NOT NULL DEFAULT 'old', stamp TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB")
			db.exec(t, "INSERT INTO t(id) VALUES(1)")
			type model struct {
				ID    int       `rnsql:"id" rntype:"INT" rnopt:"PK"`
				Value string    `rnsql:"value" rntype:"VARCHAR(40)" rnopt:"NN" rndefault:"'it''s 한글'"`
				Stamp time.Time `rnsql:"stamp" rntype:"TIMESTAMP" rndefault:"CURRENT_TIMESTAMP"`
			}
			tables := []MigrationTable{{"t", model{}}}
			p := db.plan(t, tables, nil)
			if strings.Count(p.SQL(), "MODIFY") != 1 {
				t.Fatal(p.SQL())
			}
			db.apply(t, p)
			db.exec(t, "INSERT INTO t(id) VALUES(2)")
			if db.scalar(t, "SELECT value FROM t WHERE id=1") != "old" || db.scalar(t, "SELECT value FROM t WHERE id=2") != "it's 한글" {
				t.Fatal("default change rewrote existing data or wrong new default")
			}
			db.assertNoop(t, tables, nil)
		})
	}
}

func TestMySQLMigrationTypeWideningMatrix(t *testing.T) {
	cases := []struct{ old, next, value string }{
		{"TINYINT", "SMALLINT", "127"}, {"SMALLINT", "INT", "32767"}, {"INT", "BIGINT", "2147483647"}, {"INT UNSIGNED", "BIGINT UNSIGNED", "4294967295"},
		{"VARCHAR(8)", "VARCHAR(80)", "'한글'"}, {"VARBINARY(8)", "VARBINARY(80)", "X'0027ff'"}, {"DECIMAL(10,2)", "DECIMAL(14,4)", "1234.56"},
		{"TEXT", "MEDIUMTEXT", "'sample'"}, {"BLOB", "LONGBLOB", "X'0027ff'"}, {"DATETIME", "DATETIME(6)", "'2024-01-02 03:04:05'"},
		{"ENUM('A','b')", "ENUM('A','b','c')", "'b'"}, {"SET('a','b')", "SET('a','b','c')", "'a,b'"},
	}
	for _, tc := range cases {
		t.Run(tc.old+"_to_"+tc.next, func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY, value "+tc.old+") ENGINE=InnoDB")
			db.exec(t, "INSERT INTO t VALUES(1,"+tc.value+")")
			before := db.scalar(t, "SELECT HEX(value) FROM t")
			model := reflect.New(reflect.StructOf([]reflect.StructField{{Name: "ID", Type: reflect.TypeOf(0), Tag: `rnsql:"id" rntype:"INT" rnopt:"PK"`}, {Name: "Value", Type: reflect.TypeOf(""), Tag: reflect.StructTag(`rnsql:"value" rntype:"` + tc.next + `"`)}})).Interface()
			tables := []MigrationTable{{"t", model}}
			p := db.plan(t, tables, nil)
			db.apply(t, p)
			after := db.scalar(t, "SELECT HEX(value) FROM t")
			if before != after && !strings.HasPrefix(tc.old, "DECIMAL") && !strings.HasPrefix(tc.old, "DATETIME") {
				t.Fatalf("data changed: %s -> %s", before, after)
			}
			if strings.HasPrefix(tc.old, "DECIMAL") && db.scalar(t, "SELECT value=1234.56 FROM t") != "1" {
				t.Fatal("decimal value changed")
			}
			if strings.HasPrefix(tc.old, "DATETIME") && db.scalar(t, "SELECT value='2024-01-02 03:04:05' FROM t") != "1" {
				t.Fatal("datetime changed")
			}
			db.assertNoop(t, tables, nil)
		})
	}
}

func TestMySQLMigrationPrimaryKeyOrderAndAutoIncrement(t *testing.T) {
	t.Run("add_key_with_column", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (value VARCHAR(30)) ENGINE=InnoDB")
		db.exec(t, "INSERT INTO t VALUES('a'),('b')")
		type model struct {
			ID    int    `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
			Value string `rnsql:"value" rntype:"VARCHAR(30)"`
		}
		tables := []MigrationTable{{"t", model{}}}
		p := db.plan(t, tables, nil)
		if strings.Count(p.SQL(), "ADD PRIMARY KEY") != 1 || strings.Contains(p.SQL(), "`id`, `id`") {
			t.Fatal(p.SQL())
		}
		db.apply(t, p)
		db.exec(t, "INSERT INTO t(value) VALUES('c')")
		if db.scalar(t, "SELECT GROUP_CONCAT(id ORDER BY id) FROM t") != "1,2,3" {
			t.Fatal("auto increment not preserved")
		}
		db.assertNoop(t, tables, nil)
	})
	t.Run("composite_key_order_from_index", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (a INT NOT NULL,b INT NOT NULL,PRIMARY KEY(b,a)) ENGINE=InnoDB")
		type model struct {
			B int `rnsql:"b" rntype:"INT" rnopt:"PK"`
			A int `rnsql:"a" rntype:"INT" rnopt:"PK"`
		}
		db.assertNoop(t, []MigrationTable{{"t", model{}}}, nil)
	})
	t.Run("explicit_atomic_key_change", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (a INT NOT NULL,b INT NOT NULL,PRIMARY KEY(a,b)) ENGINE=InnoDB")
		db.exec(t, "INSERT INTO t VALUES(1,2),(3,4)")
		type model struct {
			B int `rnsql:"b" rntype:"INT" rnopt:"PK"`
			A int `rnsql:"a" rntype:"INT" rnopt:"PK"`
		}
		tables := []MigrationTable{{"t", model{}}}
		p := db.plan(t, tables, nil, MigrationOptions{AllowUnsafeChanges: true})
		if len(p.Steps) != 1 {
			t.Fatal(p.SQL())
		}
		db.apply(t, p)
		db.assertNoop(t, tables, nil)
	})
}

func TestMySQLMigrationNullability(t *testing.T) {
	t.Run("relax", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (value INT NOT NULL DEFAULT 7) ENGINE=InnoDB")
		db.exec(t, "INSERT INTO t VALUES(1)")
		type model struct {
			Value *int `rnsql:"value" rntype:"INT"`
		}
		tables := []MigrationTable{{"t", model{}}}
		db.apply(t, db.plan(t, tables, nil))
		db.exec(t, "INSERT INTO t VALUES(NULL)")
		if db.scalar(t, "SELECT COUNT(*) FROM t") != "2" {
			t.Fatal("rows lost")
		}
		db.assertNoop(t, tables, nil)
	})
	t.Run("tighten_with_null_fails_without_rewriting", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t (value INT NULL) ENGINE=InnoDB")
		db.exec(t, "INSERT INTO t VALUES(NULL),(2)")
		type model struct {
			Value int `rnsql:"value" rntype:"INT" rnopt:"NN"`
		}
		tables := []MigrationTable{{"t", model{}}}
		before := db.definition(t, "t")
		p := db.plan(t, tables, nil, MigrationOptions{AllowUnsafeChanges: true})
		if err := db.ApplyMigration(context.Background(), p); err == nil {
			t.Fatal("NULL silently converted")
		}
		if before != db.definition(t, "t") || db.scalar(t, "SELECT COUNT(*) FROM t WHERE value IS NULL") != "1" {
			t.Fatal("failed conversion modified rows")
		}
		db.exec(t, "UPDATE t SET value=0 WHERE value IS NULL")
		db.apply(t, p)
		db.assertNoop(t, tables, nil)
	})
}

func TestMySQLMigrationIndexPrefixesAndDirections(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t (id INT PRIMARY KEY,value VARCHAR(100),other INT) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t VALUES(1,'abcdef',9)")
	idx := &DBIndex{TableName: "t", IndexName: "mixed", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{{ColumnName: "value", SubPart: sql.NullInt64{Int64: 12, Valid: true}, ASC: true}, {ColumnName: "other", ASC: false}}}
	db.apply(t, db.plan(t, nil, []*DBIndex{idx}))
	db.assertNoop(t, nil, []*DBIndex{idx})
	idx.Columns[0].SubPart.Int64 = 20
	p := db.plan(t, nil, []*DBIndex{idx})
	if len(p.Steps) != 1 || !strings.Contains(p.SQL(), "(20)") {
		t.Fatal(p.SQL())
	}
	db.apply(t, p)
	db.assertNoop(t, nil, []*DBIndex{idx})
}

func TestMySQLMigrationExplicitScopedDrops(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE parent(id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE child(id INT PRIMARY KEY,ref INT,extra INT,KEY extra_idx(extra),CONSTRAINT fk FOREIGN KEY(ref) REFERENCES parent(id)) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE unrelated(id INT,KEY keep_me(id)) ENGINE=InnoDB")
	type model struct {
		ID    int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Ref   int `rnsql:"ref" rntype:"INT"`
		Extra int `rnsql:"extra" rntype:"INT"`
	}
	tables := []MigrationTable{{"child", model{}}}
	db.assertNoop(t, tables, nil)
	p := db.plan(t, tables, nil, MigrationOptions{AllowDropForeignKeys: true})
	db.apply(t, p)
	if strings.Contains(db.definition(t, "child"), "CONSTRAINT `fk`") {
		t.Fatal("FK not removed")
	}
	if !strings.Contains(db.definition(t, "child"), "KEY `fk`") {
		t.Fatal("backing index removed with FK")
	}
	p = db.plan(t, tables, nil, MigrationOptions{AllowDropIndexes: true})
	db.apply(t, p)
	if strings.Contains(db.definition(t, "child"), "KEY `extra_idx`") {
		t.Fatal("index not dropped")
	}
	if !strings.Contains(db.definition(t, "unrelated"), "keep_me") {
		t.Fatal("unrelated index dropped")
	}
}

func TestMySQLMigrationAdvancedObjectsPreserved(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,value VARCHAR(40),calc INT GENERATED ALWAYS AS (length(value)) STORED,KEY hidden(value) INVISIBLE,KEY expr((lower(value))),CONSTRAINT nonempty CHECK(length(value)>0)) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t(id,value) VALUES(1,'ABC')")
	type model struct {
		ID    int    `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Value string `rnsql:"value" rntype:"VARCHAR(40)"`
	}
	tables := []MigrationTable{{"t", model{}}}
	db.assertNoop(t, tables, nil)
	before := db.definition(t, "t")
	for _, name := range []string{"hidden", "expr"} {
		if _, err := db.PlanMigration(context.Background(), nil, []*DBIndex{migrationIndex("t", name, DBIndexTypeIndex, "id")}); err == nil {
			t.Fatalf("advanced index %s silently replaced", name)
		}
	}
	if db.definition(t, "t") != before {
		t.Fatal("unsupported operation mutated schema")
	}
	if db.scalar(t, "SELECT calc FROM t") != "3" {
		t.Fatal("generated column changed")
	}
}

func TestMySQLMigrationRejectsUnsupportedAndInvalidPlansBeforeDDL(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,value INT,KEY value_idx(value)) ENGINE=InnoDB")
	db.exec(t, "CREATE VIEW v AS SELECT id FROM t")
	db.exec(t, "CREATE TABLE nontransactional(id INT) ENGINE=MyISAM")
	type justID struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	type foreign struct {
		ID  int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Ref int `rnsql:"ref" rntype:"INT" FK:"missing.id"`
	}
	cases := []struct {
		name    string
		tables  []MigrationTable
		indexes []*DBIndex
		opts    MigrationOptions
	}{
		{"view", []MigrationTable{{"v", justID{}}}, nil, MigrationOptions{}}, {"non_innodb", []MigrationTable{{"nontransactional", justID{}}}, nil, MigrationOptions{}},
		{"duplicate_table", []MigrationTable{{"t", justID{}}, {"t", justID{}}}, nil, MigrationOptions{}}, {"missing_reference", []MigrationTable{{"new", foreign{}}}, nil, MigrationOptions{}},
		{"duplicate_index", nil, []*DBIndex{migrationIndex("t", "same_name", DBIndexTypeIndex, "id"), migrationIndex("t", "same_name", DBIndexTypeIndex, "value")}, MigrationOptions{}},
		{"nil_index", nil, []*DBIndex{nil}, MigrationOptions{}}, {"missing_index_column", nil, []*DBIndex{migrationIndex("t", "bad", DBIndexTypeIndex, "missing")}, MigrationOptions{}},
		{"missing_index_table", nil, []*DBIndex{migrationIndex("absent", "bad", DBIndexTypeIndex, "id")}, MigrationOptions{}}, {"drop_indexed_column", []MigrationTable{{"t", justID{}}}, nil, MigrationOptions{AllowDropColumns: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db.rec.reset()
			if _, err := db.PlanMigration(context.Background(), tc.tables, tc.indexes, tc.opts); err == nil {
				t.Fatal("invalid plan accepted")
			}
			if len(db.rec.execs) != 0 {
				t.Fatal("invalid plan performed writes")
			}
		})
	}
}

func TestMySQLMigrationDryRunMetadataFailures(t *testing.T) {
	for _, fragment := range []string{"SELECT DATABASE()", "INFORMATION_SCHEMA.TABLES", "INFORMATION_SCHEMA.COLUMNS", "INFORMATION_SCHEMA.STATISTICS", "INFORMATION_SCHEMA.KEY_COLUMN_USAGE", "SHOW CREATE TABLE"} {
		t.Run(fragment, func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY) ENGINE=InnoDB")
			type model struct {
				ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
			}
			db.rec.reset()
			sentinel := errors.New("injected metadata failure")
			db.rec.failQuery = func(q string) error {
				if strings.Contains(q, fragment) {
					return sentinel
				}
				return nil
			}
			if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"t", model{}}}, nil); !errors.Is(err, sentinel) {
				t.Fatalf("read failure swallowed: %v", err)
			}
			if len(db.rec.execs) != 0 {
				t.Fatal("dry run mutated database")
			}
			db.rec.reset()
			db.assertNoop(t, []MigrationTable{{"t", model{}}}, nil)
		})
	}
}

func TestMySQLMigrationCancellationDuringDDL(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t VALUES(1)")
	type model struct {
		ID   int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		More int `rnsql:"more" rntype:"INT"`
	}
	tables := []MigrationTable{{"t", model{}}}
	p := db.plan(t, tables, nil)
	tx, err := db.h.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var id int
	if err := tx.QueryRow("SELECT id FROM t").Scan(&id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err = db.ApplyMigration(ctx, p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context error: %v", err)
	}
	tx.Rollback()
	// The MySQL driver discards a connection whose command was canceled. Give
	// the server time to observe its closed socket, then prove the lock/session
	// was not returned to another borrower and a fresh plan can succeed.
	deadline := time.Now().Add(3 * time.Second)
	for db.scalar(t, "SELECT IS_FREE_LOCK(?)", migrationLockName(db.name)) != "1" {
		if time.Now().After(deadline) {
			t.Fatal("lock leaked after cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	db.apply(t, db.plan(t, tables, nil))
	db.assertNoop(t, tables, nil)
}

func TestMySQLMigrationCleanupFailureDiscardsSession(t *testing.T) {
	db := newMigrationTestDB(t)
	db.h.DB.SetMaxOpenConns(1)
	db.h.DB.SetMaxIdleConns(1)
	oldID := db.scalar(t, "SELECT CONNECTION_ID()")
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	p := db.plan(t, []MigrationTable{{"t", model{}}}, nil)
	db.rec.failExec = func(q string) error {
		if strings.Contains(q, "foreign_key_checks = ?") {
			return errors.New("injected restore failure")
		}
		return nil
	}
	if err := db.ApplyMigration(context.Background(), p); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatal(err)
	}
	db.rec.reset()
	if nextID := db.scalar(t, "SELECT CONNECTION_ID()"); nextID == oldID {
		t.Fatal("dirty connection returned to pool")
	}
	db.assertNoop(t, []MigrationTable{{"t", model{}}}, nil)
}

func TestMySQLMigrationInstantAndInplacePolicy(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,value VARCHAR(20)) ENGINE=InnoDB")
	db.exec(t, "INSERT INTO t VALUES(1,'keep')")
	type model struct {
		ID    int    `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Value string `rnsql:"value" rntype:"VARCHAR(20)"`
		More  int    `rnsql:"more" rntype:"INT"`
	}
	tables := []MigrationTable{{"t", model{}}}
	p := db.plan(t, tables, nil, MigrationOptions{Algorithm: "INSTANT"})
	db.apply(t, p)
	db.assertNoop(t, tables, nil)
	idx := migrationIndex("t", "value_idx", DBIndexTypeIndex, "value")
	p = db.plan(t, nil, []*DBIndex{idx}, MigrationOptions{Algorithm: "INPLACE", Lock: "NONE"})
	db.apply(t, p)
	db.assertNoop(t, nil, []*DBIndex{idx})
}

func TestMySQLMigrationBuilderDefaultsAndIdentifierEscaping(t *testing.T) {
	for _, mode := range []string{"STRICT_TRANS_TABLES", "STRICT_TRANS_TABLES,NO_BACKSLASH_ESCAPES"} {
		t.Run(mode, func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.h.DB.SetMaxOpenConns(1)
			db.h.DB.SetMaxIdleConns(1)
			db.exec(t, "SET SESSION sql_mode=?", mode)
			db.exec(t, "CREATE TABLE `t``x`(id INT PRIMARY KEY) ENGINE=InnoDB")
			for i, value := range []interface{}{"it's \\ safe 한글", "", 42, true, nil, sql.NullString{String: "nullable", Valid: true}, sql.NullString{}} {
				name := fmt.Sprintf("c`%d", i)
				q := NewSql().Alter().Table("t`x").AddColumn(name, "VARCHAR(80)", "").Default(value).Query(false)
				db.exec(t, q)
			}
			db.exec(t, "INSERT INTO `t``x`(id) VALUES(1)")
			if db.scalar(t, "SELECT `c``0` FROM `t``x`") != "it's \\ safe 한글" {
				t.Fatal("literal was not preserved")
			}
			db.exec(t, NewSql().Alter().Table("t`x").ModifyColumn("c`0", "VARCHAR(100)", "").Query(false))
		})
	}
}

func TestMigrationDefaultLiteralRejectsInvalidValues(t *testing.T) {
	for _, value := range []interface{}{math.NaN(), math.Inf(1), struct{}{}, []int{1}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("accepted %#v", value)
				}
			}()
			NewSql().Default(value)
		}()
	}
}

func TestMySQLMigrationEnumEscapingNoop(t *testing.T) {
	for _, mode := range []string{"STRICT_TRANS_TABLES", "STRICT_TRANS_TABLES,NO_BACKSLASH_ESCAPES"} {
		t.Run(mode, func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.h.DB.SetMaxOpenConns(1)
			db.h.DB.SetMaxIdleConns(1)
			db.exec(t, "SET SESSION sql_mode=?", mode)
			typeSQL := `ENUM('it''s','a\\b')`
			model := reflect.New(reflect.StructOf([]reflect.StructField{{Name: "Value", Type: reflect.TypeOf(""), Tag: reflect.StructTag(`rnsql:"value" rntype:` + strconv.Quote(typeSQL))}})).Interface()
			tables := []MigrationTable{{"t", model}}
			db.apply(t, db.plan(t, tables, nil))
			db.exec(t, "INSERT INTO t VALUES(?)", "it's")
			commentSQL := "'comment\\\\path'"
			if strings.Contains(mode, "NO_BACKSLASH_ESCAPES") {
				commentSQL = "'comment\\path'"
			}
			db.exec(t, "ALTER TABLE t ADD name VARCHAR(40) DEFAULT X'61275c62' COMMENT "+commentSQL)
			db.assertNoop(t, tables, nil)
			if db.scalar(t, "SELECT value FROM t") != "it's" {
				t.Fatal("enum value changed")
			}
			typ := reflect.TypeOf(model).Elem()
			expanded := reflect.New(reflect.StructOf([]reflect.StructField{typ.Field(0), {Name: "Name", Type: reflect.TypeOf(""), Tag: `rnsql:"name" rntype:"VARCHAR(80)"`}})).Interface()
			tables[0].Model = expanded
			db.apply(t, db.plan(t, tables, nil))
			db.exec(t, "INSERT INTO t(value) VALUES(?)", "it's")
			if db.scalar(t, "SELECT COUNT(*) FROM t WHERE HEX(name)='61275C62'") != "2" {
				t.Fatal("default escaping changed on widening")
			}
			if db.scalar(t, "SELECT COLUMN_COMMENT FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME='t' AND COLUMN_NAME='name'", db.name) != "comment\\path" {
				t.Fatal("comment escaping changed on widening")
			}
			db.assertNoop(t, tables, nil)
		})
	}
}
