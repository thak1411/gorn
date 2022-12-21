package gorn

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

type Sql struct {
	query  string
	params []interface{}
}

// Add Select Clause
// Example:
// "SELECT table.a, table.b, table.c, table.d ... "
func (s *Sql) Select(table interface{}) *Sql {
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table must be struct")
	}
	s.query += "SELECT "
	for i := 0; i < target.NumField(); i++ {
		rnsql, ok := target.Type().Field(i).Tag.Lookup("rnsql")
		if ok {
			s.query += rnsql + ", "
		}
	}
	s.query = s.query[:len(s.query)-2] + " "
	return s
}

func stringToWordMap(str string) map[string]bool {
	result := make(map[string]bool)
	for _, v := range strings.Fields(str) {
		result[v] = true
	}
	return result
}

// Parse Data Type Options
// If withAI is true, "AI" option will be parsed
// Else "AI" option will be ignored
// Example: "NN AI NOT_A_OPTION" to "NOT NULL AUTO_INCREMENT"
func ParseOptions(options string, withAI bool) (string, bool) {
	smap := stringToWordMap(options)
	result := ""
	hasPkey := smap["PK"]
	var opt []string
	var optName []string
	if withAI {
		opt = DBColumnOptions
		optName = DBColumnOptionName
	} else {
		opt = DBColumnOptionsWithoutAI
		optName = DBColumnOptionNameWithoutAI
	}
	for i, option := range opt {
		if smap[option] {
			result += optName[i] + " "
		}
	}
	return result, hasPkey
}

