package gorn

import (
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

type migrationModel struct {
	Name        string
	Columns     []*migrationModelColumn
	Primary     []string
	ForeignKeys []*migrationModelFK
}
type migrationModelColumn struct {
	Name, Type                     string
	Nullable, Auto, Binary, Unique bool
	Default                        *string
}
type migrationModelFK struct {
	Column, RefTable, RefColumn, Delete, Update string
}

var integerMigrationType = regexp.MustCompile(`(?i)^(tinyint|smallint|mediumint|int|integer|bigint)(?:\(([0-9]+)\))?( unsigned)?( zerofill)?$`)
var sizedMigrationType = regexp.MustCompile(`(?i)^(varchar|char|varbinary|binary|decimal|numeric|dec|fixed|float|double|real|datetime|timestamp|time|year|bit)(?:\(([0-9]+)(?:\s*,\s*([0-9]+))?\))?( unsigned)?$`)
var numericMigrationDefault = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var timeMigrationDefault = regexp.MustCompile(`(?i)^current_timestamp(?:\([0-6]?\))?$`)

func normalizeMigrationType(input string, noBackslash bool) (string, error) {
	s := strings.TrimSpace(input)
	if oneOf(strings.ToLower(s), "bool", "boolean") {
		return "tinyint", nil
	}
	s = regexp.MustCompile(`(?i)^double\s+precision`).ReplaceAllString(s, "double")
	if m := integerMigrationType.FindStringSubmatch(s); m != nil {
		kind := strings.ToLower(m[1])
		if kind == "integer" {
			kind = "int"
		}
		if m[3] != "" || m[4] != "" {
			kind += " unsigned"
		}
		if m[4] != "" {
			kind += " zerofill"
		}
		return kind, nil
	}
	if m := sizedMigrationType.FindStringSubmatch(s); m != nil {
		kind := strings.ToLower(m[1])
		a, b := m[2], m[3]
		if oneOf(kind, "numeric", "dec", "fixed") {
			kind = "decimal"
		}
		if kind == "real" {
			return "", fmt.Errorf("gorn: use FLOAT or DOUBLE instead of sql_mode-dependent REAL")
		}
		n, _ := strconv.Atoi(a)
		scale, _ := strconv.Atoi(b)
		switch kind {
		case "decimal":
			if a == "" {
				n = 10
			}
			if n < 1 || n > 65 || scale > 30 || scale > n {
				return "", fmt.Errorf("gorn: invalid decimal type %q", input)
			}
			s = fmt.Sprintf("decimal(%d,%d)", n, scale)
		case "varchar", "varbinary", "char", "binary":
			if a == "" && oneOf(kind, "char", "binary") {
				n = 1
			}
			if b != "" || (a == "" && oneOf(kind, "varchar", "varbinary")) || n > 65535 {
				return "", fmt.Errorf("gorn: invalid string type %q", input)
			}
			s = fmt.Sprintf("%s(%d)", kind, n)
		case "datetime", "timestamp", "time":
			if b != "" || n > 6 {
				return "", fmt.Errorf("gorn: invalid temporal precision %q", input)
			}
			s = kind
			if n > 0 {
				s += fmt.Sprintf("(%d)", n)
			}
		case "year":
			if b != "" || (a != "" && n != 4) {
				return "", fmt.Errorf("gorn: invalid year type %q", input)
			}
			s = "year"
		case "bit":
			if a == "" {
				n = 1
			}
			if b != "" || n < 1 || n > 64 {
				return "", fmt.Errorf("gorn: invalid bit type %q", input)
			}
			s = fmt.Sprintf("bit(%d)", n)
		default:
			// FLOAT(M,D) is deprecated and silently rounds data. Keep the
			// automatic migrator's accepted numeric definitions unambiguous.
			if a != "" {
				return "", fmt.Errorf("gorn: use FLOAT/DOUBLE without display precision")
			}
			s = kind
		}
		if m[4] != "" {
			if !oneOf(kind, "decimal", "float", "double") {
				return "", fmt.Errorf("gorn: UNSIGNED is invalid for %s", kind)
			}
			s += " unsigned"
		}
		return s, nil
	}
	if oneOf(strings.ToLower(s), "date", "json", "tinytext", "text", "mediumtext", "longtext", "tinyblob", "blob", "mediumblob", "longblob") {
		return strings.ToLower(s), nil
	}
	lower := strings.ToLower(s)
	for _, kind := range []string{"enum", "set"} {
		if strings.HasPrefix(lower, kind+"(") && strings.HasSuffix(s, ")") {
			parts, err := sqlFragments(s[len(kind)+1:len(s)-1], true, noBackslash)
			if err != nil || len(parts) == 0 {
				return "", fmt.Errorf("gorn: invalid %s type", kind)
			}
			for i, p := range parts {
				value, err := decodeMigrationString(p, noBackslash)
				if err != nil {
					return "", err
				}
				// INFORMATION_SCHEMA may spell an apostrophe as \\' while the
				// model uses ''. Compare values, then emit one canonical spelling.
				if !noBackslash {
					value = strings.ReplaceAll(value, "\\", "\\\\")
				}
				parts[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
			}
			return kind + "(" + strings.Join(parts, ",") + ")", nil
		}
	}
	return "", fmt.Errorf("gorn: unsupported or invalid migration type %q; use explicit SQL for advanced types", input)
}

// MySQL's COLUMN_TYPE serialization always escapes backslashes in ENUM/SET
// members, even when the connection uses NO_BACKSLASH_ESCAPES.
func normalizeStoredMigrationType(input string, noBackslash bool) (string, error) {
	canonical, err := normalizeMigrationType(input, false)
	if err != nil || !noBackslash || (!strings.HasPrefix(canonical, "enum(") && !strings.HasPrefix(canonical, "set(")) {
		return canonical, err
	}
	open := strings.IndexByte(canonical, '(')
	parts, err := sqlFragments(canonical[open+1:len(canonical)-1], true, false)
	if err != nil {
		return "", err
	}
	for i, part := range parts {
		value, err := decodeMigrationString(part, false)
		if err != nil {
			return "", err
		}
		parts[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return canonical[:open+1] + strings.Join(parts, ",") + ")", nil
}

// SHOW CREATE TABLE serializes string literals with backslash escapes regardless
// of sql_mode. Transcode those literals before reusing attributes in a session
// with NO_BACKSLASH_ESCAPES; otherwise defaults/comments silently gain slashes.
func renderStoredMigrationDefinition(input string, noBackslash bool) (string, error) {
	if !noBackslash {
		return input, nil
	}
	var out strings.Builder
	for i := 0; i < len(input); {
		start := i
		quote := input[i]
		if quote != '\'' && quote != '`' && quote != '"' {
			out.WriteByte(quote)
			i++
			continue
		}
		i++
		closed := false
		for i < len(input) {
			if quote == '\'' && input[i] == '\\' {
				i += 2
				continue
			}
			if input[i] == quote {
				if i+1 < len(input) && input[i+1] == quote {
					i += 2
					continue
				}
				i++
				closed = true
				break
			}
			i++
		}
		if !closed {
			return "", fmt.Errorf("gorn: invalid quoted SHOW CREATE TABLE attribute")
		}
		if quote != '\'' {
			out.WriteString(input[start:i])
			continue
		}
		value, err := decodeMigrationString(input[start:i], false)
		if err != nil {
			return "", err
		}
		out.WriteByte('\'')
		out.WriteString(strings.ReplaceAll(value, "'", "''"))
		out.WriteByte('\'')
	}
	return out.String(), nil
}

func decodeMigrationString(s string, noBackslash bool) (string, error) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", fmt.Errorf("gorn: expected a single-quoted SQL literal, got %q", s)
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		if c == '\'' {
			if i+1 >= len(s)-1 || s[i+1] != '\'' {
				return "", fmt.Errorf("gorn: invalid SQL string literal")
			}
			i++
		} else if c == '\\' && !noBackslash {
			i++
			if i >= len(s)-1 {
				return "", fmt.Errorf("gorn: invalid SQL escape")
			}
			c = s[i]
			switch c {
			case '0':
				c = 0
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'Z':
				c = 26
			case '%', '_':
				b.WriteByte('\\')
			}
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

func validateMigrationDefault(s string, noBackslash bool) error {
	if oneOf(strings.ToUpper(s), "NULL", "TRUE", "FALSE") || numericMigrationDefault.MatchString(s) || timeMigrationDefault.MatchString(s) {
		return nil
	}
	_, err := decodeMigrationString(s, noBackslash)
	return err
}

func sameMigrationDefault(desired string, old *migrationColumnState, noBackslash bool) bool {
	if strings.EqualFold(desired, "NULL") {
		return !old.Default.Valid
	}
	if !old.Default.Valid {
		return false
	}
	if strings.HasPrefix(desired, "'") {
		v, err := decodeMigrationString(desired, noBackslash)
		return err == nil && v == old.Default.String
	}
	if strings.EqualFold(desired, "TRUE") {
		desired = "1"
	}
	if strings.EqualFold(desired, "FALSE") {
		desired = "0"
	}
	if timeMigrationDefault.MatchString(desired) {
		norm := func(s string) string { return strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(s), "()"), "(0)") }
		return norm(desired) == norm(old.Default.String)
	}
	// Do not equate character defaults such as '001' and '1'. MySQL reports
	// BIT defaults as b'...', whereas the model uses an ordinary SQL number.
	if isMigrationCharacterType(strings.ToLower(old.Type)) {
		return desired == old.Default.String
	}
	actual := old.Default.String
	if strings.HasPrefix(strings.ToLower(old.Type), "bit(") && strings.HasPrefix(actual, "b'") && strings.HasSuffix(actual, "'") {
		bits, ok := new(big.Int).SetString(actual[2:len(actual)-1], 2)
		if !ok {
			return false
		}
		actual = bits.String()
	}
	a, ok := new(big.Rat).SetString(desired)
	if !ok {
		return false
	}
	b, ok := new(big.Rat).SetString(actual)
	return ok && a.Cmp(b) == 0
}

func parseMigrationModel(table MigrationTable, noBackslash bool) (*migrationModel, error) {
	if err := validateMigrationIdentifier(table.Name); err != nil {
		return nil, err
	}
	t := reflect.TypeOf(table.Model)
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("gorn: model for %s must be a struct or pointer to struct", table.Name)
	}
	m := &migrationModel{Name: table.Name}
	seen := make(map[string]bool)
	autoCount := 0
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, ok := field.Tag.Lookup("rnsql")
		if !ok || name == "-" {
			continue
		}
		if field.PkgPath != "" {
			return nil, fmt.Errorf("gorn: migration field %s must be exported", field.Name)
		}
		if err := validateMigrationIdentifier(name); err != nil {
			return nil, err
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("gorn: duplicate column %s.%s", table.Name, name)
		}
		seen[strings.ToLower(name)] = true
		opts := stringToWordMap(field.Tag.Get("rnopt"))
		for opt := range opts {
			if !oneOf(opt, "PK", "NN", "UN", "AI", "BIN", "UQ") {
				return nil, fmt.Errorf("gorn: unknown rnopt %q on %s.%s", opt, table.Name, name)
			}
		}
		typeSQL := field.Tag.Get("rntype")
		if opts["UN"] && !strings.Contains(strings.ToLower(typeSQL), "unsigned") {
			typeSQL += " UNSIGNED"
		}
		typeSQL, err := normalizeMigrationType(typeSQL, noBackslash)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", table.Name, name, err)
		}
		c := &migrationModelColumn{Name: name, Type: typeSQL, Nullable: !opts["NN"] && !opts["PK"] && !opts["AI"], Auto: opts["AI"], Binary: opts["BIN"], Unique: opts["UQ"]}
		if c.Auto {
			autoCount++
			if integerMigrationType.FindStringSubmatch(typeSQL) == nil {
				return nil, fmt.Errorf("gorn: AUTO_INCREMENT requires an integer column")
			}
		}
		if c.Binary && !isMigrationCharacterType(typeSQL) {
			return nil, fmt.Errorf("gorn: BIN requires a character column")
		}
		if value, ok := field.Tag.Lookup("rndefault"); ok {
			value = strings.TrimSpace(value)
			if err := validateMigrationDefault(value, noBackslash); err != nil {
				return nil, fmt.Errorf("%s.%s default: %w", table.Name, name, err)
			}
			if isMigrationCharacterType(c.Type) && !strings.HasPrefix(value, "'") && !strings.EqualFold(value, "NULL") {
				return nil, fmt.Errorf("gorn: character default on %s.%s must be a quoted string or NULL", table.Name, name)
			}
			if (!c.Nullable && strings.EqualFold(value, "NULL")) || c.Auto {
				return nil, fmt.Errorf("gorn: invalid default on %s.%s", table.Name, name)
			}
			c.Default = &value
		}
		m.Columns = append(m.Columns, c)
		if opts["PK"] {
			m.Primary = append(m.Primary, name)
		}
		if ref, ok := field.Tag.Lookup("FK"); ok {
			parts := strings.Split(ref, ".")
			if len(parts) != 2 {
				return nil, fmt.Errorf("gorn: FK on %s.%s must be table.column", table.Name, name)
			}
			for _, part := range parts {
				if err := validateMigrationIdentifier(part); err != nil {
					return nil, err
				}
			}
			fk := &migrationModelFK{Column: name, RefTable: parts[0], RefColumn: parts[1], Delete: strings.ToUpper(field.Tag.Get("FKOnDelete")), Update: strings.ToUpper(field.Tag.Get("FKOnUpdate"))}
			for _, rule := range []string{fk.Delete, fk.Update} {
				if !oneOf(rule, "", "NO ACTION", "RESTRICT", "CASCADE", "SET NULL") {
					return nil, fmt.Errorf("gorn: unsupported foreign-key rule %q", rule)
				}
			}
			if !c.Nullable && (fk.Delete == "SET NULL" || fk.Update == "SET NULL") {
				return nil, fmt.Errorf("gorn: SET NULL requires nullable column %s.%s", table.Name, name)
			}
			m.ForeignKeys = append(m.ForeignKeys, fk)
		} else if field.Tag.Get("FKOnDelete") != "" || field.Tag.Get("FKOnUpdate") != "" {
			return nil, fmt.Errorf("gorn: foreign-key action requires FK tag")
		}
	}
	if len(m.Columns) == 0 || autoCount > 1 {
		return nil, fmt.Errorf("gorn: table %s needs columns and at most one AUTO_INCREMENT", table.Name)
	}
	return m, nil
}

