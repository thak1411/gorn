package gorn

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

func buildMigrationPlan(s *migrationSchema, tables []MigrationTable, indexes []*DBIndex, options MigrationOptions) (*MigrationPlan, error) {
	p := &MigrationPlan{Schema: s.Name, ServerVersion: s.Version, SchemaFingerprint: s.fingerprint(), Steps: []MigrationStep{}, options: options, mode: s.mode}
	noBackslash := strings.Contains(s.mode, "NO_BACKSLASH_ESCAPES")
	models := make(map[string]*migrationModel)
	wantedIndexes := make(map[string]map[string]*DBIndex)
	names := make(map[string]string)
	for _, t := range tables {
		m, err := parseMigrationModel(t, noBackslash)
		if err != nil {
			return nil, err
		}
		key := s.key(t.Name)
		if models[key] != nil {
			return nil, fmt.Errorf("gorn: duplicate table %s", t.Name)
		}
		models[key] = m
		names[key] = t.Name
	}
	for _, idx := range indexes {
		if err := validateMigrationIndex(idx); err != nil {
			return nil, err
		}
		key := s.key(idx.TableName)
		names[key] = idx.TableName
		if wantedIndexes[key] == nil {
			wantedIndexes[key] = make(map[string]*DBIndex)
		}
		ik := strings.ToLower(idx.IndexName)
		if wantedIndexes[key][ik] != nil {
			return nil, fmt.Errorf("gorn: duplicate index %s.%s", idx.TableName, idx.IndexName)
		}
		wantedIndexes[key][ik] = idx
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var creates, alters, foreignSteps []MigrationStep
	for _, key := range keys {
		name := names[key]
		m := models[key]
		old := s.Tables[key]
		desired := wantedIndexes[key]
		if old != nil && (old.Kind != "BASE TABLE" || old.Engine != "InnoDB") {
			return nil, fmt.Errorf("gorn: %s is not an InnoDB base table", name)
		}
		if old == nil && m == nil {
			return nil, fmt.Errorf("gorn: table %s does not exist and has no model", name)
		}
		if desired == nil {
			desired = make(map[string]*DBIndex)
		}
		// Validate index columns against the complete resulting column set.
		available := make(map[string]bool)
		if old != nil && (!options.AllowDropColumns || m == nil) {
			for _, c := range old.Columns {
				available[strings.ToLower(c.Name)] = true
			}
		}
		if m != nil {
			for _, c := range m.Columns {
				available[strings.ToLower(c.Name)] = true
			}
		}
		for _, idx := range desired {
			for _, c := range idx.Columns {
				if !available[strings.ToLower(c.ColumnName)] {
					return nil, fmt.Errorf("gorn: index %s.%s references missing column %s", name, idx.IndexName, c.ColumnName)
				}
			}
		}
		if old == nil {
			var definitions []string
			for _, c := range m.Columns {
				def, err := migrationColumnDefinition(c, nil, s.Charset, noBackslash)
				if err != nil {
					return nil, err
				}
				definitions = append(definitions, def)
			}
			if len(m.Primary) > 0 {
				definitions = append(definitions, "PRIMARY KEY ("+migrationIdentifierList(m.Primary)+")")
			}
			for _, ik := range sortedMigrationIndexKeys(desired) {
				definitions = append(definitions, migrationIndexDefinition(desired[ik]))
			}
			for _, c := range m.Columns {
				if c.Auto && !migrationColumnIndexed(c.Name, m.Primary, desired) {
					return nil, fmt.Errorf("gorn: AUTO_INCREMENT %s.%s must lead a primary or secondary index", name, c.Name)
				}
			}
			creates = append(creates, MigrationStep{Table: name, SQL: "CREATE TABLE " + quoteMigrationIdentifier(name) + " (" + strings.Join(definitions, ", ") + ") ENGINE=InnoDB DEFAULT CHARACTER SET " + quoteMigrationIdentifier(s.Charset) + " COLLATE " + quoteMigrationIdentifier(s.Collation), Reason: "create missing table and its indexes"})
		} else {
			clauses, reasons, destructive, err := planMigrationTable(s, old, m, desired, options, noBackslash)
			if err != nil {
				return nil, err
			}
			if len(clauses) > 0 {
				alters = append(alters, migrationAlterStep(name, clauses, strings.Join(reasons, "; "), destructive, options))
			}
		}
		if m != nil {
			clauses, destructive, err := planMigrationForeignKeys(s, m, old, models, options)
			if err != nil {
				return nil, err
			}
			if len(clauses) > 0 {
				foreignSteps = append(foreignSteps, migrationAlterStep(name, clauses, "update only changed foreign-key constraints", destructive, options))
			}
			if old != nil && !options.AllowDropColumns {
				managed := make(map[string]bool)
				for _, c := range m.Columns {
					managed[strings.ToLower(c.Name)] = true
				}
				for _, c := range old.Columns {
					if !managed[strings.ToLower(c.Name)] {
						p.Warnings = append(p.Warnings, fmt.Sprintf("preserved unmodeled column %s.%s", name, c.Name))
					}
				}
			}
		}
	}
	p.Steps = append(p.Steps, creates...)
	p.Steps = append(p.Steps, alters...)
	p.Steps = append(p.Steps, foreignSteps...)
	if len(p.Steps) > 0 {
		p.Warnings = append(p.Warnings, "DDL can acquire metadata locks or rebuild tables. Atomicity is per statement, not for the whole plan; re-plan after any failure.")
	}
	return p, nil
}

func validateMigrationIndex(idx *DBIndex) error {
	if idx == nil {
		return fmt.Errorf("gorn: nil migration index")
	}
	for _, name := range []string{idx.TableName, idx.IndexName} {
		if err := validateMigrationIdentifier(name); err != nil {
			return err
		}
	}
	if strings.EqualFold(idx.IndexName, "PRIMARY") || strings.HasPrefix(strings.ToUpper(idx.IndexName), "GORN_FK_") {
		return fmt.Errorf("gorn: reserved index name %s", idx.IndexName)
	}
	if !oneOf(idx.IndexType, DBIndexTypeIndex, DBIndexTypeUnique) || len(idx.Columns) == 0 {
		return fmt.Errorf("gorn: index %s requires INDEX/UNIQUE and columns", idx.IndexName)
	}
	seen := make(map[string]bool)
	for _, c := range idx.Columns {
		if c == nil {
			return fmt.Errorf("gorn: nil index column")
		}
		if err := validateMigrationIdentifier(c.ColumnName); err != nil {
			return err
		}
		key := strings.ToLower(c.ColumnName)
		if seen[key] {
			return fmt.Errorf("gorn: duplicate index column %s", c.ColumnName)
		}
		seen[key] = true
		if c.SubPart.Valid && c.SubPart.Int64 <= 0 {
			return fmt.Errorf("gorn: index prefix must be positive")
		}
	}
	return nil
}

func sortedMigrationIndexKeys(m map[string]*DBIndex) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func migrationIdentifierList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quoteMigrationIdentifier(n)
	}
	return strings.Join(quoted, ", ")
}
func migrationObjectName(prefix, table, column string) string {
	sum := sha256.Sum256([]byte(table + "\x00" + column))
	return fmt.Sprintf("%s%x", prefix, sum[:20])
}

