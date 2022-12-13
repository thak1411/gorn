package gorn

import (
	"context"
	"database/sql"
	"time"
)

type GornContext string

const (
	ContextFinish GornContext = "Finish"
)

type DBConfig struct {
	User      string
	Password  string
	Host      string
	Port      int
	Schema    string
	PoolSize  int
	MaxConn   int
	Lifecycle time.Duration
}

type DBContainer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

type DBInterface interface {
	Close() error
}

type DBHandler struct {
	DB        *sql.DB
	Container DBContainer
}

func (h *DBHandler) Close() error {
	return h.DB.Close()
}

type DBColumn struct {
	TableName       string `rnsql:"TABLE_NAME"`
	OrdinalPosition int    `rnsql:"ORDINAL_POSITION"`
	ColumnName      string `rnsql:"COLUMN_NAME"`
	ColumnType      string `rnsql:"COLUMN_TYPE"`
	IsNullable      string `rnsql:"IS_NULLABLE"`
	ColumnKey       string `rnsql:"COLUMN_KEY"`
	Extra           string `rnsql:"EXTRA"`
}

var DBColumnOptions = []string{"BIN", "UN", "NN", "AI"}
var DBColumnOptionName = []string{"BINARY", "UNSIGNED", "NOT NULL", "AUTO_INCREMENT"}