func isMigrationCharacterType(t string) bool {
	return strings.HasPrefix(t, "varchar(") || strings.HasPrefix(t, "char(") || strings.Contains(t, "text") || strings.HasPrefix(t, "enum(") || strings.HasPrefix(t, "set(")
}

func safeMigrationTypeChange(old, next string, noBackslash bool) bool {
	if old == next {
		return true
	}
	a, b := integerMigrationType.FindStringSubmatch(old), integerMigrationType.FindStringSubmatch(next)
	if a != nil && b != nil {
		rank := map[string]int{"tinyint": 1, "smallint": 2, "mediumint": 3, "int": 4, "bigint": 5}
		return rank[b[1]] >= rank[a[1]] && a[3] == b[3] && a[4] == b[4]
	}
	a, b = sizedMigrationType.FindStringSubmatch(old), sizedMigrationType.FindStringSubmatch(next)
	if a != nil && b != nil && a[1] == b[1] && a[4] == b[4] {
		an, _ := strconv.Atoi(a[2])
		bn, _ := strconv.Atoi(b[2])
		ad, _ := strconv.Atoi(a[3])
		bd, _ := strconv.Atoi(b[3])
		switch a[1] {
		case "varchar", "varbinary":
			return bn >= an
		case "decimal":
			return bd >= ad && bn-bd >= an-ad
		case "datetime", "timestamp", "time":
			return bn >= an
		}
	}
	for _, family := range [][]string{{"tinytext", "text", "mediumtext", "longtext"}, {"tinyblob", "blob", "mediumblob", "longblob"}} {
		ai, bi := -1, -1
		for i, t := range family {
			if t == old {
				ai = i
			}
			if t == next {
				bi = i
			}
		}
		if ai >= 0 && bi >= ai {
			return true
		}
	}
	for _, kind := range []string{"enum", "set"} {
		if strings.HasPrefix(old, kind+"(") && strings.HasPrefix(next, kind+"(") {
			a, _ := sqlFragments(old[len(kind)+1:len(old)-1], true, noBackslash)
			b, _ := sqlFragments(next[len(kind)+1:len(next)-1], true, noBackslash)
			if len(b) < len(a) {
				return false
			}
			for i := range a {
				av, _ := decodeMigrationString(a[i], noBackslash)
				bv, _ := decodeMigrationString(b[i], noBackslash)
				if av != bv {
					return false
				}
			}
			return true
		}
	}
	return false
}

