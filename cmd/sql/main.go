package main

import (
	"context"
	"fmt"
	"time"

	"github.com/thak1411/gorn"
)

type User struct {
	Id   int64  `rnsql:"id" rntype:"INT" PK:"" NN:"" AI:""`
	Name string `rnsql:"name" rntype:"VARCHAR(20)" NN:""`
}

type Admin struct {
	UserId int64 `rnsql:"user_id" rntype:"INT" FK:"user" FK_REF:"id" PK:"" NN:""`
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

	if err := db.Migration("user", User{
		Name: "default value",
	}); err != nil {
		panic(err)
	}
	if err := db.Migration("admin", Admin{}); err != nil {
		panic(err)
	}

	userId, err := db.Insert(
		context.Background(),
		"user",
		&User{
			Name: "thak1411",
		},
	)
	if err != nil {
		panic(err)
	}
	userId2, err := db.Insert(
		context.Background(),
		"user",
		&User{
			Name: "thak1311",
		},
	)
	if err != nil {
		panic(err)
	}
	insertId, err := db.Insert(
		context.Background(),
		"admin",
		&Admin{
			UserId: userId2,
		},
	)
	if err != nil {
		panic(err)
	}
	fmt.Println("insert result:", userId, userId2, insertId)

	user := &User{}
	row := db.QueryRow(
		context.Background(),
		gorn.NewSql().Select(user).From("user").Limit(1),
	)
	if err := db.ScanRow(row, user); err != nil {
		panic(err)
	}
	fmt.Println(user)
}
