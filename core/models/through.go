package models

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ImplicitThrough describes the auto-created intermediary for a many-to-many
// field. Its rows are relationship effects, not independently registered apps.
func ImplicitThrough(source Schema, field Field, target Schema) (Schema, error) {
	if field.Kind != ManyToMany || field.Relation == nil || field.Relation.Through != "" {
		return Schema{}, errors.New("models: implicit through requires an automatic many-to-many field")
	}
	if len(source.PKFields()) != 1 || len(target.PKFields()) != 1 {
		return Schema{}, errors.New("models: automatic through requires scalar source and target primary keys")
	}
	table := shortIdentifier(source.DBTable() + "_" + field.DBColumn())
	return Schema{AppLabel: source.AppLabel, Name: shortIdentifier(source.Name + "_" + field.Name), Table: table, AutoCreatedBy: source.Key(), AutoCreatedField: field.Name, Fields: []Field{BigAutoField("id"), ForeignKeyField("source_id", Relation{Target: source.Key(), TargetFields: []string{source.PKFields()[0].Name}, OnDelete: Cascade, RelatedName: "+"}), ForeignKeyField("target_id", Relation{Target: target.Key(), TargetFields: []string{target.PKFields()[0].Name}, OnDelete: Cascade, RelatedName: "+"})}, Constraints: []Constraint{{Name: shortIdentifier(table + "_pair_key"), Kind: "unique", Fields: []string{"source_id", "target_id"}}}}, nil
}
func shortIdentifier(name string) string {
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:50] + "_" + hex.EncodeToString(sum[:6])
}
