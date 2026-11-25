package gorn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMySQLMigrationUniqueTagSurvivesIndexChanges(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprint(drop), func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,value INT,UNIQUE KEY existing(value)) ENGINE=InnoDB")
			db.exec(t, "INSERT INTO t VALUES(1,7)")
			type model struct {
				ID    int `rnsql:"id" rntype:"INT" rnopt:"PK"`
				Value int `rnsql:"value" rntype:"INT" rnopt:"UQ"`
			}
			tables := []MigrationTable{{"t", model{}}}
			var indexes []*DBIndex
			if !drop {
				indexes = []*DBIndex{migrationIndex("t", "existing", DBIndexTypeIndex, "id")}
			}
			opts := MigrationOptions{AllowUnsafeChanges: true, AllowDropIndexes: drop}
			p := db.plan(t, tables, indexes, opts)
			db.apply(t, p)
			if _, err := db.h.DB.Exec("INSERT INTO t VALUES(2,7)"); err == nil {
				t.Fatal("UQ constraint lost when old index changed")
			}
			db.assertNoop(t, tables, indexes, opts)
		})
	}
}

func TestMySQLMigrationBinaryAndAutoIncrementWithoutPrimary(t *testing.T) {
	t.Run("binary_collation", func(t *testing.T) {
		db := newMigrationTestDB(t)
		type model struct {
			ID    int    `rnsql:"id" rntype:"INT" rnopt:"PK"`
			Value string `rnsql:"value" rntype:"VARCHAR(20)" rnopt:"BIN"`
		}
		tables := []MigrationTable{{"t", model{}}}
		db.apply(t, db.plan(t, tables, nil))
		db.exec(t, "INSERT INTO t VALUES(1,'a'),(2,'A')")
		if db.scalar(t, "SELECT COUNT(*) FROM t WHERE value='a'") != "1" {
			t.Fatal("BIN not enforced")
		}
		db.assertNoop(t, tables, nil)
	})
	t.Run("explicit_binary_conversion", func(t *testing.T) {
		db := newMigrationTestDB(t)
		db.exec(t, "CREATE TABLE t(value VARCHAR(20)) ENGINE=InnoDB")
		type model struct {
			Value string `rnsql:"value" rntype:"VARCHAR(20)" rnopt:"BIN"`
		}
		tables := []MigrationTable{{"t", model{}}}
		if _, err := db.PlanMigration(context.Background(), tables, nil); !errors.Is(err, ErrUnsafeMigration) {
			t.Fatal(err)
		}
		db.apply(t, db.plan(t, tables, nil, MigrationOptions{AllowUnsafeChanges: true}))
		db.assertNoop(t, tables, nil)
	})
	t.Run("auto_unique", func(t *testing.T) {
		db := newMigrationTestDB(t)
		type model struct {
			ID int `rnsql:"id" rntype:"INT" rnopt:"AI UQ"`
		}
		tables := []MigrationTable{{"t", model{}}}
		db.apply(t, db.plan(t, tables, nil))
		db.exec(t, "INSERT INTO t () VALUES ()")
		db.assertNoop(t, tables, nil)
	})
	t.Run("auto_unindexed_rejected", func(t *testing.T) {
		db := newMigrationTestDB(t)
		type model struct {
			ID int `rnsql:"id" rntype:"INT" rnopt:"AI"`
		}
		if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"t", model{}}}, nil); err == nil {
			t.Fatal("unindexed AI accepted")
		}
	})
}

func TestMySQLMigrationForeignKeyExplicitActionsAndCompositePreservation(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE parent(a INT NOT NULL,b INT NOT NULL,PRIMARY KEY(a,b),UNIQUE KEY a_unique(a)) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE child(id INT PRIMARY KEY,a INT,b INT,CONSTRAINT composite_fk FOREIGN KEY(a,b) REFERENCES parent(a,b)) ENGINE=InnoDB")
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		A  int `rnsql:"a" rntype:"INT"`
		B  int `rnsql:"b" rntype:"INT"`
	}
	db.assertNoop(t, []MigrationTable{{"child", model{}}}, nil)
	fks, err := db.GetForeignKeys("child")
	if err != nil || len(fks) != 2 || fks[0].OrdinalPosition != 1 || fks[1].OrdinalPosition != 2 {
		t.Fatalf("composite metadata: %+v %v", fks, err)
	}
	type other struct {
		ID int  `rnsql:"id" rntype:"INT" rnopt:"PK"`
		A  *int `rnsql:"a" rntype:"INT" FK:"parent.a" FKOnDelete:"SET NULL" FKOnUpdate:"CASCADE"`
	}
	tables := []MigrationTable{{"other", other{}}}
	db.apply(t, db.plan(t, tables, nil))
	db.assertNoop(t, tables, nil)
	db.exec(t, "INSERT INTO parent VALUES(1,1)")
	db.exec(t, "INSERT INTO other VALUES(2,1)")
	db.exec(t, "UPDATE parent SET a=3 WHERE a=1")
	if db.scalar(t, "SELECT a FROM other") != "3" {
		t.Fatal("ON UPDATE lost")
	}
	db.exec(t, "DELETE FROM parent")
	if db.scalar(t, "SELECT a IS NULL FROM other") != "1" {
		t.Fatal("ON DELETE lost")
	}
}

