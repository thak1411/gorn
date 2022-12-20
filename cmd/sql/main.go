package main

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/thak1411/gorn"
)

type User struct {
	Id   int64  `rnsql:"id" rntype:"INT" rnopt:"PK NN AI"`
	Name string `rnsql:"name" rntype:"VARCHAR(20)" rnopt:"NN"`
}

type Admin struct {
	UserId int64 `rnsql:"user_id" rntype:"INT" FK:"user.id" rnopt:"PK NN"`
}

type TestTable struct {
	Col  int64         `rnsql:"col" rntype:"INT" rnopt:"PK NN UQ UN AI"`
	CCol string        `rnsql:"c_col" rntype:"VARCHAR(22)" rnopt:"NN BIN"`
	Abc  int           `rnsql:"abc" rntype:"INT" rnopt:"NN"`
	Def  int           `rnsql:"def" rntype:"INT" rnopt:"NN"`
	Deff string        `rnsql:"deff" rntype:"VARCHAR(22)" rnopt:"NN"`
	Nn   sql.NullInt64 `rnsql:"nn" rntype:"INT" rnopt:""`
}

func main() {
	db := gorn.NewDB("mysql")
	err := db.Open(&gorn.DBConfig{
		User:      "root",
		Password:  "pass",
		Host:      "localhost",
		Port:      8806,
		Schema:    "gorn",
		PoolSize:  10,
		MaxConn:   100,
		Lifecycle: 7 * time.Hour,
	})
	if err != nil {
		panic(err)
	}

	if err := db.Migration("test_table", &TestTable{}); err != nil {
		panic(err)
	}
	columns, err := db.GetColumns("test_table")
	if err != nil {
		panic(err)
	}
	for _, v := range *columns {
		fmt.Println(v)
	}

	res, err := db.HasIndex("test_table", "index_test")
	if err != nil {
		panic(err)
	}
	fmt.Println(res)
	if err := db.MigrationIndex(
		[]*gorn.DBIndex{
			{
				TableName: "test_table",
				IndexName: "index_test",
				IndexType: gorn.DBIndexTypeIndex,
				Columns: []*gorn.DBIndexColumn{
					{
						ColumnName: "col",
						ASC:        true,
					},
					{
						ColumnName: "c_col",
						SubPart: sql.NullInt64{
							Int64: 10,
							Valid: true,
						},
						ASC: false,
					},
				},
			},
		},
	); err != nil {
		panic(err)
	}
	idx, err := db.GetIndexes()
	if err != nil {
		panic(err)
	}
	for _, v := range *idx {
		fmt.Println(v)
		for _, w := range v.Columns {
			fmt.Println(w)
		}
	}

	// db.Insert(
	// 	context.Background(),
	// 	"test_table",
	// 	&TestTable{
	// 		Col:  0,
	// 		CCol: "test",
	// 		Abc:  2,
	// 		Def:  3,
	// 		Deff: "4",
	// 	},
	// )
	// db.Insert(
	// 	context.Background(),
	// 	"test_table",
	// 	&TestTable{
	// 		Col:  0,
	// 		CCol: "test2",
	// 		Abc:  4,
	// 		Def:  6,
	// 		Deff: "8",
	// 	},
	// )
	// results := make([]*TestTable, 0)
	// if err := db.Select(
	// 	context.Background(),
	// 	"test_table",
	// 	&TestTable{},
	// 	&results,
	// ); err != nil {
	// 	panic(err)
	// }
	// for _, v := range results {
	// 	fmt.Println(v)
	// }
	// if err := db.DropTable("test_table"); err != nil {
	// 	panic(err)
	// }
	// id, err := db.Insert(
	// 	context.Background(),
	// 	"test_table", &TestTable{
	// 		Col:  0,
	// 		CCol: "test",
	// 		Abc:  2,
	// 		Def:  3,
	// 		Deff: 4,
	// 	},
	// )
	// if err != nil {
	// 	panic(err)
	// }
	// fmt.Println(id)

	// if err := db.Migration("user", User{
	// 	Name: "default value",
	// }); err != nil {
	// 	panic(err)
	// }
	// if err := db.Migration("admin", Admin{}); err != nil {
	// 	panic(err)
	// }

	// userId, err := db.Insert(
	// 	context.Background(),
	// 	"user",
	// 	&User{
	// 		Name: "thak1411",
	// 	},
	// )
	// if err != nil {
	// 	panic(err)
	// }
	// userId2, err := db.Insert(
	// 	context.Background(),
	// 	"user",
	// 	&User{
	// 		Name: "thak1311",
	// 	},
	// )
	// if err != nil {
	// 	panic(err)
	// }
	// insertId, err := db.Insert(
	// 	context.Background(),
	// 	"admin",
	// 	&Admin{
	// 		UserId: userId2,
	// 	},
	// )
	// if err != nil {
	// 	panic(err)
	// }
	// fmt.Println("insert result:", userId, userId2, insertId)

	// user := &User{}
	// row := db.QueryRow(
	// 	context.Background(),
	// 	gorn.NewSql().Select(user).From("user").Limit(1),
	// )
	// if err := db.ScanRow(row, user); err != nil {
	// 	panic(err)
	// }
	// fmt.Println(user)

	// columns, err = db.GetColumns("user")
	// if err != nil {
	// 	panic(err)
	// }
	// for _, v := range *columns {
	// 	fmt.Println(v)
	// }
}
