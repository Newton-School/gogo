package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

// Prefetch customizes one relation query. Children are relative to its returned
// records. ToAttr uses a separate instance-local cache without overwriting fields.
type Prefetch struct {
	Path, ToAttr string
	Where        db.Predicate
	OrderBy      []string
	Children     []Prefetch
}
type prefetchNode struct {
	name, attr string
	binding    relationBinding
	where      db.Predicate
	order      []string
	children   []*prefetchNode
}
type eagerBudget struct{ remaining int }

var ErrEagerLimit = errors.New("orm: eager graph exceeds its configured object bound; page the root query or choose a deliberate larger bound")

func (b *eagerBudget) take(count int) error {
	b.remaining -= count
	if b.remaining < 0 {
		return ErrEagerLimit
	}
	return nil
}

func clonePrefetches(values []Prefetch) []Prefetch {
	result := append([]Prefetch(nil), values...)
	for i := range result {
		result[i].Where = clonePredicate(result[i].Where)
		result[i].OrderBy = append([]string(nil), result[i].OrderBy...)
		result[i].Children = clonePrefetches(result[i].Children)
	}
	return result
}
func (q Query[T]) PrefetchRelated(paths ...string) Query[T] {
	q = q.clone()
	q.prefetches = nil
	for _, path := range paths {
		q.prefetches = append(q.prefetches, Prefetch{Path: path})
	}
	return q
}
func (q Query[T]) Prefetch(specs ...Prefetch) Query[T] {
	q = q.clone()
	q.prefetches = clonePrefetches(specs)
	return q
}

// EagerLimit bounds roots, intermediary rows and fetched related objects together.
// The default is 10000; it never silently truncates a related collection.
func (q Query[T]) EagerLimit(maximum int) Query[T] {
	q = q.clone()
	if maximum < 1 {
		q.err = errors.New("orm: eager limit must be positive")
	}
	q.eagerLimit = maximum
	return q
}

func (q Query[T]) allPrefetched(ctx context.Context) ([]T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if q.store.Registry == nil {
		return nil, errors.New("orm: prefetch requires a complete model registry")
	}
	nodes, err := compilePrefetches(q.store, q.schema, q.prefetches, map[string]bool{q.schema.Key(): true}, 0)
	if err != nil {
		return nil, err
	}
	maximum := q.eagerLimit
	if maximum == 0 {
		maximum = 10000
	}
	if maximum == int(^uint(0)>>1) {
		return nil, errors.New("orm: eager limit is too large")
	}
	root := q
	root.prefetches = nil
	if root.selectAST.Limit == nil || *root.selectAST.Limit > maximum {
		root = root.Limit(maximum + 1)
	}
	objects, err := root.All(ctx)
	if err != nil {
		return nil, err
	}
	budget := &eagerBudget{remaining: maximum}
	if err := budget.take(len(objects)); err != nil {
		return nil, err
	}
	records := make([]models.Record, len(objects))
	for i, object := range objects {
		record, err := models.Bind(object)
		if err != nil {
			return nil, err
		}
		records[i] = record
	}
	if err := countEagerCache(ctx, records, budget); err != nil {
		return nil, err
	}
	if err := loadPrefetches(ctx, q.store, records, nodes, q.scope, budget); err != nil {
		return nil, err
	}
	return objects, nil
}

