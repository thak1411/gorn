package gorn

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type DBContainer interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
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
var DBColumnOptionsWithoutAI = []string{"BIN", "UN", "NN"}
var DBColumnOptionNameWithoutAI = []string{"BINARY", "UNSIGNED", "NOT NULL"}

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

const (
	DBIndexTypeUnique = "UNIQUE"
	DBIndexTypeIndex  = "INDEX"
)

type DBIndexColumn struct {
	ColumnName string
	SubPart    sql.NullInt64
	ASC        bool
}

type DBIndex struct {
	TableName string
	IndexName string
	IndexType string
	Columns   []*DBIndexColumn
}

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

// Begin Transaction
func (d *DB) BeginTx(ctx context.Context) (*DB, error) {
	tx, err := d.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	newHandler := &DB{
		&DBHandler{
			DB:        d.h.DB,
			Container: tx,
		},
		d.Engine,
		d.conf,
	}
	return newHandler, nil
}

// Commit Transaction
func (d *DB) CommitTx() error {
	tx, ok := d.h.Container.(*sql.Tx)
	if !ok {
		return fmt.Errorf("gorn: commit fail - not transaction")
	}
	return tx.Commit()
}

// Rollback Transaction
func (d *DB) RollbackTx() error {
	tx, ok := d.h.Container.(*sql.Tx)
	if !ok {
		return fmt.Errorf("gorn: rollback fail - not transaction")
	}
	return tx.Rollback()
}

// Execute Transaction
func (d *DB) ExecTx(ctx context.Context, fn func(txdb *DB) error) error {
	newHandler, err := d.BeginTx(ctx)
	if err != nil {
		return err
	}
	err = fn(newHandler)
	if err != nil {
		if rbErr := newHandler.RollbackTx(); rbErr != nil {
			return rbErr
		}
		return err
	}
	return newHandler.CommitTx()
}

// Execute SQL
func (d *DB) Exec(ctx context.Context, sql *Sql) (sql.Result, error) {
	return d.h.Container.ExecContext(ctx, sql.Query(), sql.Params()...)
}

// Execute SQL & Get Multiple Rows
func (d *DB) Query(ctx context.Context, sql *Sql) (*sql.Rows, error) {
	return d.h.Container.QueryContext(ctx, sql.Query(), sql.Params()...)
}

// Execute SQL & Get Single Row
func (d *DB) QueryRow(ctx context.Context, sql *Sql) *sql.Row {
	return d.h.Container.QueryRowContext(ctx, sql.Query(), sql.Params()...)
}

// Prepare SQL
func (d *DB) Prepare(ctx context.Context, sql *Sql) (*sql.Stmt, error) {
	return d.h.Container.PrepareContext(ctx, sql.Query())
}

// Insert Row
func (d *DB) Insert(ctx context.Context, tableName string, table interface{}) error {
	sql := NewSql().Insert(tableName, table)
	result, err := d.Exec(ctx, sql)
	if err != nil {
		return err
	}
	_, err = result.RowsAffected()
	return err
}