// Add Create Table Clause
//
// Create Table from struct
// Example:
// type TestTable struct {
// 	Id   int64  `rnsql:"id" rntype:"INT" rnopt:"PK NN AI"`
// 	Name string `rnsql:"name" rntype:"VARCHAR(255)" rnopt:"NN"`
// }
//
// ->
//
// Create Table `table_name` (
// 	`id` INT NOT NULL AUTO_INCREMENT,
// 	`name` VARCHAR(255) NOT NULL,
// 	PRIMARY KEY (`id`)
// )
func (s *Sql) CreateTable(tableName string, table interface{}) *Sql {
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table must be struct")
	}
	primaryKey := []string{}
	foreignKey := []string{}
	s.query += fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` ( ", tableName)
	for i := 0; i < target.NumField(); i++ {
		tag := target.Type().Field(i).Tag
		rnsql, ok := tag.Lookup("rnsql")
		rnsql = "`" + rnsql + "`"
		if ok {
			if rntype, ok := tag.Lookup("rntype"); ok {
				s.query += rnsql + " " + rntype + " "
			} else {
				panic("rntype not found")
			}
			options, hasPkey := ParseOptions(tag.Get("rnopt"), true)
			s.query += options + ", "
			if hasPkey {
				primaryKey = append(primaryKey, rnsql)
			}
			if fk, ok := tag.Lookup("FK"); ok {
				spt := strings.Split(fk, ".")
				if len(spt) != 2 {
					panic("FK must be in format `table.column`")
				}
				foreignKey = append(foreignKey, rnsql, spt[0], spt[1])
			}
		}
	}
	if len(primaryKey) > 0 {
		s.query += "PRIMARY KEY (" + strings.Join(primaryKey, ", ") + "), "
	}
	for i := 0; i < len(foreignKey); i += 3 {
		s.query += fmt.Sprintf("CONSTRAINT `RN_FK_%s` ", tableName) +
			fmt.Sprintf("FOREIGN KEY (%s) ", foreignKey[i]) +
			fmt.Sprintf("REFERENCES %s (%s) ", foreignKey[i+1], foreignKey[i+2]) +
			"ON DELETE NO ACTION ON UPDATE NO ACTION, "
	}
	s.query = s.query[:len(s.query)-2] + " ) ENGINE = InnoDB;"
	return s
}

// Add Insert Clause
// Example:
// type TestTable struct {
// 	Id   int64  `rnsql:"id" rntype:"INT" rnopt:"PK NN AI"`
// 	Name string `rnsql:"name" rntype:"VARCHAR(255)" rnopt:"NN"`
// }
//
// ->
//
// "INSERT INTO `table_name` (`id`, `name`) values (?, ?) "
func (s *Sql) Insert(tableName string, table interface{}) *Sql {
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table must be struct")
	}
	s.query += "INSERT INTO `" + tableName + "` ("
	paramCount := 0
	for i := 0; i < target.NumField(); i++ {
		rnsql, ok := target.Type().Field(i).Tag.Lookup("rnsql")
		if ok {
			s.query += rnsql + ", "
			s.params = append(s.params, target.Field(i).Interface())
			paramCount++
		}
	}
	if paramCount > 0 {
		s.query = s.query[:len(s.query)-2]
	}
	s.query += ") VALUES (" + strings.Repeat("?, ", paramCount)
	if paramCount > 0 {
		s.query = s.query[:len(s.query)-2]
	}
	s.query += ") "
	return s
}

// Add Delete From Clause
// Example:
// "DELETE FROM `table_name` "
func (s *Sql) DeleteFrom(tableName string) *Sql {
	s.query += "DELETE FROM `" + tableName + "` "
	return s
}

// Add Update Clause
// Example:
// type TestTable struct {
// 	Id   int64  `rnsql:"id" rntype:"INT" rnopt:"PK NN AI"`
// 	Name string `rnsql:"name" rntype:"VARCHAR(255)" rnopt:"NN"`
// }
//
// ->
//
// "UPDATE `table_name` SET `id` = ?, `name` = ? "
func (s *Sql) Update(tableName string, table interface{}) *Sql {
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table must be struct")
	}
	s.query += "UPDATE `" + tableName + "` SET "
	for i := 0; i < target.NumField(); i++ {
		rnsql, ok := target.Type().Field(i).Tag.Lookup("rnsql")
		if ok {
			s.query += rnsql + " = ?, "
			s.params = append(s.params, target.Field(i).Interface())
		}
	}
	s.query = s.query[:len(s.query)-2] + " "
	return s
}

// Add Create Index Clause
// Example:
// gorn.DBIndex{
// 	TableName: "table_name",
// 	IndexName: "index_name",
//  IndexType: gorn.DBIndexTypeIndex,
// 	IndexColumns: []*DBIndexColumn{
//    &DBIndexColumn{
//      ColumnName: "id",
//      ASC:        true,
//    },
//    &DBIndexColumn{
//      ColumnName: "name",
//      ASC:        false,
//    },
// }
//
// ->
//
// "CREATE INDEX `index_name` ON `table_name` (`id`, `name`) "
func (s *Sql) CreateIndex(tableName, indexName string, indexColumns []string, isUnique, increase bool) *Sql {
	if isUnique {
		s.query += "CREATE UNIQUE INDEX `"
	} else {
		s.query += "CREATE INDEX `"
	}
	s.query += indexName + "` ON `" + tableName + "` ("
	for _, indexColumn := range indexColumns {
		s.query += indexColumn + ", "
	}
	s.query = s.query[:len(s.query)-2]
	if increase {
		s.query += " ASC) "
	} else {
		s.query += " DESC) "
	}
	return s
}

// Add Set Clause
// Example:
// "SET `set` "
func (s *Sql) Set(set string) *Sql {
	s.query += "SET " + set + " "
	return s
}

// Add From Clause
// Example:
// "FROM `table` "
func (s *Sql) From(table string) *Sql {
	s.query += "FROM " + table + " "
	return s
}

// Add From Clause
// Example:
// "FROM (SELECT * FROM `table`) "
func (s *Sql) FromSql(sql *Sql) *Sql {
	s.query += "FROM " + sql.NestedQuery() + " "
	s.params = append(s.params, sql.Params()...)
	return s
}

