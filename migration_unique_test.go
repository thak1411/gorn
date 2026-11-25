package gorn

import (
	"fmt"
	"testing"
)

func TestMySQLMigrationUniqueDefinitionsAreExplicit(t *testing.T) {
	type row struct {
		ID int `rnsql:"id" rntype:"INT" rnopt:"PK AI"`
		A  int `rnsql:"a" rntype:"INT" rnopt:"NN UQ"`
		B  int `rnsql:"b" rntype:"INT" rnopt:"NN UQ"`
	}
	for _, mode := range []string{"annotation_only", "pair", "independent"} {
		t.Run(mode, func(t *testing.T) {
			db := newMigrationTestDB(t)
			tables := []MigrationTable{{"entries", row{}}}
			var indexes []*DBIndex
			switch mode {
			case "pair":
				indexes = []*DBIndex{migrationIndex("entries", "pair_unique", DBIndexTypeUnique, "a", "b")}
			case "independent":
				indexes = []*DBIndex{migrationIndex("entries", "a_unique", DBIndexTypeUnique, "a"), migrationIndex("entries", "b_unique", DBIndexTypeUnique, "b")}
			}
			db.apply(t, db.plan(t, tables, indexes))
			db.exec(t, "INSERT INTO entries(a,b) VALUES(1,2)")
			for _, pair := range [][2]int{{1, 3}, {4, 2}, {1, 2}} {
				_, err := db.h.DB.Exec("INSERT INTO entries(a,b) VALUES(?,?)", pair[0], pair[1])
				wantError := mode == "independent" || (mode == "pair" && pair == [2]int{1, 2})
				if (err != nil) != wantError {
					t.Fatalf("%s %v: err=%v, wantError=%v", mode, pair, err, wantError)
				}
			}
			want := len(indexes)
			got := db.scalar(t, "SELECT COUNT(DISTINCT INDEX_NAME) FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME='entries' AND NON_UNIQUE=0 AND INDEX_NAME<>'PRIMARY'", db.name)
			if got != fmt.Sprint(want) {
				t.Fatalf("unexpected unique constraints: %s, want %d", got, want)
			}
			db.assertNoop(t, tables, indexes)
		})
	}
}