// Insert Row & Return Last Insert Id
func (d *DB) InsertWithLastId(ctx context.Context, tableName string, table interface{}) (int64, error) {
	sql := NewSql().Insert(tableName, table)
	result, err := d.Exec(ctx, sql)
	if err != nil {
		return 0, err
	}
	if _, err = result.RowsAffected(); err != nil {
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
		Where("TABLE_SCHEMA LIKE ?", d.conf.Schema).
		And("TABLE_NAME LIKE ?", tableName).
		And("TABLE_TYPE LIKE ?", "BASE_TABLE")

	row := d.QueryRow(
		context.Background(),
		sql,
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
		Where("TABLE_SCHEMA LIKE ?", d.conf.Schema).
		And("TABLE_NAME LIKE ?", tableName).
		And("COLUMN_NAME LIKE ?", columnName)

	row := d.QueryRow(
		context.Background(),
		sql,
	)
	err := d.ScanRow(row, column)
	return column.Count > 0, err
}

// Has Index
func (d *DB) HasIndex(tableName, indexName string) (bool, error) {
	type Index struct {
		Count int64 `rnsql:"COUNT(INDEX_NAME)"`
	}
	index := &Index{}
	sql := NewSql().
		Select(index).
		From("INFORMATION_SCHEMA.STATISTICS").
		Where("TABLE_SCHEMA LIKE ?", d.conf.Schema).
		And("TABLE_NAME LIKE ?", tableName).
		And("INDEX_NAME LIKE ?", indexName)

	row := d.QueryRow(
		context.Background(),
		sql,
	)
	err := d.ScanRow(row, index)
	return index.Count > 0, err
}

// Get All Columns
func (d *DB) GetColumns(tableName string) (*[]*DBColumn, error) {
	columns := &[]*DBColumn{}
	sql := NewSql().
		Select(&DBColumn{}).
		From("INFORMATION_SCHEMA.COLUMNS").
		Where("TABLE_SCHEMA LIKE ?", d.conf.Schema).
		And("TABLE_NAME LIKE ?", tableName)

	rows, err := d.Query(context.Background(), sql)
	if err != nil {
		return nil, err
	}
	if err := d.ScanRows(rows, columns); err != nil {
		return nil, err
	}
	return columns, nil
}

// Get All Indexes
func (d *DB) GetIndexes() (*[]*DBIndex, error) {
	result := []*DBIndex{}
	type Indexes struct {
		TableName   string        `rnsql:"TABLE_NAME"`
		IndexName   string        `rnsql:"INDEX_NAME"`
		SeqInIndex  int64         `rnsql:"SEQ_IN_INDEX"`
		ColumnName  string        `rnsql:"COLUMN_NAME"`
		IsNotUnique bool          `rnsql:"NON_UNIQUE"`
		Collation   string        `rnsql:"COLLATION"`
		SubPart     sql.NullInt64 `rnsql:"SUB_PART"`
	}
	indexes := &[]*Indexes{}
	sql := NewSql().
		Select(&Indexes{}).
		From("INFORMATION_SCHEMA.STATISTICS").
		Where("TABLE_SCHEMA LIKE ?", d.conf.Schema).
		And("INDEX_NAME NOT LIKE ?", "PRIMARY").
		And("INDEX_NAME NOT LIKE ?", "GORN_FK_%"). // GORN_FK_... is Default Foreign Key Index
		OrderBy("TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX")

	rows, err := d.Query(context.Background(), sql)
	if err != nil {
		return nil, err
	}
	if err := d.ScanRows(rows, indexes); err != nil {
		return nil, err
	}
	prevIndexName := "__gorn_trash_value__!&#*#&"
	for _, index := range *indexes {
		// Append New Index
		if index.IndexName != prevIndexName {
			indexType := DBIndexTypeUnique
			if index.IsNotUnique {
				indexType = DBIndexTypeIndex
			}
			result = append(
				result,
				&DBIndex{
					TableName: index.TableName,
					IndexName: index.IndexName,
					IndexType: indexType,
					Columns:   make([]*DBIndexColumn, 0),
				},
			)
		}
		prevIndexName = index.IndexName
		// Append Index Column
		asc := true
		if index.Collation == "D" {
			asc = false
		}
		result[len(result)-1].Columns = append(
			result[len(result)-1].Columns,
			&DBIndexColumn{
				ColumnName: index.ColumnName,
				SubPart:    index.SubPart,
				ASC:        asc,
			},
		)
	}
	return &result, nil
}

// Migration Table
func (d *DB) Migration(tableName string, table interface{}) error {
	// Make Table
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

// Migration Index
func (d *DB) MigrationIndex(indexes []*DBIndex) error {
	// Get All Indexes
	oldIndexes, err := d.GetIndexes()
	if err != nil {
		return err
	}
	oldIndexMap := make(map[string]map[string]*DBIndex)
	indexMap := make(map[string]map[string]bool)
	// Make Old Index Map
	for _, index := range *oldIndexes {
		if _, ok := oldIndexMap[index.TableName]; !ok {
			oldIndexMap[index.TableName] = make(map[string]*DBIndex)
		}
		oldIndexMap[index.TableName][index.IndexName] = index
	}

	for _, index := range indexes {
		// Make Index
		if has, err := d.HasIndex(index.TableName, index.IndexName); err != nil {
			return err
		} else if has {
			// If Index Was Not Changed, Then Skip
			if reflect.DeepEqual(oldIndexMap[index.TableName][index.IndexName], index) {
				continue
			}
			if err := d.DropIndex(index); err != nil {
				return err
			}
		}
		// If Index Was Created, Then Drop Index & Create Index
		if err := d.CreateIndex(index); err != nil {
			return err
		}
		if _, ok := indexMap[index.TableName]; !ok {
			indexMap[index.TableName] = make(map[string]bool)
		}
		indexMap[index.TableName][index.IndexName] = true
	}
	// Drop Index
	for _, oldIndex := range *oldIndexes {
		dropFlag := false
		if _, ok := indexMap[oldIndex.TableName]; !ok {
			dropFlag = true
		} else if _, ok := indexMap[oldIndex.TableName][oldIndex.IndexName]; !ok {
			dropFlag = true
		}
		if dropFlag {
			if err := d.DropIndex(oldIndex); err != nil {
				return err
			}
		}
	}
	return nil
}

// Create Index
func (d *DB) CreateIndex(index *DBIndex) error {
	isUnique := false
	if index.IndexType == DBIndexTypeUnique {
		isUnique = true
	}
	columnNames := make([]string, 0)
	columnOrders := make([]string, 0)
	columnSubParts := make([]sql.NullInt64, 0)
	for _, column := range index.Columns {
		columnNames = append(columnNames, column.ColumnName)
		columnSubParts = append(columnSubParts, column.SubPart)
		if column.ASC {
			columnOrders = append(columnOrders, "ASC")
		} else {
			columnOrders = append(columnOrders, "DESC")
		}
	}
	sql := NewSql().Alter().Table(index.TableName).
		AddIndex(index.IndexName, columnNames, columnSubParts, columnOrders, isUnique)

	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Drop Index
func (d *DB) DropIndex(index *DBIndex) error {
	sql := NewSql().Alter().Table(index.TableName).DropIndex(index.IndexName)
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Create Table
func (d *DB) CreateTable(tableName string, table interface{}) error {
	sql := NewSql().CreateTable(tableName, table)
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Drop Table
func (d *DB) DropTable(tableName string) error {
	sql := NewSql().Drop().Table(tableName)
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Alter Table
func (d *DB) AlterTable(tableName string, table interface{}) error {
	// Get Columns
	columns, err := d.GetColumns(tableName)
	if err != nil {
		return err
	}
	// Make Column Map
	columnMap := make(map[string]*DBColumn)
	oldPkeys := make([]string, 0)
	for _, v := range *columns {
		columnMap[v.ColumnName] = v
		if v.ColumnKey == "PRI" {
			oldPkeys = append(oldPkeys, v.ColumnName)
		}
	}
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table obj must be struct")
	}
	prevCol := ""
	pkeys := make([]string, 0)
	modifyValue := make([]string, 0)
	for i := 0; i < target.NumField(); i++ {
		value := target.Type().Field(i)
		rnsql, ok := value.Tag.Lookup("rnsql")
		if !ok {
			continue
		}
		rntype, ok := value.Tag.Lookup("rntype")
		if !ok {
			panic("rntype is required")
		}
		rnopt, ok := value.Tag.Lookup("rnopt")
		if !ok {
			rnopt = ""
		}
		_, ok = columnMap[rnsql]

		// (Add | Modify) Column Without Auto Increase Option

		// If Column Not Exist Then Add Column
		if !ok {
			if hasPkey, err := d.AddColumn(tableName, rnsql, rntype, rnopt, prevCol, target.Field(i).Interface(), false); err != nil {
				return err
			} else if hasPkey {
				pkeys = append(pkeys, rnsql)
			}
		}
		// Add Column Have Default Value Options
		// So, We Have to Change It to The Original Options

		// If Change Primary Key And Column Has AI Option Then
		// Remove AI Option And Change Primary Key First
		// Then Add AI Option To Column
		modifyValue = append(modifyValue, tableName, rnsql, rntype, rnopt, prevCol)
		if hasPkey, err := d.ModifyColumn(tableName, rnsql, rntype, rnopt, prevCol, false); err != nil {
			return err
		} else if hasPkey {
			pkeys = append(pkeys, rnsql)
		}
		columnMap[rnsql] = nil
		prevCol = rnsql
	}
	// Remove Old Column
	for k, v := range columnMap {
		if v == nil {
			continue
		}
		if err := d.DropColumn(tableName, k); err != nil {
			return err
		}
	}
	// If Primary Key Changed Then Change Primary Key
	if !reflect.DeepEqual(oldPkeys, pkeys) {
		if len(oldPkeys) > 0 {
			if err := d.DropPrimaryKey(tableName); err != nil {
				return err
			}
		}
		if err := d.AddPrimaryKey(tableName, pkeys); err != nil {
			return err
		}
	}
	// Add AI Option
	for i := 0; i < len(modifyValue); i += 5 {
		if _, err := d.ModifyColumn(
			modifyValue[i],
			modifyValue[i+1],
			modifyValue[i+2],
			modifyValue[i+3],
			modifyValue[i+4],
			true,
		); err != nil {
			return err
		}
	}
	return nil
}

// Add Column to Table With Default Value
// Return Column Has Primary Key Option
func (d *DB) AddColumn(tableName string, columnName, columnType, columnOptions, prevCol string, defaultValue interface{}, withAI bool) (bool, error) {
	options, hasPkey := ParseOptions(columnOptions, withAI)

	sql := NewSql().
		Alter().Table(tableName).
		AddColumn(columnName, columnType, options).
		Default(defaultValue)
	if prevCol != "" {
		sql.After(prevCol)
	} else {
		sql.First()
	}
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return false, err
	} else if _, err := res.RowsAffected(); err != nil {
		return false, err
	}
	return hasPkey, nil
}

// Modify Column
// Return Column Has Primary Key Option
func (d *DB) ModifyColumn(tableName string, columnName, columnType, columnOptions, prevCol string, withAI bool) (bool, error) {
	options, hasPkey := ParseOptions(columnOptions, withAI)

	sql := NewSql().
		Alter().Table(tableName).
		ModifyColumn(columnName, columnType, options)
	if prevCol != "" {
		sql.After(prevCol)
	} else {
		sql.First()
	}
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return false, err
	} else if _, err := res.RowsAffected(); err != nil {
		return false, err
	}
	return hasPkey, nil
}

// Drop Column
func (d *DB) DropColumn(tableName, columnName string) error {
	sql := NewSql().
		Alter().Table(tableName).
		DropColumn(columnName)
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Drop Primary Key
func (d *DB) DropPrimaryKey(tableName string) error {
	sql := NewSql().
		Alter().Table(tableName).
		DropPrimaryKey()
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}
	return nil
}

// Add Primary Key
func (d *DB) AddPrimaryKey(tableName string, columns []string) error {
	sql := NewSql().
		Alter().Table(tableName).
		AddPrimaryKey(columns)
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