func compilePrefetches(store *Store, schema models.Schema, specs []Prefetch, ancestors map[string]bool, depth int) ([]*prefetchNode, error) {
	if depth > 8 {
		return nil, errors.New("orm: prefetch depth exceeds eight")
	}
	nodes := []*prefetchNode{}
	for _, spec := range specs {
		parts := strings.Split(spec.Path, "__")
		name := parts[0]
		if !models.ValidIdentifier(name) {
			return nil, errors.New("orm: invalid prefetch relation path")
		}
		if len(parts) > 1 {
			child := spec
			child.Path = strings.Join(parts[1:], "__")
			spec = Prefetch{Path: name, Children: []Prefetch{child}}
		}
		attr := spec.ToAttr
		if attr == "" {
			attr = name
		} else {
			if !models.ValidIdentifier(attr) {
				return nil, errors.New("orm: invalid prefetch cache attribute")
			}
			if _, exists := schema.Field(attr); exists {
				return nil, errors.New("orm: prefetch cache attribute conflicts with a model field")
			}
		}
		var node *prefetchNode
		for _, existing := range nodes {
			if existing.attr == attr {
				if existing.name != name || !reflect.DeepEqual(existing.where, spec.Where) || !reflect.DeepEqual(existing.order, spec.OrderBy) {
					return nil, errors.New("orm: conflicting prefetch queries for one cache attribute")
				}
				node = existing
				break
			}
		}
		if node == nil {
			prototype, err := models.NewRecord(schema)
			if err != nil {
				return nil, err
			}
			binding, err := (RelationManager{Store: store, Source: prototype, Name: name}).resolve()
			if err != nil {
				return nil, err
			}
			if ancestors[binding.target.Key()] {
				return nil, errors.New("orm: cyclic prefetch path rejected")
			}
			node = &prefetchNode{name: name, attr: attr, binding: binding, where: spec.Where, order: append([]string(nil), spec.OrderBy...)}
			nodes = append(nodes, node)
		}
		next := map[string]bool{}
		for key, value := range ancestors {
			next[key] = value
		}
		next[node.binding.target.Key()] = true
		children, err := compilePrefetches(store, node.binding.target, spec.Children, next, depth+1)
		if err != nil {
			return nil, err
		}
		// Independent repeated prefixes may add new children, but must not replace
		// an already configured child query with a different interpretation.
		for _, child := range children {
			duplicate := false
			for _, existing := range node.children {
				if existing.attr == child.attr {
					if existing.name != child.name || !reflect.DeepEqual(existing.where, child.where) || !reflect.DeepEqual(existing.order, child.order) {
						return nil, errors.New("orm: conflicting nested prefetch queries")
					}
					existing.children = append(existing.children, child.children...)
					duplicate = true
					break
				}
			}
			if !duplicate {
				node.children = append(node.children, child)
			}
		}
	}
	return nodes, nil
}

func loadPrefetches(ctx context.Context, store *Store, roots []models.Record, nodes []*prefetchNode, scope QueryScope, budget *eagerBudget) error {
	if len(roots) == 0 {
		return nil
	}
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.attr == node.name && reflect.DeepEqual(node.where, db.Predicate{}) && len(node.order) == 0 {
			cached := []models.Record{}
			allLoaded := true
			for _, root := range roots {
				value, ok := root.State().Related[node.attr]
				if !ok {
					allLoaded = false
					break
				}
				records := []models.Record{}
				if value != nil {
					switch related := value.(type) {
					case models.Record:
						records = append(records, related)
					case []models.Record:
						records = related
					default:
						allLoaded = false
					}
				}
				cached = append(cached, records...)
			}
			if allLoaded {
				if err := loadPrefetches(ctx, store, cached, node.children, scope, budget); err != nil {
					return err
				}
				continue
			}
		}
		binding := node.binding
		var sourceField, targetField string
		if binding.through != nil {
			key, err := relationTargetField(roots[0].Schema(), binding.through.source)
			if err != nil {
				return err
			}
			sourceField = key.Name
			key, err = relationTargetField(binding.target, binding.through.target)
			if err != nil {
				return err
			}
			targetField = key.Name
		} else if binding.reverse {
			key, err := relationTargetField(roots[0].Schema(), binding.field)
			if err != nil {
				return err
			}
			sourceField = key.Name
			targetField = binding.field.Name
		} else {
			sourceField = binding.field.Name
			key, err := relationTargetField(binding.target, binding.field)
			if err != nil {
				return err
			}
			targetField = key.Name
		}
		keys, err := prefetchKeys(roots, sourceField)
		if err != nil {
			return err
		}
		var links []models.Record
		if binding.through != nil {
			through := binding.through
			joinScope := scope
			if through.automatic {
				joinScope = nil
			}
			if through.symmetrical && !through.automatic && scope != nil {
				return errors.New("orm: scoped symmetric explicit intermediary prefetch requires a dedicated application loader")
			}
			links, err = fetchPrefetchRows(ctx, store, through.schema, through.source.Name, keys, db.Predicate{}, nil, joinScope, budget, false)
			if err != nil {
				return err
			}
			keys, err = prefetchKeys(links, through.target.Name)
			if err != nil {
				return err
			}
		}
		rows, err := fetchPrefetchRows(ctx, store, binding.target, targetField, keys, node.where, node.order, scope, budget, binding.through != nil)
		if err != nil {
			return err
		}
		groups := map[string][]models.Record{}
		for _, row := range rows {
			value, err := row.Get(targetField)
			if err != nil {
				return err
			}
			key, err := scalarKey(value)
			if err != nil {
				return err
			}
			groups[key] = append(groups[key], row)
		}
		if binding.through != nil {
			linked := map[string]map[string]bool{}
			for _, link := range links {
				source, err := link.Get(binding.through.source.Name)
				if err != nil {
					return err
				}
				target, err := link.Get(binding.through.target.Name)
				if err != nil {
					return err
				}
				sourceKey, err := scalarKey(source)
				if err != nil {
					return err
				}
				targetKey, err := scalarKey(target)
				if err != nil {
					return err
				}
				if linked[targetKey] == nil {
					linked[targetKey] = map[string]bool{}
				}
				linked[targetKey][sourceKey] = true
			}
			groups = map[string][]models.Record{}
			for _, row := range rows {
				value, err := row.Get(targetField)
				if err != nil {
					return err
				}
				targetKey, err := scalarKey(value)
				if err != nil {
					return err
				}
				for sourceKey := range linked[targetKey] {
					groups[sourceKey] = append(groups[sourceKey], row)
				}
			}
		}
		for _, root := range roots {
			value, err := root.Get(sourceField)
			if err != nil {
				return err
			}
			key, err := scalarKey(value)
			if err != nil {
				return err
			}
			if root.State().Related == nil {
				root.State().Related = map[string]any{}
			}
			values := groups[key]
			if binding.through == nil && (!binding.reverse || binding.field.Kind == models.OneToOne) {
				if len(values) > 1 {
					return ErrMultipleObjects
				}
				if len(values) == 0 {
					root.State().Related[node.attr] = nil
				} else {
					root.State().Related[node.attr] = values[0]
				}
			} else {
				root.State().Related[node.attr] = append([]models.Record{}, values...)
			}
		}
		if err := loadPrefetches(ctx, store, rows, node.children, scope, budget); err != nil {
			return err
		}
	}
	return nil
}

