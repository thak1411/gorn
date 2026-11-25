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

type DBForeignKey struct {
	TableName            string `rnsql:"k.TABLE_NAME"`
	OrdinalPosition      int    `rnsql:"k.ORDINAL_POSITION"`
	ConstraintName       string `rnsql:"k.CONSTRAINT_NAME"`
	ColumnName           string `rnsql:"k.COLUMN_NAME"`
	ReferencedTableName  string `rnsql:"k.REFERENCED_TABLE_NAME"`
	ReferencedColumnName string `rnsql:"k.REFERENCED_COLUMN_NAME"`
	UpdateRule           string `rnsql:"r.UPDATE_RULE"`
	DeleteRule           string `rnsql:"r.DELETE_RULE"`
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

type DBConfig struct {
	User      string
	Password  string
	Host      string
	Port      int
	Schema    string
	PoolSize  int
	MaxConn   int
	Lifecycle time.Duration
	MaxRetry  int
}

type DB struct {
	h             *DBHandler
	Engine        string
	conf          *DBConfig
	isTransaction bool
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
	newConf := new(DBConfig)
	*newConf = *d.conf
	newConf.MaxRetry = 0
	newHandler := &DB{
		&DBHandler{
			DB:        d.h.DB,
			Container: tx,
		},
		d.Engine,
		newConf,
		true,
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
func (d *DB) Exec(ctx context.Context, tsql *Sql) (sql.Result, error) {
	var res sql.Result
	var err error
	res, err = d.h.Container.ExecContext(ctx, tsql.Query(d.isTransaction), tsql.Params()...)
	if err == nil {
		return res, nil
	}
	return res, err
}

// Execute SQL & Get Multiple Rows
func (d *DB) Query(ctx context.Context, tsql *Sql) (*sql.Rows, error) {
	var res *sql.Rows
	var err error
	res, err = d.h.Container.QueryContext(ctx, tsql.Query(d.isTransaction), tsql.Params()...)
	if err == nil {
		return res, nil
	}
	return res, err
}

// Execute SQL & Get Single Row
func (d *DB) QueryRow(ctx context.Context, tsql *Sql) *sql.Row {
	return d.h.Container.QueryRowContext(ctx, tsql.Query(d.isTransaction), tsql.Params()...)
}

// Prepare SQL
func (d *DB) Prepare(ctx context.Context, tsql *Sql) (*sql.Stmt, error) {
	return d.h.Container.PrepareContext(ctx, tsql.Query(d.isTransaction))
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
	return rows.Err()
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
		Where("TABLE_SCHEMA = ?", d.conf.Schema).
		And("TABLE_NAME = ?", tableName).
		And("TABLE_TYPE = ?", "BASE TABLE")

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
		Where("TABLE_SCHEMA = ?", d.conf.Schema).
		And("TABLE_NAME = ?", tableName).
		And("COLUMN_NAME = ?", columnName)

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
		Where("TABLE_SCHEMA = ?", d.conf.Schema).
		And("TABLE_NAME = ?", tableName).
		And("INDEX_NAME = ?", indexName)

	row := d.QueryRow(
		context.Background(),
		sql,
	)
	err := d.ScanRow(row, index)
	return index.Count > 0, err
}

// Get All Columns
func (d *DB) GetColumns(tableName string) ([]*DBColumn, error) {
	columns := []*DBColumn{}
	sql := NewSql().
		Select(&DBColumn{}).
		From("INFORMATION_SCHEMA.COLUMNS").
		Where("TABLE_SCHEMA = ?", d.conf.Schema).
		And("TABLE_NAME = ?", tableName).
		OrderBy("ORDINAL_POSITION").ASC()

	rows, err := d.Query(context.Background(), sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if err := d.ScanRows(rows, &columns); err != nil {
		return nil, err
	}
	return columns, nil
}

// Get All Foreign Keys
func (d *DB) GetForeignKeys(tableName string) ([]*DBForeignKey, error) {
	foreignKeys := []*DBForeignKey{}
	sql := NewSql().
		Select(&DBForeignKey{}).
		From("INFORMATION_SCHEMA.KEY_COLUMN_USAGE").As("k").
		InnerJoin("INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS").As("r").
		On("k.CONSTRAINT_NAME = r.CONSTRAINT_NAME").
		And("k.CONSTRAINT_SCHEMA = r.CONSTRAINT_SCHEMA").
		And("k.TABLE_NAME = r.TABLE_NAME").
		Where("k.TABLE_SCHEMA = ?", d.conf.Schema).
		And("k.TABLE_NAME = ?", tableName).
		And("k.REFERENCED_TABLE_NAME IS NOT NULL").
		OrderBy("k.CONSTRAINT_NAME, k.ORDINAL_POSITION").ASC()

	rows, err := d.Query(context.Background(), sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if err := d.ScanRows(rows, &foreignKeys); err != nil {
		return nil, err
	}
	return foreignKeys, nil
}

// GetIndexes returns ordinary secondary BTREE indexes. Features that DBIndex
// cannot represent (expressions, visibility, other methods) return an error
// instead of an incomplete definition that a caller might accidentally drop.
func (d *DB) GetIndexes() ([]*DBIndex, error) {
	type indexRow struct {
		Table      string         `rnsql:"TABLE_NAME"`
		Name       string         `rnsql:"INDEX_NAME"`
		Method     string         `rnsql:"INDEX_TYPE"`
		Visible    string         `rnsql:"IS_VISIBLE"`
		NonUnique  bool           `rnsql:"NON_UNIQUE"`
		Column     sql.NullString `rnsql:"COLUMN_NAME"`
		Expression sql.NullString `rnsql:"EXPRESSION"`
		Prefix     sql.NullInt64  `rnsql:"SUB_PART"`
		Collation  sql.NullString `rnsql:"COLLATION"`
	}
	query := NewSql().Select(&indexRow{}).From("INFORMATION_SCHEMA.STATISTICS").
		Where("TABLE_SCHEMA = ?", d.conf.Schema).
		And("INDEX_NAME <> ?", "PRIMARY").
		And("LEFT(INDEX_NAME, 8) <> ?", "GORN_FK_").
		OrderBy("TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX")
	rows, err := d.Query(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var raw []*indexRow
	if err := d.ScanRows(rows, &raw); err != nil {
		return nil, err
	}
	var result []*DBIndex
	for i := 0; i < len(raw); {
		first := raw[i]
		idx := &migrationIndexState{Name: first.Name, Method: first.Method, Invisible: first.Visible == "NO", Unique: !first.NonUnique}
		for i < len(raw) && raw[i].Table == first.Table && raw[i].Name == first.Name {
			row := raw[i]
			idx.Parts = append(idx.Parts, migrationIndexPart{Name: row.Column, Expression: row.Expression, Prefix: row.Prefix, Descending: row.Collation.String == "D"})
			i++
		}
		index, err := publicMigrationIndex(first.Table, idx)
		if err != nil {
			return nil, err
		}
		result = append(result, index)
	}
	return result, nil
}

// Migration applies the conservative migration engine to one table.
// Use PlanMigration for a dry run or Migrate for multiple related tables.
func (d *DB) Migration(tableName string, table interface{}) error {
	_, err := d.Migrate(context.Background(), []MigrationTable{{Name: tableName, Model: table}}, nil)
	return err
}

// MigrationIndex changes only the supplied indexes; omitted indexes are kept.
func (d *DB) MigrationIndex(indexes []*DBIndex) error {
	_, err := d.Migrate(context.Background(), nil, indexes)
	return err
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

// AlterTable uses the same conservative diff as Migration. Column order and
// unmodeled columns are preserved; dangerous changes require an explicit plan.
func (d *DB) AlterTable(tableName string, table interface{}) error {
	return d.Migration(tableName, table)
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

// Drop Foreign Key
func (d *DB) DropForeignKey(tableName, foreignKey string) error {
	sql := NewSql().
		Alter().Table(tableName).
		DropForeignKey(foreignKey)
	if res, err := d.Exec(context.Background(), sql); err != nil {
		return err
	} else if _, err := res.RowsAffected(); err != nil {
		return err
	}

	return nil
}

// Add Foreign Key
func (d *DB) AddForeignKey(tableName string, foreignKey *DBForeignKey) error {
	sql := NewSql().
		Alter().Table(tableName).
		AddForeignKey(foreignKey)
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
	return &DB{Engine: engine, isTransaction: false}
}
