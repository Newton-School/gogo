package migrations

import "github.com/Newton-School/gogo/core/db"

func (e *Executor) validateIndexOperations(migration Migration, reverse bool) error {
	if err := e.validateConstraintOperations(migration); err != nil {
		return err
	}
	for _, operation := range migration.Operations {
		if operation.Kind == "create_model" && !reverse && !migration.NonAtomic {
			for _, index := range operation.Schema.Indexes {
				if index.Concurrent {
					return &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent indexes cannot be created inside an atomic model migration"}
				}
			}
		}
		removes := operation.Kind == "remove_index" && !reverse || operation.Kind == "add_index" && reverse
		if removes {
			if _, supported := e.Editor.(db.IndexLifecycleEditor); !supported {
				return &db.Error{Code: db.UnsupportedFeature, Message: "Historical index removal requires a definition-aware schema editor"}
			}
			if operation.Index.Concurrent || migration.NonAtomic {
				return &db.Error{Code: db.UnsupportedFeature, Message: "Index lifecycle removal requires an atomic nonconcurrent migration"}
			}
		}
		if operation.Kind == "add_index" && !reverse && operation.Index.Concurrent && !migration.NonAtomic {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent index creation requires an explicit non-atomic migration"}
		}
		if operation.Kind == "remove_index" && operation.Index.Concurrent {
			return &db.Error{Code: db.UnsupportedFeature, Message: "Concurrent index removal requires an explicit online migration"}
		}
	}
	return nil
}
