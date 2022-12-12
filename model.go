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
	ColumnName string `gsql:"COLUMN_NAME"`
	ColumnType string `gsql:"DATA_TYPE"`
}
