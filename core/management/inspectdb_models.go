package management

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func mapInspectedModel(namespace string, relation db.CatalogRelation, names map[string]string, options InspectDBOptions) (inspectedModel, error) {
	result := inspectedModel{catalog: relation, schema: models.Schema{AppLabel: options.AppLabel, Name: names[relation.Name], Table: relation.Name, Unmanaged: true, Comment: relation.Comment, Tablespace: relation.Tablespace}}
	schema := &result.schema
	fail := func(message string) (inspectedModel, error) {
		return inspectedModel{}, inspectionError(relation.Name, "", message)
	}
	switch relation.Kind {
	case "table", "partitioned_table", "partition":
	case "view", "materialized_view":
		if !options.Catalog.IncludeViews {
			return fail("view generation requires IncludeViews")
		}
	default:
		return fail("unsupported relation kind")
	}
	if len(relation.Columns) == 0 || len(relation.Columns) > 1600 || len(relation.Constraints) > 1024 || len(relation.Indexes) > 1024 {
		return fail("invalid catalog column, constraint or index count")
	}
	columns := map[string]int{}
	goNames := map[string]bool{"Base": true, "Schema": true, "ModelState": true}
	for _, column := range relation.Columns {
		if !models.ValidIdentifier(column.Name) || len(column.Name) > 63 || strings.Contains(column.Name, "__") {
			return fail("unsupported column identifier")
		}
		if _, exists := columns[column.Name]; exists {
			return fail("duplicate catalog column")
		}
		if column.MappingIssue != "" {
			return inspectedModel{}, inspectionError(relation.Name, column.Name, column.MappingIssue)
		}
		field := column.Field
		if field.Name != column.Name || field.DBColumn() != column.Name || field.Collation != column.Collation {
			return fail("column descriptor disagrees with observed identity")
		}
		goType, err := inspectionFieldType(field, 0)
		if err != nil {
			return inspectedModel{}, inspectionError(relation.Name, column.Name, err.Error())
		}
		name := inspectionGoName(column.Name)
		if goNames[name] {
			name += "Field"
		}
		base := name
		for n := 2; goNames[name]; n++ {
			name = fmt.Sprintf("%s%d", base, n)
		}
		goNames[name] = true
		field = (models.Schema{Fields: []models.Field{field}}).Clone().Fields[0]
		field.StructField, field.Column = name, column.Name
		if field.Null {
			goType = "*" + goType
		}
		columns[column.Name] = len(schema.Fields)
		schema.Fields = append(schema.Fields, field)
		result.fields = append(result.fields, inspectedField{field: field, goType: goType})
	}
	constraints := slices.Clone(relation.Constraints)
	slices.SortFunc(constraints, func(a, b db.CatalogConstraint) int { return strings.Compare(a.Name, b.Name) })
	result.catalog.Constraints = constraints
	constraintNames := map[string]string{}
	indexByConstraint := map[string]db.CatalogIndex{}
	for _, index := range relation.Indexes {
		if index.Constraint != "" {
			if _, exists := indexByConstraint[index.Constraint]; exists {
				return fail("multiple indexes claim the same constraint")
			}
			indexByConstraint[index.Constraint] = index
		}
	}
	for _, constraint := range constraints {
		if constraint.MappingIssue != "" {
			return fail(constraint.MappingIssue)
		}
		if !models.ValidIdentifier(constraint.Name) || constraintNames[constraint.Name] != "" || !constraint.Validated {
			return fail("invalid, duplicate or unvalidated constraint")
		}
		constraintNames[constraint.Name] = constraint.Kind
		for _, name := range constraint.Columns {
			if _, ok := columns[name]; !ok {
				return fail("constraint references an absent column")
			}
		}
		switch constraint.Kind {
		case "primary_key":
			if schema.PrimaryKey != nil || len(constraint.Columns) == 0 || constraint.Deferrable {
				return fail("duplicate, empty or deferrable primary key is not supported")
			}
			schema.PrimaryKey = slices.Clone(constraint.Columns)
			if index, ok := indexByConstraint[constraint.Name]; !ok || !index.Primary || !index.Unique || !slices.Equal(index.Columns, constraint.Columns) {
				return fail("primary-key constraint index metadata is incomplete")
			}
			if index := indexByConstraint[constraint.Name]; len(index.Include) != 0 || index.Condition != "" {
				return fail("primary-key include/predicate metadata requires an explicit mapping")
			}
		case "not_null":
			if len(constraint.Columns) != 1 || schema.Fields[columns[constraint.Columns[0]]].Null {
				return fail("not-null constraint disagrees with column nullability")
			}
		case "unique":
			if constraint.Deferrable && !constraint.InitiallyDeferred {
				return fail("initially-immediate deferrable uniqueness is not representable")
			}
			index, ok := indexByConstraint[constraint.Name]
			if !ok || !index.Unique || index.Primary || !slices.Equal(index.Columns, constraint.Columns) {
				return fail("unique constraint index metadata is incomplete")
			}
			if err := validateInspectedIndex(index, *schema); err != nil {
				return fail(err.Error())
			}
			if len(index.Include) != 0 || index.Condition != "" {
				return fail("unique constraint include/predicate metadata is not representable")
			}
			distinct := index.NullsDistinct
			schema.Constraints = append(schema.Constraints, models.Constraint{Name: constraint.Name, Kind: "unique", Fields: slices.Clone(constraint.Columns), Deferrable: constraint.Deferrable, NullsDistinct: &distinct})
		case "check":
			if constraint.Expression == "" || constraint.Deferrable {
				return fail("unsupported check constraint metadata")
			}
			schema.Constraints = append(schema.Constraints, models.Constraint{Name: constraint.Name, Kind: "check", Expression: constraint.Expression})
		case "foreign_key":
			if len(constraint.Columns) != 1 || len(constraint.ReferencedColumns) != 1 || constraint.ReferencedSchema != namespace || names[constraint.ReferencedTable] == "" || constraint.Deferrable || constraint.OnUpdate != "NO ACTION" {
				return fail("foreign key requires one selected same-schema target column, NO ACTION update and nondeferrable metadata")
			}
			policies := map[string]models.DeletePolicy{"NO ACTION": models.DoNothing, "RESTRICT": models.Restrict, "CASCADE": models.Cascade, "SET NULL": models.SetNull}
			policy, ok := policies[constraint.OnDelete]
			if !ok {
				return fail("foreign-key delete behavior requires an explicit mapping")
			}
			i := columns[constraint.Columns[0]]
			field := &schema.Fields[i]
			if field.Relation != nil || field.Kind == models.Generated || field.IsAuto() {
				return fail("overlapping or generated foreign-key source column is not supported")
			}
			field.Kind = models.ForeignKey
			field.Relation = &models.Relation{Target: options.AppLabel + "." + names[constraint.ReferencedTable], TargetFields: slices.Clone(constraint.ReferencedColumns), OnDelete: policy, RelatedName: strings.ToLower(schema.Name) + "_" + field.Name + "_set"}
		default:
			return fail("constraint kind requires an explicit mapping")
		}
	}
	if schema.PrimaryKey == nil {
		keys := options.PrimaryKeys[relation.Name]
		if len(keys) == 0 {
			return fail("no primary key; explicitly supply verified non-null unique row identity columns")
		}
		for _, key := range keys {
			if _, ok := columns[key]; !ok {
				return fail("explicit primary key references an absent column")
			}
		}
		schema.PrimaryKey = slices.Clone(keys)
	} else if len(options.PrimaryKeys[relation.Name]) > 0 {
		return fail("explicit primary keys cannot replace a database primary key")
	}
	for i := range schema.Fields {
		field := &schema.Fields[i]
		primary := slices.Contains(schema.PrimaryKey, field.Name)
		if len(options.PrimaryKeys[relation.Name]) == 0 && field.PrimaryKey != primary {
			return fail("primary-key descriptor disagrees with its catalog constraint")
		}
		field.PrimaryKey = primary
		if primary && len(options.PrimaryKeys[relation.Name]) > 0 {
			field.Null = false
			result.fields[i].goType = strings.TrimPrefix(result.fields[i].goType, "*")
		}
		result.fields[i].field = *field
	}
	indexes := slices.Clone(relation.Indexes)
	slices.SortFunc(indexes, func(a, b db.CatalogIndex) int { return strings.Compare(a.Name, b.Name) })
	result.catalog.Indexes = indexes
	seenIndexes := map[string]bool{}
	for _, index := range indexes {
		if seenIndexes[index.Name] {
			return fail("duplicate catalog index")
		}
		seenIndexes[index.Name] = true
		if index.Constraint != "" {
			kind := constraintNames[index.Constraint]
			if kind != "primary_key" && kind != "unique" {
				return fail("index references an unsupported or absent constraint")
			}
			if kind == "primary_key" && (!index.Primary || !index.Unique || !slices.Equal(index.Columns, schema.PrimaryKey)) {
				return fail("primary-key index metadata disagrees with constraint")
			}
			if err := validateInspectedIndex(index, *schema); err != nil {
				return fail(err.Error())
			}
			continue
		}
		if err := validateInspectedIndex(index, *schema); err != nil {
			return fail(err.Error())
		}
		var distinct *bool
		if index.Unique {
			value := index.NullsDistinct
			distinct = &value
		}
		schema.Indexes = append(schema.Indexes, models.Index{Name: index.Name, Fields: slices.Clone(index.Columns), Unique: index.Unique, Method: index.Method, Condition: index.Condition, Include: slices.Clone(index.Include), NullsDistinct: distinct})
	}
	if err := schema.Validate(); err != nil {
		return fail(err.Error())
	}
	for i := range schema.Fields {
		field := &schema.Fields[i]
		if field.Kind != models.ForeignKey {
			continue
		}
		unique := len(schema.PrimaryKey) == 1 && schema.PrimaryKey[0] == field.Name
		for _, constraint := range schema.Constraints {
			unique = unique || constraint.Kind == "unique" && len(constraint.Fields) == 1 && constraint.Fields[0] == field.Name && constraint.Condition == ""
		}
		for _, index := range schema.Indexes {
			unique = unique || index.Unique && len(index.Fields) == 1 && index.Fields[0] == field.Name && index.Condition == ""
		}
		if unique {
			field.Kind, field.Unique = models.OneToOne, true
			field.Relation.RelatedName = strings.TrimSuffix(field.Relation.RelatedName, "_set")
		}
		result.fields[i].field = *field
	}
	return result, nil
}