func TestMySQLMigrationRejectsReferencedColumnTypeChangesBeforeDDL(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE parent(id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "CREATE TABLE child(id INT PRIMARY KEY,ref INT,CONSTRAINT fk FOREIGN KEY(ref) REFERENCES parent(id)) ENGINE=InnoDB")
	type parent struct {
		ID int `rnsql:"id" rntype:"BIGINT" rnopt:"PK"`
	}
	type child struct {
		ID  int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Ref int `rnsql:"ref" rntype:"BIGINT" FK:"parent.id"`
	}
	for _, tables := range [][]MigrationTable{{{"parent", parent{}}}, {{"child", child{}}}, {{"parent", parent{}}, {"child", child{}}}} {
		db.rec.reset()
		if _, err := db.PlanMigration(context.Background(), tables, nil, MigrationOptions{AllowUnsafeChanges: true}); err == nil || !strings.Contains(err.Error(), "foreign-key column") {
			t.Fatal(err)
		}
		if len(db.rec.execs) != 0 {
			t.Fatal("rejected type change executed SQL")
		}
	}
}

func TestMySQLMigrationRowsErrorsDoNotBecomeEmptySchema(t *testing.T) {
	db := newMigrationTestDB(t)
	db.h.DB.SetMaxOpenConns(1)
	db.h.DB.SetMaxIdleConns(1)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,KEY idx(id)) ENGINE=InnoDB")
	sentinel := errors.New("injected rows.Next failure")
	for _, which := range []string{"COLUMNS", "STATISTICS", "KEY_COLUMN_USAGE"} {
		t.Run(which, func(t *testing.T) {
			db.rec.reset()
			db.rec.failRows = func(q string) error {
				if strings.Contains(q, "INFORMATION_SCHEMA."+which) {
					return sentinel
				}
				return nil
			}
			var err error
			switch which {
			case "COLUMNS":
				_, err = db.GetColumns("t")
			case "STATISTICS":
				_, err = db.GetIndexes()
			case "KEY_COLUMN_USAGE":
				_, err = db.GetForeignKeys("t")
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("partial result accepted: %v", err)
			}
			db.rec.reset()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := db.h.DB.PingContext(ctx); err != nil {
				t.Fatalf("rows leaked sole pool connection: %v", err)
			}
		})
	}
}