// Add As Clause
// Example:
// "AS `alias` "
func (s *Sql) As(alias string) *Sql {
	s.query += "AS " + alias + " "
	return s
}

// Add Join Clause
// Example:
// "JOIN `table` "
func (s *Sql) Join(table string) *Sql {
	s.query += "JOIN " + table + " "
	return s
}

// Add Inner Join Clause
// Example:
// "INNER JOIN `table` "
func (s *Sql) InnerJoin(table string) *Sql {
	s.query += "INNER JOIN " + table + " "
	return s
}

// Add Left Join Clause
// Example:
// "LEFT JOIN `table` "
func (s *Sql) LeftJoin(table string) *Sql {
	s.query += "LEFT JOIN " + table + " "
	return s
}

// Add Right Join Clause
// Example:
// "RIGHT JOIN `table` "
func (s *Sql) RightJoin(table string) *Sql {
	s.query += "RIGHT JOIN " + table + " "
	return s
}

// Add On Clause & Params
// Example:
// "ON `condition` "
func (s *Sql) On(condition string, params ...interface{}) *Sql {
	s.query += "ON " + condition + " "
	s.params = append(s.params, params...)
	return s
}

// Add Where Clause & Params
// Example:
// "WHERE `condition` "
func (s *Sql) Where(condition string, params ...interface{}) *Sql {
	s.query += "WHERE " + condition + " "
	s.params = append(s.params, params...)
	return s
}

// Add And Clause & Params
// Example:
// "AND `condition` "
func (s *Sql) And(condition string, params ...interface{}) *Sql {
	s.query += "AND " + condition + " "
	s.params = append(s.params, params...)
	return s
}

// Add Or Clause & Params
// Example:
// "OR `condition` "
func (s *Sql) Or(condition string, params ...interface{}) *Sql {
	s.query += "OR " + condition + " "
	s.params = append(s.params, params...)
	return s
}

// Add Order By Clause
// Example:
// "ORDER BY `order` "
func (s *Sql) OrderBy(order string) *Sql {
	s.query += "ORDER BY " + order + " "
	return s
}

// Add ASC Clause
// Example:
// "ASC "
func (s *Sql) ASC() *Sql {
	s.query += "ASC "
	return s
}

// Add DESC Clause
// Example:
// "DESC "
func (s *Sql) DESC() *Sql {
	s.query += "DESC "
	return s
}

// Add Limit Clause
// Example:
// "LIMIT `limit` "
func (s *Sql) Limit(limit int) *Sql {
	s.query += "LIMIT ? "
	s.params = append(s.params, limit)
	return s
}

// Add Limit Clause With Pagenation
// Example:
// "LIMIT `page` * `pageSize`, `pageSize` "
func (s *Sql) LimitPage(page, pageSize int64) *Sql {
	s.query += "LIMIT ?, ? "
	s.params = append(s.params, page*pageSize, pageSize)
	return s
}

// Add Offset Clause & Params
// Example:
// "OFFSET ? "
func (s *Sql) Offset(offset int) *Sql {
	s.query += "OFFSET ? "
	s.params = append(s.params, offset)
	return s
}

// Add Show Clause
// Example:
// "SHOW "
func (s *Sql) Show() *Sql {
	s.query += "SHOW "
	return s
}

// Add Full Clause
// Example:
// "FULL "
func (s *Sql) Full() *Sql {
	s.query += "FULL "
	return s
}

// Add Table Clause
// Example:
// "TABLE `table_name` "
func (s *Sql) Table(tableName string) *Sql {
	s.query += "TABLE `" + tableName + "` "
	return s
}

// Add Tables Clause
// Example:
// "TABLES "
func (s *Sql) Tables() *Sql {
	s.query += "TABLES "
	return s
}

// Add Alter Clause
// Example:
// "ALTER "
func (s *Sql) Alter() *Sql {
	s.query += "ALTER "
	return s
}

// Add Add Clause
// Example:
// "ADD "
func (s *Sql) Add() *Sql {
	s.query += "ADD "
	return s
}

