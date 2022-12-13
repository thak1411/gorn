package gorn

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	_ "github.com/go-sql-driver/mysql"
)

type DB struct {
	h      *DBHandler
	Engine string
	conf   *DBConfig
}

// Connect to Database
func (d *DB) Open(conf *DBConfig) error {
	d.conf = conf
	db, err := sql.Open(
		d.Engine,
		fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
			conf.User,
			conf.Password,
			conf.Host,
			conf.Port,
			conf.Schema,
		),
	)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		return err
	}
	db.SetMaxIdleConns(conf.PoolSize)
	db.SetMaxOpenConns(conf.MaxConn)
	db.SetConnMaxLifetime(conf.Lifecycle)

	d.h = &DBHandler{
		DB:        db,
		Container: db,
	}
	return nil
}

// Execute SQL
func (d *DB) Exec(ctx context.Context, sql *Sql, args ...interface{}) (sql.Result, error) {
	return d.h.Container.ExecContext(ctx, sql.Query(), args...)
}

// Execute SQL & Get Multiple Rows
func (d *DB) Query(ctx context.Context, sql *Sql, args ...interface{}) (*sql.Rows, error) {
	return d.h.Container.QueryContext(ctx, sql.Query(), args...)
}

// Execute SQL & Get Single Row
func (d *DB) QueryRow(ctx context.Context, sql *Sql, args ...interface{}) *sql.Row {
	return d.h.Container.QueryRowContext(ctx, sql.Query(), args...)
}

// Prepare SQL
func (d *DB) Prepare(ctx context.Context, sql *Sql) (*sql.Stmt, error) {
	return d.h.Container.PrepareContext(ctx, sql.Query())
}

// Insert Row
func (d *DB) Insert(ctx context.Context, tableName string, table interface{}) (int64, error) {
	sql, params := NewSql().InsertWithParams(tableName, table)
	result, err := d.Exec(ctx, sql, params...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// Select Rows
func (d *DB) Select(ctx context.Context, tableName string, table interface{}, dest interface{}) error {
	sql := NewSql().Select(table).From(tableName)
	rows, err := d.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	return d.ScanRows(rows, dest)
}

// Scan Row
func (d *DB) ScanRow(row *sql.Row, dest interface{}) error {
	target := reflect.ValueOf(dest)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("dest obj must be struct")
	}
	params := make([]interface{}, 0)
	for i := 0; i < target.NumField(); i++ {
		_, ok := target.Type().Field(i).Tag.Lookup("rnsql")
		if ok {
			params = append(params, target.Field(i).Addr().Interface())
		}
	}
	return row.Scan(params...)
}

// Scan Rows
func (d *DB) ScanRows(rows *sql.Rows, dest interface{}) error {
	target := reflect.ValueOf(dest)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Slice {
		panic("dest obj must be slice")
	}
	for rows.Next() {
		var item reflect.Value
		isPointer := false
		if target.Type().Elem().Kind() == reflect.Ptr {
			item = reflect.New(target.Type().Elem().Elem())
			isPointer = true
		} else {
			item = reflect.New(target.Type().Elem())
		}
		params := make([]interface{}, 0)
		for i := 0; i < item.Elem().NumField(); i++ {
			_, ok := item.Elem().Type().Field(i).Tag.Lookup("rnsql")
			if ok {
				params = append(params, item.Elem().Field(i).Addr().Interface())
			}
		}
		if err := rows.Scan(params...); err != nil {
			return err
		}
		if isPointer {
			target.Set(reflect.Append(target, item))
		} else {
			target.Set(reflect.Append(target, item.Elem()))
		}
	}
	return nil
}

// Has Table
func (d *DB) HasTable(tableName string) (bool, error) {
	type Table struct {
		Count int64 `rnsql:"COUNT(TABLE_NAME)"`
	}
	table := &Table{}
	sql := NewSql().
		Select(table).
		From("INFORMATION_SCHEMA.TABLES").
		Where("TABLE_SCHEMA LIKE ?").
		And("TABLE_NAME LIKE ?").
		And("TABLE_TYPE LIKE ?")

	row := d.QueryRow(
		context.Background(),
		sql,
		d.conf.Schema,
		tableName,
		"BASE_TABLE",
	)
	err := d.ScanRow(row, table)
	return table.Count > 0, err
}

// Has Column
func (d *DB) HasColumn(tableName, columnName string) (bool, error) {
	type Column struct {
		Count int64 `rnsql:"COUNT(COLUMN_NAME)"`
	}
	column := &Column{}
	sql := NewSql().
		Select(column).
		From("INFORMATION_SCHEMA.COLUMNS").
		Where("TABLE_SCHEMA LIKE ?").
		And("TABLE_NAME LIKE ?").
		And("COLUMN_NAME LIKE ?")

	row := d.QueryRow(
		context.Background(),
		sql,
		d.conf.Schema,
		tableName,
	)
	err := d.ScanRow(row, column)
	return column.Count > 0, err
}

// Get All Columns
func (d *DB) GetColumns(tableName string) (*[]*DBColumn, error) {
	columns := &[]*DBColumn{}
	sql := NewSql().
		Select(&DBColumn{}).
		From("INFORMATION_SCHEMA.COLUMNS").
		Where("TABLE_SCHEMA LIKE ?").
		And("TABLE_NAME LIKE ?")

	rows, err := d.Query(context.Background(), sql, d.conf.Schema, tableName)
	if err != nil {
		return nil, err
	}
	if err := d.ScanRows(rows, columns); err != nil {
		return nil, err
	}
	return columns, nil
}

// Migration Table
func (d *DB) Migration(tableName string, table interface{}) error {
	if has, err := d.HasTable(tableName); err != nil {
		return err
	} else if has {
		if err := d.AlterTable(tableName, table); err != nil {
			return err
		}
	} else {
		if err := d.CreateTable(tableName, table); err != nil {
			return err
		}
	}
	return nil
}

// Create Table
func (d *DB) CreateTable(tableName string, table interface{}) error {
	sql := NewSql().Create(tableName, table)
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Alter Table
func (d *DB) AlterTable(tableName string, table interface{}) error {
	columns, err := d.GetColumns(tableName)
	if err != nil {
		return err
	}
	columnMap := make(map[string]*DBColumn)
	for _, v := range *columns {
		columnMap[v.ColumnName] = v
	}
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table obj must be struct")
	}
	prevCol := ""
	for i := 0; i < target.NumField(); i++ {
		value := target.Type().Field(i)
		rnsql, ok := value.Tag.Lookup("rnsql")
		if !ok {
			continue
		}
		col, ok := columnMap[rnsql]
		if !ok {
			if err := d.AddColumn(tableName, rnsql, prevCol); err != nil {
				return err
			}
		} else {

		}
		prevCol = rnsql
	}

	return nil
}

// Add Column to Table
func (d *DB) AddColumn(tableName string, columnName, prevCol string) error {
	sql := NewSql().
		Alter().Table(tableName).
		Add().Column(columnName)
	if prevCol != "" {
		sql.After(prevCol)
	} else {
		sql.First()
	}
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Close Database
func (d *DB) Close() error {
	return d.h.Close()
}

// Generate New DB Instance
func NewDB(engine string) *DB {
	return &DB{Engine: engine}
}