func TestMySQLMigrationConcurrentApplication(t *testing.T) {
	db := newMigrationTestDB(t)
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	tables := []MigrationTable{{"t", model{}}}
	p := db.plan(t, tables, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	db.rec.mu.Lock()
	db.rec.failExec = func(q string) error {
		if isMigrationDDL(q) {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	db.rec.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- db.ApplyMigration(context.Background(), p) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("first migration did not start")
	}
	err := db.ApplyMigration(context.Background(), p)
	close(release)
	first := <-done
	if first != nil || !errors.Is(err, ErrMigrationBusy) {
		t.Fatalf("concurrency results: first=%v second=%v", first, err)
	}
	db.rec.reset()
	db.assertNoop(t, tables, nil)
}

func TestMySQLMigrationWrongDatabaseAndDroppedObjectDrift(t *testing.T) {
	db := newMigrationTestDB(t)
	other := newMigrationTestDB(t)
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	tables := []MigrationTable{{"t", model{}}}
	p := db.plan(t, tables, nil)
	if err := other.ApplyMigration(context.Background(), p); !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("wrong database accepted: %v", err)
	}
	if other.scalar(t, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA=?", other.name) != "0" {
		t.Fatal("wrong DB modified")
	}
	db.apply(t, p)
	p = db.plan(t, tables, nil)
	db.exec(t, "DROP TABLE t")
	if err := db.ApplyMigration(context.Background(), p); !errors.Is(err, ErrMigrationDrift) {
		t.Fatal(err)
	}
}

func TestMySQLMigrationIdentifierEscapingInPlans(t *testing.T) {
	db := newMigrationTestDB(t)
	type model struct {
		ID    int    `rnsql:"select" rntype:"INT" rnopt:"PK"`
		Value string `rnsql:"order" rntype:"VARCHAR(20)"`
	}
	tables := []MigrationTable{{"table`%_한글", model{}}}
	indexes := []*DBIndex{migrationIndex("table`%_한글", "index`name", DBIndexTypeIndex, "order")}
	db.apply(t, db.plan(t, tables, indexes))
	db.assertNoop(t, tables, indexes)
}

func TestMySQLMigrationGeneratedColumnChangeRejected(t *testing.T) {
	db := newMigrationTestDB(t)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,derived INT GENERATED ALWAYS AS (id+1) STORED) ENGINE=InnoDB")
	type same struct {
		ID      int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Derived int `rnsql:"derived" rntype:"INT"`
	}
	db.assertNoop(t, []MigrationTable{{"t", same{}}}, nil)
	type changed struct {
		ID      int `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Derived int `rnsql:"derived" rntype:"BIGINT"`
	}
	if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"t", changed{}}}, nil, MigrationOptions{AllowUnsafeChanges: true}); err == nil {
		t.Fatal("generated column silently rewritten")
	}
}

func TestMySQLMigrationIndexGetterRejectsUnrepresentableIndexes(t *testing.T) {
	for _, definition := range []string{"KEY idx(value) INVISIBLE", "KEY idx((lower(value)))", "FULLTEXT KEY idx(value)"} {
		t.Run(definition, func(t *testing.T) {
			db := newMigrationTestDB(t)
			db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY,value VARCHAR(100),"+definition+") ENGINE=InnoDB")
			if _, err := db.GetIndexes(); err == nil {
				t.Fatal("advanced index metadata silently flattened")
			}
		})
	}
}

func TestMigrationIdentifierAndIndexInputValidation(t *testing.T) {
	for _, s := range []string{"", "x.y", "\x00", strings.Repeat("x", 65), "trailing ", "\xff"} {
		if err := validateMigrationIdentifier(s); err == nil {
			t.Errorf("accepted identifier %q", s)
		}
	}
	for _, idx := range []*DBIndex{
		{TableName: "t", IndexName: "PRIMARY", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{{ColumnName: "id"}}},
		{TableName: "t", IndexName: "GORN_FK_0", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{{ColumnName: "id"}}},
		{TableName: "t", IndexName: "bad", IndexType: "unknown", Columns: []*DBIndexColumn{{ColumnName: "id"}}},
		{TableName: "t", IndexName: "empty", IndexType: DBIndexTypeIndex},
		{TableName: "t", IndexName: "nil", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{nil}},
		{TableName: "t", IndexName: "duplicates", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{{ColumnName: "id"}, {ColumnName: "ID"}}},
		{TableName: "t", IndexName: "prefix", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{{ColumnName: "id", SubPart: sql.NullInt64{Int64: 0, Valid: true}}}},
	} {
		if err := validateMigrationIndex(idx); err == nil {
			t.Errorf("accepted invalid index %+v", idx)
		}
	}
}

func TestMySQLMigrationBitDefaultAndQuotedNumericString(t *testing.T) {
	db := newMigrationTestDB(t)
	type model struct {
		ID   int    `rnsql:"id" rntype:"INT" rnopt:"PK"`
		Bits uint8  `rnsql:"bits" rntype:"BIT(3)" rndefault:"5"`
		Code string `rnsql:"code" rntype:"VARCHAR(20)" rndefault:"'001'"`
	}
	tables := []MigrationTable{{"t", model{}}}
	db.apply(t, db.plan(t, tables, nil))
	db.exec(t, "INSERT INTO t(id) VALUES(1)")
	if db.scalar(t, "SELECT CONCAT(bits+0, ':', code) FROM t") != "5:001" {
		t.Fatal("default value changed")
	}
	db.assertNoop(t, tables, nil)
	type ambiguous struct {
		Code string `rnsql:"code" rntype:"VARCHAR(20)" rndefault:"1e2"`
	}
	if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"t", ambiguous{}}}, nil); err == nil {
		t.Fatal("ambiguous character default accepted")
	}
	if got := sqlDefaultLiteral([]byte(nil)); got != "NULL" {
		t.Fatal(got)
	}
}

func TestMySQLMigrationTemporaryShadowRejected(t *testing.T) {
	db := newMigrationTestDB(t)
	db.h.DB.SetMaxOpenConns(1)
	db.h.DB.SetMaxIdleConns(1)
	db.exec(t, "CREATE TABLE t(id INT PRIMARY KEY) ENGINE=InnoDB")
	db.exec(t, "CREATE TEMPORARY TABLE t(id INT PRIMARY KEY) ENGINE=InnoDB")
	type model struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK"`
	}
	db.rec.reset()
	if _, err := db.PlanMigration(context.Background(), []MigrationTable{{"t", model{}}}, nil); err == nil || !strings.Contains(err.Error(), "temporary table shadows") {
		t.Fatal(err)
	}
	if len(db.rec.execs) != 0 {
		t.Fatal("temporary shadow caused writes")
	}
}
