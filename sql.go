package gorn

import (
	"fmt"
	"reflect"
	"strings"
)

type Sql struct {
	query string
}

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

func (s *Sql) Create(tableName string, table interface{}) *Sql {
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
		rnsql, ok := target.Type().Field(i).Tag.Lookup("rnsql")
		rnsql = "`" + rnsql + "`"
		if ok {
			if rntype, ok := target.Type().Field(i).Tag.Lookup("rntype"); ok {
				s.query += rnsql + " " + rntype + " "
			} else {
				panic("rntype not found")
			}
			options := []string{"NN", "UQ", "BIN", "UN", "ZF", "AI"}
			optionName := []string{"NOT NULL", "UNIQUE", "BINARY", "UNSIGNED", "ZEROFILL", "AUTO_INCREMENT"}
			for j, option := range options {
				if _, ok := target.Type().Field(i).Tag.Lookup(option); ok {
					s.query += optionName[j] + " "
				}
			}
			s.query += ", "
			if _, ok := target.Type().Field(i).Tag.Lookup("PK"); ok {
				primaryKey = append(primaryKey, rnsql)
			}
			if fk, ok := target.Type().Field(i).Tag.Lookup("FK"); ok {
				if fkRef, ok := target.Type().Field(i).Tag.Lookup("FK_REF"); !ok {
					foreignKey = append(foreignKey, rnsql, fk, fkRef)
				}
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

func (s *Sql) Insert(tableName string, table interface{}) *Sql {
	target := reflect.ValueOf(table)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("table must be struct")
	}
	valueCount := 0
	s.query += "INSERT INTO " + tableName + " ("
	for i := 0; i < target.NumField(); i++ {
		rnsql, ok := target.Type().Field(i).Tag.Lookup("rnsql")
		if ok {
			if _, ok := target.Type().Field(i).Tag.Lookup("AI"); !ok {
				s.query += rnsql + ", "
				valueCount += 1
			}
		}
	}
	if valueCount > 0 {
		s.query = s.query[:len(s.query)-2]
	}
	s.query += ") VALUES (" + strings.Repeat("?, ", valueCount)
	if valueCount > 0 {
		s.query = s.query[:len(s.query)-2]
	}
	s.query += ") "
	return s
}

func (s *Sql) From(table string) *Sql {
	s.query += "FROM " + table + " "
	return s
}

func (s *Sql) As(alias string) *Sql {
	s.query += "AS " + alias + " "
	return s
}

func (s *Sql) Where(condition string) *Sql {
	s.query += "WHERE " + condition + " "
	return s
}

func (s *Sql) And(condition string) *Sql {
	s.query += "AND " + condition + " "
	return s
}

func (s *Sql) Or(condition string) *Sql {
	s.query += "OR " + condition + " "
	return s
}

func (s *Sql) OrderBy(order string) *Sql {
	s.query += "ORDER BY " + order + " "
	return s
}

func (s *Sql) Limit(limit int) *Sql {
	s.query += "LIMIT " + fmt.Sprint(limit) + " "
	return s
}

func (s *Sql) Offset(offset int) *Sql {
	s.query += "OFFSET " + fmt.Sprint(offset) + " "
	return s
}

func (s *Sql) Show() *Sql {
	s.query += "SHOW "
	return s
}

func (s *Sql) Full() *Sql {
	s.query += "FULL "
	return s
}

func (s *Sql) Tables() *Sql {
	s.query += "TABLES "
	return s
}

func (s *Sql) AddPlainQuery(query string) *Sql {
	s.query += query + " "
	return s
}

func (s *Sql) Query() string {
	return s.query + ";"
}

func (s *Sql) NestedQuery() string {
	return "(" + s.query + ")"
}

func (s *Sql) Clear() {
	s.query = ""
}

func NewSql() *Sql {
	return &Sql{query: ""}
}