func migrationIndexDefinition(idx *DBIndex) string {
	kind := "INDEX"
	if idx.IndexType == DBIndexTypeUnique {
		kind = "UNIQUE INDEX"
	}
	parts := make([]string, 0, len(idx.Columns))
	for _, c := range idx.Columns {
		part := quoteMigrationIdentifier(c.ColumnName)
		if c.SubPart.Valid {
			part += fmt.Sprintf("(%d)", c.SubPart.Int64)
		}
		if c.ASC {
			part += " ASC"
		} else {
			part += " DESC"
		}
		parts = append(parts, part)
	}
	return kind + " " + quoteMigrationIdentifier(idx.IndexName) + " (" + strings.Join(parts, ", ") + ")"
}

func equalMigrationIndex(old *migrationIndexState, desired *DBIndex) bool {
	if old.Method != "BTREE" || old.Invisible || old.Unique != (desired.IndexType == DBIndexTypeUnique) || len(old.Parts) != len(desired.Columns) {
		return false
	}
	for i, p := range old.Parts {
		c := desired.Columns[i]
		if !p.Name.Valid || p.Expression.Valid || !strings.EqualFold(p.Name.String, c.ColumnName) || p.Descending == c.ASC || p.Prefix.Valid != c.SubPart.Valid || (p.Prefix.Valid && p.Prefix.Int64 != c.SubPart.Int64) {
			return false
		}
	}
	return true
}

func migrationColumnIndexed(column string, primary []string, desired map[string]*DBIndex) bool {
	if len(primary) > 0 && strings.EqualFold(primary[0], column) {
		return true
	}
	for _, idx := range desired {
		if len(idx.Columns) > 0 && strings.EqualFold(idx.Columns[0].ColumnName, column) && !idx.Columns[0].SubPart.Valid {
			return true
		}
	}
	return false
}

