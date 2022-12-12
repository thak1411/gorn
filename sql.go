package gorn

import (
	"fmt"
	"reflect"
)

type Sql struct {
	query string
}

func (s *Sql) Select(obj interface{}) *Sql {
	target := reflect.ValueOf(obj)
	if target.Kind() == reflect.Ptr {
		target = target.Elem()
	}
	if target.Kind() != reflect.Struct {
		panic("obj must be struct")
	}
	s.query += "SELECT "
	for i := 0; i < target.NumField(); i++ {
		gsql, ok := target.Type().Field(i).Tag.Lookup("gsql")
		if ok {
			s.query += gsql + ", "
		}
	}
	s.query = s.query[:len(s.query)-2] + " "
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

func (s *Sql) Clear() {
	s.query = ""
}

func NewSql() *Sql {
	return &Sql{query: ""}
}