func prefetchKeys(records []models.Record, field string) ([]any, error) {
	keys := []any{}
	seen := map[string]bool{}
	for _, record := range records {
		value, err := record.Get(field)
		if err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		key, err := scalarKey(value)
		if err != nil {
			return nil, err
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, value)
		}
	}
	return keys, nil
}

func fetchPrefetchRows(ctx context.Context, store *Store, schema models.Schema, keyField string, keys []any, where db.Predicate, order []string, scope QueryScope, budget *eagerBudget, globalOrder bool) ([]models.Record, error) {
	if len(keys) == 0 {
		return []models.Record{}, nil
	}
	query := For(store, func() *models.MapRecord { record, _ := models.NewRecord(schema); return record }).Filter(where).WithScope(scope)
	if len(order) > 0 {
		query = query.OrderBy(order...)
	}
	query, err := query.prepareRelated(ctx)
	if err != nil {
		return nil, err
	}
	_, args, err := query.Limit(budget.remaining + 1).SQLContext(ctx)
	if err != nil {
		return nil, err
	}
	maximum := 500
	if limiter, ok := store.Backend.(db.ParameterLimiter); ok {
		maximum = limiter.MaxParameters()
	}
	batch := maximum - len(args)
	if batch < 1 {
		return nil, errors.New("orm: prefetch filter exhausts backend parameter limit")
	}
	if globalOrder && len(keys) > batch && (len(order) > 0 || len(schema.Ordering) > 0) {
		return nil, errors.New("orm: ordered many-to-many prefetch exceeds one backend key batch; use a smaller root page")
	}
	result := []models.Record{}
	for start := 0; start < len(keys); start += batch {
		end := start + batch
		if end > len(keys) {
			end = len(keys)
		}
		rows, err := query.Filter(Q(keyField+"__in", keys[start:end])).Limit(budget.remaining + 1).All(ctx)
		if err != nil {
			return nil, err
		}
		if err := budget.take(len(rows)); err != nil {
			return nil, err
		}
		for _, row := range rows {
			result = append(result, row)
		}
	}
	return result, nil
}

// RelatedMany returns a copied collection slice from this instance's eager cache.
// The bool distinguishes a loaded empty collection from an unrequested relation.
func RelatedMany(record models.Record, name string) ([]models.Record, bool) {
	if record == nil || record.State() == nil {
		return nil, false
	}
	value, ok := record.State().Related[name]
	if !ok {
		return nil, false
	}
	records, ok := value.([]models.Record)
	return append([]models.Record{}, records...), ok
}

func countEagerCache(ctx context.Context, roots []models.Record, budget *eagerBudget) error {
	seen := map[*models.State]bool{}
	for _, root := range roots {
		seen[root.State()] = true
	}
	var visit func(models.Record) error
	visit = func(record models.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, value := range record.State().Related {
			children := []models.Record{}
			switch related := value.(type) {
			case models.Record:
				children = append(children, related)
			case []models.Record:
				children = related
			}
			for _, child := range children {
				if child == nil {
					continue
				}
				if seen[child.State()] {
					continue
				}
				seen[child.State()] = true
				if err := budget.take(1); err != nil {
					return err
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := visit(root); err != nil {
			return err
		}
	}
	return nil
}