// Rebuild only model-owned attributes; preserve DEFAULT, COMMENT, charset,
// collation, ON UPDATE and other server-described attributes verbatim.
func migrationColumnDefinition(c *migrationModelColumn, old *migrationColumnState, charset string, noBackslash bool) (string, error) {
	var typeAttrs, attrs []string
	if old != nil {
		definition, err := renderStoredMigrationDefinition(old.Definition, noBackslash)
		if err != nil {
			return "", err
		}
		words, err := sqlFragments(definition, false, noBackslash)
		if err != nil {
			return "", err
		}
		if len(words) == 0 {
			return "", fmt.Errorf("gorn: empty column definition")
		}
		for i := 1; i < len(words); i++ {
			w := strings.ToUpper(words[i])
			switch w {
			case "UNSIGNED", "ZEROFILL", "AUTO_INCREMENT":
			case "NOT":
				if i+1 < len(words) && strings.EqualFold(words[i+1], "NULL") {
					i++
				} else {
					return "", fmt.Errorf("gorn: unsupported NOT column attribute")
				}
			case "NULL":
			case "CHARACTER":
				if i+2 >= len(words) || !strings.EqualFold(words[i+1], "SET") {
					return "", fmt.Errorf("gorn: invalid column charset")
				}
				typeAttrs = append(typeAttrs, words[i:i+3]...)
				i += 2
			case "COLLATE":
				if i+1 >= len(words) {
					return "", fmt.Errorf("gorn: invalid column collation")
				}
				if !c.Binary {
					typeAttrs = append(typeAttrs, words[i:i+2]...)
				}
				i++
			case "DEFAULT":
				if i+1 >= len(words) {
					return "", fmt.Errorf("gorn: invalid column default")
				}
				// DEFAULT NULL is implicit for nullable columns. It cannot be
				// carried into an explicitly permitted NOT NULL conversion.
				if c.Default == nil && (c.Nullable || !strings.EqualFold(words[i+1], "NULL")) {
					attrs = append(attrs, words[i:i+2]...)
				}
				i++
			default:
				attrs = append(attrs, words[i])
			}
		}
	}
	if c.Binary {
		if old != nil && old.Charset.Valid {
			charset = old.Charset.String
		}
		collation := charset + "_bin"
		if old != nil && old.Collation.Valid && strings.HasSuffix(old.Collation.String, "_bin") {
			collation = old.Collation.String
		}
		typeAttrs = append(typeAttrs, "COLLATE", quoteMigrationIdentifier(collation))
	}
	parts := []string{quoteMigrationIdentifier(c.Name), c.Type}
	parts = append(parts, typeAttrs...)
	if c.Nullable {
		parts = append(parts, "NULL")
	} else {
		parts = append(parts, "NOT NULL")
	}
	if c.Default != nil {
		parts = append(parts, "DEFAULT", *c.Default)
	}
	parts = append(parts, attrs...)
	if c.Auto {
		parts = append(parts, "AUTO_INCREMENT")
	}
	return strings.Join(parts, " "), nil
}