// Add Add Column Clause
// Example:
// "ADD COLUMN `column` `column_type` `column_options` "
func (s *Sql) AddColumn(column, columnType, columnOptions string) *Sql {
	s.query += "ADD COLUMN `" + column + "` " + columnType + " " + columnOptions + " "
	return s
}

// Add Add Index Clause
// Example:
// "ADD INDEX `index` (column1(sub_part) ASC, column2 DESC, ...) "
func (s *Sql) AddIndex(indexName string, columnNames []string, columnSubParts []sql.NullInt64, columnOrders []string, isUnique bool) *Sql {
	s.query += "ADD INDEX `" + indexName + "` ("
	for i, column := range columnNames {
		s.query += "`" + column + "` "
		if columnSubParts[i].Valid {
			s.query += "(" + fmt.Sprint(columnSubParts[i].Int64) + ") "
		}
		s.query += columnOrders[i] + ", "
	}
	s.query = s.query[:len(s.query)-2]
	s.query += ") "
	return s
}

// Add Modify Column Clause
// Example:
// "MODIFY COLUMN `column` `column_type` `column_options` "
func (s *Sql) ModifyColumn(column, columnType, columnOptions string) *Sql {
	s.query += "MODIFY COLUMN " + column + " " + columnType + " " + columnOptions + " "
	return s
}

// Add Drop Column Clause
// Example:
// "DROP COLUMN `column` "
func (s *Sql) DropColumn(column string) *Sql {
	s.query += "DROP COLUMN `" + column + "` "
	return s
}

// Add Drop Index Clause
// Example:
// "DROP INDEX `index` "
func (s *Sql) DropIndex(index string) *Sql {
	s.query += "DROP INDEX `" + index + "` "
	return s
}

// Add Drop Primary Key Clause
// Example:
// "DROP PRIMARY KEY "
func (s *Sql) DropPrimaryKey() *Sql {
	s.query += "DROP PRIMARY KEY "
	return s
}

// Add Add Primary Key Clause
// Example:
// "ADD PRIMARY KEY (`column1`, `column2`) "
func (s *Sql) AddPrimaryKey(columns []string) *Sql {
	s.query += "ADD PRIMARY KEY ("
	for _, column := range columns {
		s.query += column + ", "
	}
	s.query = s.query[:len(s.query)-2] + ") "
	return s
}

// Add Default Clause
// Example:
// "DEFAULT `defaultValue` "
//TODO: Fix Default Option
// If defaultValue is string, it must be quoted like `"defaultValue"`
// If defaultValue is nil, it must be NULL like `NULL`
// If defaultValue is Integer or Float, it must be unquoted like `123
func (s *Sql) Default(defaultValue interface{}) *Sql {
	// s.query += "DEFAULT " + fmt.Sprintf("%v", defaultValue) + " "
	return s
}

// Add After Clause
// Example:
// "AFTER `column` "
func (s *Sql) After(column string) *Sql {
	s.query += "AFTER " + column + " "
	return s
}

// Add First Clause
// Example:
// "FIRST "
func (s *Sql) First() *Sql {
	s.query += "FIRST "
	return s
}

// Add Drop Clause
// Example:
// "DROP "
func (s *Sql) Drop() *Sql {
	s.query += "DROP "
	return s
}

// Add Plain Query String
// Example:
// "`query` "
func (s *Sql) AddPlainQuery(query string) *Sql {
	s.query += query + " "
	return s
}

// Return Query With Semicolon
func (s *Sql) Query() string {
	return s.query + ";"
}

// Return Params
func (s *Sql) Params() []interface{} {
	return s.params
}

// Return Query For Nested Query
func (s *Sql) NestedQuery() string {
	return "(" + s.query + ")"
}

// Clear Query & Params
func (s *Sql) Clear() {
	s.query = ""
	s.params = []interface{}{}
}

// Generate New Sql
func NewSql() *Sql {
	return &Sql{query: "", params: []interface{}{}}
}
