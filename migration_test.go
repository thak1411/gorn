package gorn

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMigrationTypeNormalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"INT(11)", "int"}, {"INTEGER", "int"}, {"INT UNSIGNED", "int unsigned"}, {"tinyint(1)", "tinyint"}, {"BOOLEAN", "tinyint"}, {"BOOL", "tinyint"},
		{"BIGINT(20) ZEROFILL", "bigint unsigned zerofill"}, {"DOUBLE PRECISION", "double"}, {"NUMERIC", "decimal(10,0)"}, {"DEC(12)", "decimal(12,0)"}, {"DECIMAL(12, 3)", "decimal(12,3)"},
		{"TIMESTAMP(0)", "timestamp"}, {"datetime(6)", "datetime(6)"}, {"TIME", "time"}, {"YEAR(4)", "year"}, {"CHAR", "char(1)"}, {"BINARY", "binary(1)"}, {"BIT", "bit(1)"},
		{"VARCHAR(64)", "varchar(64)"}, {"VARBINARY(255)", "varbinary(255)"}, {"JSON", "json"}, {"MEDIUMTEXT", "mediumtext"}, {"LONG BLOB", ""},
		{"ENUM('Open','CLOSED','a,b','it''s')", "enum('Open','CLOSED','a,b','it''s')"}, {"SET('a', 'b')", "set('a','b')"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := normalizeMigrationType(tc.in, false)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "INT; DROP TABLE users", "INT DEFAULT 1", "VARCHAR(2) NOT NULL", "ENUM('a'); SELECT 1", "ENUM('a' 'b')", "REAL", "DECIMAL(66,0)", "DECIMAL(3,4)", "DATETIME(7)", "YEAR(2)", "BIT(65)", "FLOAT(9,2)", "VARCHAR", "VARCHAR(4) UNSIGNED", "CHAR(5,2)"} {
		t.Run("reject_"+bad, func(t *testing.T) {
			if _, err := normalizeMigrationType(bad, false); err == nil {
				t.Fatalf("accepted %q", bad)
			}
		})
	}
}

func TestMigrationSafeTypeChanges(t *testing.T) {
	cases := []struct {
		old, next string
		safe      bool
	}{
		{"int", "bigint", true}, {"bigint", "int", false}, {"int", "int unsigned", false}, {"int unsigned", "bigint unsigned", true}, {"varchar(10)", "varchar(40)", true}, {"varchar(40)", "varchar(10)", false},
		{"char(10)", "varchar(10)", false}, {"decimal(10,2)", "decimal(12,4)", true}, {"decimal(10,2)", "decimal(10,4)", false}, {"decimal(10,2)", "decimal(12,1)", false},
		{"text", "longtext", true}, {"blob", "mediumblob", true}, {"text", "blob", false}, {"datetime", "datetime(6)", true}, {"datetime(6)", "datetime", false},
		{"enum('a','b')", "enum('a','b','c')", true}, {"enum('a','b')", "enum('b','a')", false}, {"enum('a','b')", "enum('a')", false}, {"enum('A')", "enum('a')", false}, {"set('a')", "set('a','b')", true},
	}
	for _, tc := range cases {
		t.Run(tc.old+"_to_"+tc.next, func(t *testing.T) {
			if got := safeMigrationTypeChange(tc.old, tc.next, false); got != tc.safe {
				t.Fatalf("safe=%v want %v", got, tc.safe)
			}
		})
	}
}

