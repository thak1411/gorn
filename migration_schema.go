package gorn

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type migrationSchema struct {
	Name, ServerID, Version, Charset, Collation string
	LowerCase                                   int
	Tables                                      map[string]*migrationTableState
	ForeignKeys                                 []*migrationForeignKey
	mode                                        string
}

type migrationTableState struct {
	Name, Kind, Engine, Collation, Definition string
	Columns                                   []*migrationColumnState
	Indexes                                   map[string]*migrationIndexState
	ForeignKeys                               []*migrationForeignKey
}

type migrationColumnState struct {
	Name, Type, Extra, Definition string
	Nullable                      bool
	Default, Charset, Collation   sql.NullString
}

type migrationIndexState struct {
	Name, Method      string
	Unique, Invisible bool
	Parts             []migrationIndexPart
}

type migrationIndexPart struct {
	Name, Expression sql.NullString
	Prefix           sql.NullInt64
	Descending       bool
}

type migrationForeignKey struct {
	Schema, Table, Name, RefSchema, RefTable, UpdateRule, DeleteRule string
	Columns, RefColumns                                              []string
}

func quoteMigrationIdentifier(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func validateMigrationIdentifier(s string) error {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64 || strings.TrimSpace(s) != s {
		return fmt.Errorf("gorn: invalid migration identifier %q", s)
	}
	for _, c := range s {
		if c == '.' || unicode.IsControl(c) {
			return fmt.Errorf("gorn: invalid migration identifier %q", s)
		}
	}
	return nil
}

func (s *migrationSchema) key(name string) string {
	if s.LowerCase != 0 {
		return strings.ToLower(name)
	}
	return name
}

func (s *migrationSchema) fingerprint() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sqlFragments splits only outside quoted strings, quoted identifiers and
// parentheses. SHOW CREATE TABLE, including expressions and comments, is the
// authority for attributes the model does not describe.
func sqlFragments(s string, comma, noBackslash bool) ([]string, error) {
	var result []string
	start, depth := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && quote != '`' && !noBackslash {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(s) && s[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("gorn: unbalanced SQL definition")
			}
		}
		if depth == 0 && ((comma && c == ',') || (!comma && unicode.IsSpace(rune(c)))) {
			if part := strings.TrimSpace(s[start:i]); part != "" {
				result = append(result, part)
			}
			start = i + 1
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("gorn: incomplete SQL definition")
	}
	if part := strings.TrimSpace(s[start:]); part != "" {
		result = append(result, part)
	}
	return result, nil
}

func unquoteMigrationIdentifier(s string) (string, bool) {
	if len(s) < 2 || (s[0] != '`' && s[0] != '"') || s[len(s)-1] != s[0] {
		return "", false
	}
	q := string(s[0])
	return strings.ReplaceAll(s[1:len(s)-1], q+q, q), true
}

func parseCreateDefinition(ddl string, noBackslash bool) (map[string]string, string, error) {
	// Split CREATE TABLE into top-level words: the complete (...) is one word.
	words, err := sqlFragments(ddl, false, noBackslash)
	if err != nil {
		return nil, "", err
	}
	defs := make(map[string]string)
	var normalized []string
	found := false
	for _, word := range words {
		if strings.HasPrefix(word, "(") && strings.HasSuffix(word, ")") && !found {
			found = true
			parts, err := sqlFragments(word[1:len(word)-1], true, noBackslash)
			if err != nil {
				return nil, "", err
			}
			for _, part := range parts {
				fields, err := sqlFragments(part, false, noBackslash)
				if err != nil {
					return nil, "", err
				}
				if len(fields) > 1 {
					if name, ok := unquoteMigrationIdentifier(fields[0]); ok {
						defs[name] = strings.Join(fields[1:], " ")
					}
				}
			}
		}
		// The counter changes on INSERT and is not schema drift. Only remove an
		// actual table option, never AUTO_INCREMENT text inside a quoted comment.
		if found && strings.HasPrefix(word, "AUTO_INCREMENT=") {
			continue
		}
		normalized = append(normalized, word)
	}
	if !found || len(defs) == 0 {
		return nil, "", fmt.Errorf("gorn: cannot parse SHOW CREATE TABLE (sql_quote_show_create must be enabled)")
	}
	return defs, strings.Join(normalized, " "), nil
}

func readMigrationSchema(ctx context.Context, db DBContainer, expectedSchema string) (*migrationSchema, error) {
	s := &migrationSchema{Tables: make(map[string]*migrationTableState)}
	if err := db.QueryRowContext(ctx, "SELECT DATABASE(), @@server_uuid, VERSION(), @@lower_case_table_names, @@SESSION.sql_mode").Scan(&s.Name, &s.ServerID, &s.Version, &s.LowerCase, &s.mode); err != nil {
		return nil, err
	}
	if s.key(s.Name) != s.key(expectedSchema) {
		return nil, fmt.Errorf("gorn: connection database %q differs from configured database %q", s.Name, expectedSchema)
	}
	if !strings.HasPrefix(s.Version, "8.") {
		return nil, fmt.Errorf("gorn: safe migration requires MySQL 8.x, got %s", s.Version)
	}
	if err := db.QueryRowContext(ctx, "SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME = ?", s.Name).Scan(&s.Charset, &s.Collation); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT TABLE_NAME, TABLE_TYPE, COALESCE(ENGINE, ''), COALESCE(TABLE_COLLATION, '') FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", s.Name)
	if err != nil {
		return nil, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			t := &migrationTableState{Indexes: make(map[string]*migrationIndexState)}
			if err := rows.Scan(&t.Name, &t.Kind, &t.Engine, &t.Collation); err != nil {
				return err
			}
			s.Tables[s.key(t.Name)] = t
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, "SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, CHARACTER_SET_NAME, COLLATION_NAME, EXTRA FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION", s.Name)
	if err != nil {
		return nil, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var table, nullable string
			c := &migrationColumnState{}
			if err := rows.Scan(&table, &c.Name, &c.Type, &nullable, &c.Default, &c.Charset, &c.Collation, &c.Extra); err != nil {
				return err
			}
			c.Nullable = nullable == "YES"
			if t := s.Tables[s.key(table)]; t != nil {
				t.Columns = append(t.Columns, c)
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, "SELECT TABLE_NAME, INDEX_NAME, NON_UNIQUE, INDEX_TYPE, IS_VISIBLE, COLUMN_NAME, EXPRESSION, SUB_PART, COLLATION FROM INFORMATION_SCHEMA.STATISTICS WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX", s.Name)
	if err != nil {
		return nil, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var table, name, method, visible string
			var nonUnique bool
			var collation sql.NullString
			var p migrationIndexPart
			if err := rows.Scan(&table, &name, &nonUnique, &method, &visible, &p.Name, &p.Expression, &p.Prefix, &collation); err != nil {
				return err
			}
			t := s.Tables[s.key(table)]
			if t == nil {
				return ErrMigrationDrift
			}
			idx := t.Indexes[strings.ToLower(name)]
			if idx == nil {
				idx = &migrationIndexState{Name: name, Unique: !nonUnique, Method: method, Invisible: visible == "NO"}
				t.Indexes[strings.ToLower(name)] = idx
			}
			p.Descending = collation.String == "D"
			idx.Parts = append(idx.Parts, p)
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME, k.COLUMN_NAME,
 k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.UPDATE_RULE, r.DELETE_RULE
 FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE k JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS r
 ON k.CONSTRAINT_SCHEMA = r.CONSTRAINT_SCHEMA AND k.CONSTRAINT_NAME = r.CONSTRAINT_NAME AND k.TABLE_NAME = r.TABLE_NAME
 WHERE k.REFERENCED_TABLE_NAME IS NOT NULL AND (k.TABLE_SCHEMA = ? OR k.REFERENCED_TABLE_SCHEMA = ?)
 ORDER BY k.TABLE_SCHEMA, k.TABLE_NAME, k.CONSTRAINT_NAME, k.ORDINAL_POSITION`, s.Name, s.Name)
	if err != nil {
		return nil, err
	}
	err = func() error {
		defer rows.Close()
		var previous *migrationForeignKey
		for rows.Next() {
			fk := &migrationForeignKey{}
			var col, refCol string
			if err := rows.Scan(&fk.Schema, &fk.Table, &fk.Name, &col, &fk.RefSchema, &fk.RefTable, &refCol, &fk.UpdateRule, &fk.DeleteRule); err != nil {
				return err
			}
			if previous != nil && previous.Schema == fk.Schema && previous.Table == fk.Table && previous.Name == fk.Name {
				fk = previous
			} else {
				s.ForeignKeys = append(s.ForeignKeys, fk)
				if fk.Schema == s.Name {
					if t := s.Tables[s.key(fk.Table)]; t != nil {
						t.ForeignKeys = append(t.ForeignKeys, fk)
					}
				}
			}
			fk.Columns = append(fk.Columns, col)
			fk.RefColumns = append(fk.RefColumns, refCol)
			previous = fk
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(s.Tables))
	for k := range s.Tables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := s.Tables[k]
		if t.Kind != "BASE TABLE" {
			continue
		}
		var name, ddl string
		if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+quoteMigrationIdentifier(s.Name)+"."+quoteMigrationIdentifier(t.Name)).Scan(&name, &ddl); err != nil {
			return nil, err
		}
		if strings.HasPrefix(strings.ToUpper(ddl), "CREATE TEMPORARY TABLE") {
			return nil, fmt.Errorf("gorn: temporary table shadows %s; use a migration connection without temporary tables", t.Name)
		}
		defs, normalized, err := parseCreateDefinition(ddl, false)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", t.Name, err)
		}
		t.Definition = normalized
		for _, c := range t.Columns {
			var ok bool
			c.Definition, ok = defs[c.Name]
			if !ok {
				return nil, ErrMigrationDrift
			}
		}
	}
	return s, nil
}