func migrationAlterStep(name string, clauses []string, reason string, destructive bool, o MigrationOptions) MigrationStep {
	if o.Algorithm != "" {
		clauses = append(clauses, "ALGORITHM="+o.Algorithm)
	}
	if o.Lock != "" {
		clauses = append(clauses, "LOCK="+o.Lock)
	}
	return MigrationStep{Table: name, SQL: "ALTER TABLE " + quoteMigrationIdentifier(name) + " " + strings.Join(clauses, ", "), Reason: reason, Destructive: destructive}
}

func planMigrationTable(s *migrationSchema, old *migrationTableState, m *migrationModel, indexes map[string]*DBIndex, o MigrationOptions, noBackslash bool) ([]string, []string, bool, error) {
	var drops, columns, keys, reasons []string
	destructive := false
	if m != nil {
		oldColumns := make(map[string]*migrationColumnState)
		for _, c := range old.Columns {
			oldColumns[strings.ToLower(c.Name)] = c
		}
		for _, c := range m.Columns {
			prev := oldColumns[strings.ToLower(c.Name)]
			delete(oldColumns, strings.ToLower(c.Name))
			if prev == nil {
				unsafeAdd := !c.Nullable && c.Default == nil && !c.Auto
				if unsafeAdd && !o.AllowUnsafeChanges {
					return nil, nil, false, fmt.Errorf("%w: adding NOT NULL %s.%s without an explicit rndefault requires a backfill", ErrUnsafeMigration, m.Name, c.Name)
				}
				def, err := migrationColumnDefinition(c, nil, s.Charset, noBackslash)
				if err != nil {
					return nil, nil, false, err
				}
				columns = append(columns, "ADD COLUMN "+def)
				reasons = append(reasons, "add column "+c.Name)
				destructive = destructive || unsafeAdd
				continue
			}
			prevType, err := normalizeStoredMigrationType(prev.Type, noBackslash)
			if err != nil {
				return nil, nil, false, err
			}
			auto := strings.Contains(strings.ToLower(prev.Extra), "auto_increment")
			binaryChanged := c.Binary && !strings.HasSuffix(prev.Collation.String, "_bin")
			defaultChanged := c.Default != nil && !sameMigrationDefault(*c.Default, prev, noBackslash)
			if prevType == c.Type && prev.Nullable == c.Nullable && auto == c.Auto && !binaryChanged && !defaultChanged {
				continue
			}
			if strings.Contains(strings.ToUpper(prev.Extra), "GENERATED") && !strings.Contains(strings.ToUpper(prev.Extra), "DEFAULT_GENERATED") {
				return nil, nil, false, fmt.Errorf("gorn: generated column %s.%s requires explicit SQL", m.Name, c.Name)
			}
			unsafe := !safeMigrationTypeChange(prevType, c.Type, noBackslash) || (prev.Nullable && !c.Nullable) || auto != c.Auto || binaryChanged
			if unsafe && !o.AllowUnsafeChanges {
				return nil, nil, false, fmt.Errorf("%w: changing definition of %s.%s", ErrUnsafeMigration, m.Name, c.Name)
			}
			// Keep FKs installed throughout. Cross-table type changes cannot be
			// made atomic by toggling FOREIGN_KEY_CHECKS; reject before any DDL.
			if prevType != c.Type && migrationColumnHasFK(s, old.Name, c.Name) {
				return nil, nil, false, fmt.Errorf("gorn: type change of foreign-key column %s.%s requires a staged explicit migration", m.Name, c.Name)
			}
			def, err := migrationColumnDefinition(c, prev, s.Charset, noBackslash)
			if err != nil {
				return nil, nil, false, err
			}
			columns = append(columns, "MODIFY COLUMN "+def)
			reasons = append(reasons, "modify column "+c.Name)
			destructive = destructive || unsafe
		}
		if o.AllowDropColumns {
			// Preserve deterministic column order and refuse implicit index/FK
			// deletion by MySQL, even when dropping a column was requested.
			for _, c := range old.Columns {
				if oldColumns[strings.ToLower(c.Name)] == nil {
					continue
				}
				if migrationColumnHasFK(s, old.Name, c.Name) || migrationColumnInAnyIndex(old, c.Name) {
					return nil, nil, false, fmt.Errorf("gorn: drop of indexed/referenced column %s.%s requires an explicit staged migration", old.Name, c.Name)
				}
				drops = append(drops, "DROP COLUMN "+quoteMigrationIdentifier(c.Name))
				reasons = append(reasons, "drop column "+c.Name)
				destructive = true
			}
		}
		var oldPrimary []string
		if idx := old.Indexes["primary"]; idx != nil {
			for _, part := range idx.Parts {
				oldPrimary = append(oldPrimary, part.Name.String)
			}
		}
		if !sameMigrationNames(oldPrimary, m.Primary) {
			if len(m.Primary) == 0 && !o.AllowUnsafeChanges {
				// An omitted PK is preserved by default, like an omitted column.
			} else {
				if len(oldPrimary) > 0 && !o.AllowUnsafeChanges {
					return nil, nil, false, fmt.Errorf("%w: replacing primary key on %s", ErrUnsafeMigration, old.Name)
				}
				if len(oldPrimary) > 0 {
					drops = append(drops, "DROP PRIMARY KEY")
					destructive = true
				}
				if len(m.Primary) > 0 {
					keys = append(keys, "ADD PRIMARY KEY ("+migrationIdentifierList(m.Primary)+")")
				}
				reasons = append(reasons, "change primary key")
			}
		}
	}
	for _, ik := range sortedMigrationIndexKeys(indexes) {
		idx := indexes[ik]
		prev := old.Indexes[ik]
		if prev != nil && equalMigrationIndex(prev, idx) {
			continue
		}
		if prev != nil {
			if prev.Method != "BTREE" || prev.Invisible {
				return nil, nil, false, fmt.Errorf("gorn: advanced index %s.%s requires explicit SQL", old.Name, prev.Name)
			}
			for _, part := range prev.Parts {
				if part.Expression.Valid {
					return nil, nil, false, fmt.Errorf("gorn: functional index %s.%s requires explicit SQL", old.Name, prev.Name)
				}
			}
			if prev.Unique && !o.AllowUnsafeChanges {
				return nil, nil, false, fmt.Errorf("%w: replacing unique index %s.%s", ErrUnsafeMigration, old.Name, prev.Name)
			}
			drops = append(drops, "DROP INDEX "+quoteMigrationIdentifier(prev.Name))
			destructive = destructive || prev.Unique
		}
		keys = append(keys, "ADD "+migrationIndexDefinition(idx))
		reasons = append(reasons, "add or replace index "+idx.IndexName)
	}
	if o.AllowDropIndexes {
		oldKeys := make([]string, 0, len(old.Indexes))
		for k := range old.Indexes {
			oldKeys = append(oldKeys, k)
		}
		sort.Strings(oldKeys)
		for _, ik := range oldKeys {
			idx := old.Indexes[ik]
			if ik == "primary" || indexes[ik] != nil || strings.HasPrefix(strings.ToUpper(idx.Name), "GORN_FK_") {
				continue
			}
			if idx.Method != "BTREE" || idx.Invisible {
				return nil, nil, false, fmt.Errorf("gorn: drop of advanced index %s.%s requires explicit SQL", old.Name, idx.Name)
			}
			drops = append(drops, "DROP INDEX "+quoteMigrationIdentifier(idx.Name))
			reasons = append(reasons, "drop index "+idx.Name)
			destructive = true
		}
	}
	return append(append(drops, columns...), keys...), reasons, destructive, nil
}