func TestMigrationSQLDefinitionParser(t *testing.T) {
	ddl := "CREATE TABLE `strange``table` (\n `id` bigint NOT NULL AUTO_INCREMENT,\n `a``b` varchar(40) DEFAULT 'x,y)' COMMENT 'AUTO_INCREMENT=42, it\\'s ok',\n `generated` int GENERATED ALWAYS AS ((length(`a``b`) + 1)) STORED,\n KEY `expr` ((lower(`a``b`)))\n) ENGINE=InnoDB AUTO_INCREMENT=99 COMMENT='AUTO_INCREMENT=100'"
	defs, normalized, err := parseCreateDefinition(ddl, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 3 || !strings.Contains(defs["a`b"], "DEFAULT 'x,y)'") {
		t.Fatalf("bad columns: %#v", defs)
	}
	if strings.Contains(normalized, "AUTO_INCREMENT=99") || !strings.Contains(normalized, "COMMENT='AUTO_INCREMENT=100'") || !strings.Contains(normalized, "AUTO_INCREMENT=42") {
		t.Fatalf("bad normalization %s", normalized)
	}
	_, same, err := parseCreateDefinition(strings.Replace(ddl, "AUTO_INCREMENT=99", "AUTO_INCREMENT=123", 1), false)
	if err != nil || same != normalized {
		t.Fatalf("counter changed schema: %v", err)
	}
	for _, bad := range []string{"", "CREATE TABLE t (id int)", "CREATE TABLE `t` (`a` varchar(4) DEFAULT 'x)", "CREATE TABLE `t` (`a` int))"} {
		if _, _, err := parseCreateDefinition(bad, false); err == nil {
			t.Fatalf("accepted malformed definition %q", bad)
		}
	}
	defs, _, err = parseCreateDefinition(`CREATE TABLE "q" ("id" int NOT NULL, "s" varchar(20) DEFAULT 'a\b') ENGINE=InnoDB`, true)
	if err != nil || defs["s"] == "" {
		t.Fatalf("ANSI_QUOTES / NO_BACKSLASH_ESCAPES: %v", err)
	}
}

func TestMigrationModelValidation(t *testing.T) {
	cases := []interface{}{
		nil, 42, []int{}, struct {
			ID int `rnsql:"id"`
		}{}, struct {
			ID int `rnsql:"id" rntype:"INT" rnopt:"TYPO"`
		}{},
		struct {
			ID    int `rnsql:"id" rntype:"INT"`
			Other int `rnsql:"ID" rntype:"INT"`
		}{},
		struct {
			ID int `rnsql:"x.id" rntype:"INT"`
		}{}, struct {
			ID int `rnsql:"id" rntype:"INT" FK:"a.b.c"`
		}{},
		struct {
			ID int `rnsql:"id" rntype:"INT" FK:"a.id" FKOnDelete:"CASCADE;DROP TABLE a"`
		}{},
		struct {
			ID int `rnsql:"id" rntype:"INT" rnopt:"NN" FK:"a.id" FKOnDelete:"SET NULL"`
		}{},
		struct {
			ID int `rnsql:"id" rntype:"INT" rnopt:"NN" rndefault:"NULL"`
		}{}, struct {
			ID int `rnsql:"id" rntype:"VARCHAR(20)" rnopt:"AI"`
		}{},
		struct {
			ID    int `rnsql:"id" rntype:"INT" rnopt:"AI"`
			Other int `rnsql:"other" rntype:"INT" rnopt:"AI"`
		}{},
		struct {
			ID int `rnsql:"id" rntype:"INT" FKOnDelete:"CASCADE"`
		}{}, struct {
			id int `rnsql:"id" rntype:"INT"`
		}{},
	}
	for i, model := range cases {
		if _, err := parseMigrationModel(MigrationTable{"t", model}, false); err == nil {
			t.Errorf("case %d accepted invalid model", i)
		}
	}
	type valid struct {
		ID     int    `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
		Ignore string `rnsql:"-"`
		Ref    *int   `rnsql:"ref" rntype:"INT" FK:"p.id" FKOnDelete:"SET NULL"`
	}
	var typedNil *valid
	for _, model := range []interface{}{valid{}, &valid{}, typedNil} {
		m, err := parseMigrationModel(MigrationTable{"t", model}, false)
		if err != nil || len(m.Columns) != 2 || m.Columns[0].Nullable {
			t.Fatalf("valid model: %#v %v", m, err)
		}
	}
}

func TestMigrationDefaultValidationAndComparison(t *testing.T) {
	for _, s := range []string{"NULL", "TRUE", "FALSE", "0", "-12.5", "1e4", "CURRENT_TIMESTAMP", "current_timestamp(6)", "''", "'it''s fine'", "'a\\nb'"} {
		if err := validateMigrationDefault(s, false); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"NOW(); DROP TABLE x", "'a' OR 1=1", "'unterminated", "uuid()", "CURRENT_TIMESTAMP(7)", "NaN", "Inf"} {
		if err := validateMigrationDefault(s, false); err == nil {
			t.Errorf("accepted %s", s)
		}
	}
	for _, tc := range []struct{ want, old string }{{"1", "1.00"}, {"TRUE", "1"}, {"'it''s fine'", "it's fine"}, {"CURRENT_TIMESTAMP", "current_timestamp()"}, {"'a\\nb'", "a\nb"}} {
		if !sameMigrationDefault(tc.want, &migrationColumnState{Default: sql.NullString{String: tc.old, Valid: true}}, false) {
			t.Errorf("did not match %#v", tc)
		}
	}
}

func TestMigrationPlanIntegrityAndOptionValidation(t *testing.T) {
	p := &MigrationPlan{Schema: "test", Steps: []MigrationStep{{SQL: "CREATE TABLE t (id int)"}}}
	p.seal = p.signature()
	if p.SQL() != "CREATE TABLE t (id int);\n" {
		t.Fatal(p.SQL())
	}
	p.Steps[0].SQL = "DROP DATABASE test"
	if err := (*DB)(nil).ApplyMigration(context.Background(), p); !errors.Is(err, ErrMigrationPlanChanged) {
		t.Fatal(err)
	}
	if err := (*DB)(nil).ApplyMigration(context.Background(), nil); !errors.Is(err, ErrMigrationPlanChanged) {
		t.Fatal(err)
	}
	for _, o := range []MigrationOptions{{Algorithm: "instant;drop"}, {Lock: "garbage"}, {Algorithm: "INSTANT", Lock: "NONE"}, {LockWaitTimeout: -time.Second}, {LockWaitTimeout: time.Millisecond}} {
		if _, err := migrationOptions([]MigrationOptions{o}); err == nil {
			t.Errorf("accepted %+v", o)
		}
	}
	if _, err := migrationOptions([]MigrationOptions{{}, {}}); err == nil {
		t.Fatal("accepted multiple options")
	}
	if _, err := (*DB)(nil).PlanMigration(context.Background(), nil, nil); err == nil {
		t.Fatal("nil DB accepted")
	}
}

func TestMigrationForeignKeySemanticComparison(t *testing.T) {
	s := &migrationSchema{Name: "test", Tables: map[string]*migrationTableState{"parent": {Name: "parent", Columns: []*migrationColumnState{{Name: "id"}}}}}
	m := &migrationModel{Name: "child", ForeignKeys: []*migrationModelFK{{Column: "a", RefTable: "parent", RefColumn: "id"}, {Column: "b", RefTable: "parent", RefColumn: "id"}}}
	old := &migrationTableState{ForeignKeys: []*migrationForeignKey{{Schema: "test", Table: "child", Name: "legacy_a", Columns: []string{"a"}, RefSchema: "test", RefTable: "parent", RefColumns: []string{"id"}, DeleteRule: "CASCADE", UpdateRule: "RESTRICT"}, {Schema: "test", Table: "child", Name: "legacy_b", Columns: []string{"b"}, RefSchema: "test", RefTable: "parent", RefColumns: []string{"id"}, DeleteRule: "RESTRICT", UpdateRule: "RESTRICT"}}}
	clauses, _, err := planMigrationForeignKeys(s, m, old, nil, MigrationOptions{})
	if err != nil || len(clauses) != 0 {
		t.Fatalf("equivalent FKs changed: %v %v", clauses, err)
	}
}

func TestMigrationColumnAttributePreservation(t *testing.T) {
	old := &migrationColumnState{Definition: "varchar(10) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'x' COMMENT 'NOT NULL AUTO_INCREMENT DEFAULT x'"}
	c := &migrationModelColumn{Name: "a`b", Type: "varchar(40)", Nullable: false}
	got, err := migrationColumnDefinition(c, old, "utf8mb4", false)
	if err != nil {
		t.Fatal(err)
	}
	want := "`a``b` varchar(40) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT 'x' COMMENT 'NOT NULL AUTO_INCREMENT DEFAULT x'"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestMigrationIndexIdentityAndPrefixComparison(t *testing.T) {
	old := &migrationIndexState{Name: "same", Method: "BTREE", Parts: []migrationIndexPart{{Name: sql.NullString{String: "x", Valid: true}}}}
	want := &DBIndex{TableName: "a", IndexName: "same", IndexType: DBIndexTypeIndex, Columns: []*DBIndexColumn{{ColumnName: "x", ASC: true, SubPart: sql.NullInt64{Int64: 999, Valid: false}}}}
	if !equalMigrationIndex(old, want) {
		t.Fatal("invalid prefix payload must not cause DDL")
	}
	a, _ := publicMigrationIndex("a", old)
	b, _ := publicMigrationIndex("b", old)
	if reflect.DeepEqual(a, b) {
		t.Fatal("table identity lost")
	}
	old.Parts[0].Expression = sql.NullString{String: "lower(x)", Valid: true}
	if _, err := publicMigrationIndex("a", old); err == nil {
		t.Fatal("functional index silently misrepresented")
	}
}

func FuzzMigrationSQLFragments(f *testing.F) {
	for _, s := range []string{"a,b", "enum('a,b','c')", "`a``b` int", "'x\\'y'", "((a,b),c)", "'quote''quote'"} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, noBackslash bool) {
		if len(s) > 8192 {
			t.Skip()
		}
		parts, err := sqlFragments(s, true, noBackslash)
		if err != nil {
			return
		}
		joined := strings.Join(parts, ",")
		again, err := sqlFragments(joined, true, noBackslash)
		if err != nil || !reflect.DeepEqual(parts, again) {
			t.Fatalf("unstable parse %q: %#v %#v %v", s, parts, again, err)
		}
	})
}
