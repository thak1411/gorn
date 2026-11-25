package gorn

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Generate independent models with repeated index names and a cycle of foreign
// keys. No application source, schema dump, configuration or fixture is used.
func TestMySQLMigrationLargeSchemaMinimalChange(t *testing.T) {
	db := newMigrationTestDB(t)
	const tableCount = 32
	var tables []MigrationTable
	var indexes []*DBIndex
	for i := 0; i < tableCount; i++ {
		name := fmt.Sprintf("node_%02d", i)
		parent := fmt.Sprintf("node_%02d.id", (i+tableCount-1)%tableCount)
		fields := []reflect.StructField{
			{Name: "ID", Type: reflect.TypeOf(int64(0)), Tag: `rnsql:"id" rntype:"BIGINT" rnopt:"PK AI"`},
			{Name: "ParentID", Type: reflect.TypeOf(int64(0)), Tag: reflect.StructTag(fmt.Sprintf(`rnsql:"parent_id" rntype:"BIGINT" FK:%q`, parent))},
			{Name: "Label", Type: reflect.TypeOf(""), Tag: `rnsql:"label" rntype:"VARCHAR(96)" rnopt:"NN" rndefault:"'initial'"`},
			{Name: "Enabled", Type: reflect.TypeOf(false), Tag: `rnsql:"enabled" rntype:"BOOLEAN" rnopt:"NN" rndefault:"TRUE"`},
			{Name: "Score", Type: reflect.TypeOf(float64(0)), Tag: `rnsql:"score" rntype:"DECIMAL(12,2)" rndefault:"0"`},
			{Name: "Created", Type: reflect.TypeOf(""), Tag: `rnsql:"created" rntype:"DATETIME(6)"`},
			{Name: "Payload", Type: reflect.TypeOf(""), Tag: `rnsql:"payload" rntype:"JSON"`},
		}
		tables = append(tables, MigrationTable{Name: name, Model: reflect.New(reflect.StructOf(fields)).Interface()})
		indexes = append(indexes,
			migrationIndex(name, "label_index", DBIndexTypeIndex, "label"),
			migrationIndex(name, "parent_index", DBIndexTypeIndex, "parent_id", "id"),
		)
	}
	db.apply(t, db.plan(t, tables, indexes))
	before := make(map[string]string, tableCount)
	for _, table := range tables {
		db.exec(t, "INSERT INTO "+quoteMigrationIdentifier(table.Name)+" (label) VALUES (?)", table.Name)
		before[table.Name] = db.definition(t, table.Name)
	}
	for _, table := range tables {
		db.exec(t, "UPDATE "+quoteMigrationIdentifier(table.Name)+" SET parent_id = 1")
	}
	db.assertNoop(t, tables, indexes)

	const changed = 17
	typ := reflect.TypeOf(tables[changed].Model).Elem()
	fields := make([]reflect.StructField, typ.NumField())
	for i := range fields {
		fields[i] = typ.Field(i)
	}
	fields = append(fields, reflect.StructField{Name: "Note", Type: reflect.TypeOf(""), Tag: `rnsql:"note" rntype:"VARCHAR(32)"`})
	tables[changed].Model = reflect.New(reflect.StructOf(fields)).Interface()
	plan := db.plan(t, tables, indexes)
	if len(plan.Steps) != 1 || plan.Steps[0].Table != tables[changed].Name ||
		strings.Count(plan.SQL(), "ADD COLUMN") != 1 || strings.Contains(plan.SQL(), "DROP") || strings.Contains(plan.SQL(), "MODIFY") {
		t.Fatalf("one-column addition changed unrelated objects:\n%s", plan.SQL())
	}
	db.apply(t, plan)
	for i, table := range tables {
		if i != changed && db.definition(t, table.Name) != before[table.Name] {
			t.Fatalf("unrelated table %s changed", table.Name)
		}
		got := db.scalar(t, "SELECT CONCAT(id, ':', parent_id, ':', label, ':', enabled) FROM "+quoteMigrationIdentifier(table.Name))
		if got != "1:1:"+table.Name+":1" {
			t.Fatalf("data changed in %s: %s", table.Name, got)
		}
	}
	if got := db.scalar(t, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=?", db.name); got != fmt.Sprint(tableCount) {
		t.Fatalf("foreign-key count changed: %s", got)
	}
	db.assertNoop(t, tables, indexes)
}