func migrationColumnHasFK(s *migrationSchema, table, column string) bool {
	for _, fk := range s.ForeignKeys {
		if fk.Schema == s.Name && s.key(fk.Table) == s.key(table) {
			for _, c := range fk.Columns {
				if strings.EqualFold(c, column) {
					return true
				}
			}
		}
		if fk.RefSchema == s.Name && s.key(fk.RefTable) == s.key(table) {
			for _, c := range fk.RefColumns {
				if strings.EqualFold(c, column) {
					return true
				}
			}
		}
	}
	return false
}
func migrationColumnInAnyIndex(t *migrationTableState, column string) bool {
	for _, idx := range t.Indexes {
		for _, p := range idx.Parts {
			if p.Expression.Valid || strings.EqualFold(p.Name.String, column) {
				return true
			}
		}
	}
	return false
}
func sameMigrationNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}
func canonicalMigrationRule(rule string) string {
	if oneOf(rule, "", "NO ACTION") {
		return "RESTRICT"
	}
	return rule
}

func planMigrationForeignKeys(s *migrationSchema, m *migrationModel, old *migrationTableState, models map[string]*migrationModel, o MigrationOptions) ([]string, bool, error) {
	var drops, adds []string
	destructive := false
	var oldKeys []*migrationForeignKey
	if old != nil {
		oldKeys = old.ForeignKeys
	}
	matched := make(map[string]bool)
	usedNames := make(map[string]bool)
	for _, fk := range s.ForeignKeys {
		usedNames[strings.ToLower(fk.Name)] = true
	}
	for _, fk := range m.ForeignKeys {
		ref := s.Tables[s.key(fk.RefTable)]
		refModel := models[s.key(fk.RefTable)]
		found := false
		if refModel != nil {
			for _, c := range refModel.Columns {
				if strings.EqualFold(c.Name, fk.RefColumn) {
					found = true
				}
			}
		}
		if ref != nil && (!o.AllowDropColumns || refModel == nil) {
			for _, c := range ref.Columns {
				if strings.EqualFold(c.Name, fk.RefColumn) {
					found = true
				}
			}
		}
		if !found {
			return nil, false, fmt.Errorf("gorn: foreign key %s.%s references missing %s.%s; include all new tables in one plan", m.Name, fk.Column, fk.RefTable, fk.RefColumn)
		}
		var equivalent, conflict *migrationForeignKey
		for _, prev := range oldKeys {
			if len(prev.Columns) != 1 || !strings.EqualFold(prev.Columns[0], fk.Column) {
				continue
			}
			if prev.RefSchema == s.Name && s.key(prev.RefTable) == s.key(fk.RefTable) && len(prev.RefColumns) == 1 && strings.EqualFold(prev.RefColumns[0], fk.RefColumn) && (fk.Update == "" || canonicalMigrationRule(prev.UpdateRule) == canonicalMigrationRule(fk.Update)) && (fk.Delete == "" || canonicalMigrationRule(prev.DeleteRule) == canonicalMigrationRule(fk.Delete)) {
				equivalent = prev
				break
			}
			conflict = prev
		}
		if equivalent != nil {
			matched[equivalent.Name] = true
			continue
		}
		name := migrationObjectName("GORN_FK_", m.Name, fk.Column)
		if conflict != nil {
			if !o.AllowUnsafeChanges {
				return nil, false, fmt.Errorf("%w: replacing foreign key %s.%s", ErrUnsafeMigration, m.Name, conflict.Name)
			}
			name = conflict.Name
			matched[name] = true
			drops = append(drops, "DROP FOREIGN KEY "+quoteMigrationIdentifier(name))
			destructive = true
		} else if usedNames[strings.ToLower(name)] {
			return nil, false, fmt.Errorf("gorn: generated foreign-key name collision %s", name)
		}
		usedNames[strings.ToLower(name)] = true
		update, deleteRule := fk.Update, fk.Delete
		if conflict != nil {
			if update == "" {
				update = conflict.UpdateRule
			}
			if deleteRule == "" {
				deleteRule = conflict.DeleteRule
			}
		}
		if update == "" {
			update = "RESTRICT"
		}
		if deleteRule == "" {
			deleteRule = "RESTRICT"
		}
		adds = append(adds, "ADD CONSTRAINT "+quoteMigrationIdentifier(name)+" FOREIGN KEY ("+quoteMigrationIdentifier(fk.Column)+") REFERENCES "+quoteMigrationIdentifier(fk.RefTable)+" ("+quoteMigrationIdentifier(fk.RefColumn)+") ON UPDATE "+update+" ON DELETE "+deleteRule)
	}
	if o.AllowDropForeignKeys {
		for _, prev := range oldKeys {
			if !matched[prev.Name] {
				drops = append(drops, "DROP FOREIGN KEY "+quoteMigrationIdentifier(prev.Name))
				destructive = true
			}
		}
	}
	return append(drops, adds...), destructive, nil
}

// Used by GetIndexes without conflating identically named indexes on different
// tables. Unsupported index features are an error, never a partial definition.
func publicMigrationIndex(table string, idx *migrationIndexState) (*DBIndex, error) {
	if idx.Method != "BTREE" || idx.Invisible {
		return nil, fmt.Errorf("gorn: index %s.%s has unsupported method or visibility", table, idx.Name)
	}
	typ := DBIndexTypeIndex
	if idx.Unique {
		typ = DBIndexTypeUnique
	}
	result := &DBIndex{TableName: table, IndexName: idx.Name, IndexType: typ}
	for _, p := range idx.Parts {
		if !p.Name.Valid || p.Expression.Valid {
			return nil, fmt.Errorf("gorn: index %s.%s contains an expression", table, idx.Name)
		}
		prefix := sql.NullInt64{}
		if p.Prefix.Valid {
			prefix = p.Prefix
		}
		result.Columns = append(result.Columns, &DBIndexColumn{ColumnName: p.Name.String, SubPart: prefix, ASC: !p.Descending})
	}
	return result, nil
}