func validateInspectedIndex(index db.CatalogIndex, schema models.Schema) error {
	if index.MappingIssue != "" {
		return errors.New(index.MappingIssue)
	}
	if !models.ValidIdentifier(index.Name) || !models.ValidIdentifier(index.Method) || len(index.Columns) == 0 || !index.Valid || !index.Ready || len(index.Expressions) != len(index.Columns) || len(index.Options) != len(index.Columns) || len(index.OpClasses) != len(index.Columns) || len(index.DefaultOpClasses) != len(index.Columns) || len(index.Collations) != len(index.Columns) {
		return errors.New("index metadata is incomplete or unsupported")
	}
	for i, name := range index.Columns {
		field, ok := schema.Field(name)
		if !ok || index.Expressions[i] != "" || index.Options[i] != 0 || !index.DefaultOpClasses[i] || index.Collations[i] != field.Collation {
			return errors.New("index expression, ordering, opclass or collation requires an explicit mapping")
		}
	}
	for _, name := range index.Include {
		if _, ok := schema.Field(name); !ok {
			return errors.New("index includes an absent column")
		}
	}
	return nil
}

func inspectionFieldType(field models.Field, depth int) (string, error) {
	if depth > 1 || field.Codec != nil || field.Default != nil || field.DefaultFunc != nil || len(field.Validators) != 0 || field.Relation != nil {
		return "", errors.New("custom field behavior requires explicit registered source mapping")
	}
	// Reject unrelated descriptor options instead of silently omitting them.
	allowed := models.Field{Name: field.Name, Column: field.Column, Kind: field.Kind, Null: field.Null, Editable: field.Editable, PrimaryKey: field.PrimaryKey, MaxLength: field.MaxLength, MaxDigits: field.MaxDigits, DecimalPlaces: field.DecimalPlaces, DBDefault: field.DBDefault, GeneratedExpression: field.GeneratedExpression, Collation: field.Collation, Element: field.Element}
	if !reflect.DeepEqual(field, allowed) {
		return "", errors.New("field metadata requires an explicit source mapping")
	}
	if field.Kind == models.Generated {
		if field.Element == nil || field.GeneratedExpression == "" || field.Editable || field.Element.Kind == models.Generated {
			return "", errors.New("generated field metadata is incomplete")
		}
		return inspectionFieldType(*field.Element, depth+1)
	}
	if field.Element != nil || field.GeneratedExpression != "" {
		return "", errors.New("unexpected scalar element/generated metadata")
	}
	switch field.Kind {
	case models.SmallAuto, models.SmallInteger:
		return "int16", nil
	case models.Auto, models.Integer:
		return "int32", nil
	case models.BigAuto, models.BigInteger:
		return "int64", nil
	case models.Char, models.Text, models.UUID:
		return "string", nil
	case models.Decimal:
		if field.MaxDigits > 0 && field.DecimalPlaces >= 0 && field.DecimalPlaces <= field.MaxDigits {
			return "string", nil
		}
	case models.Float:
		return "float64", nil
	case models.Boolean:
		return "bool", nil
	case models.Date, models.DateTime:
		return "time.Time", nil
	case models.JSON:
		return "json.RawMessage", nil
	case models.Binary:
		return "[]byte", nil
	}
	return "", fmt.Errorf("field kind %q is not supported by the source renderer", field.Kind)
}
