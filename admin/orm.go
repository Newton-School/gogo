package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// QueryScope binds database filtering and retained history to one stable scope.
type QueryScope struct {
	Predicate db.Predicate
	Identity  string
}
type ORMConfig struct {
	Store      *orm.Store
	Factories  map[string]func() models.Model
	QueryScope func(context.Context, auth.Principal, models.Schema) (QueryScope, error)
	// ValidateWrite must reject any record that moves outside the actor's scope.
	ValidateWrite                       func(context.Context, auth.Principal, models.Record) error
	Initialize                          func(context.Context, auth.Principal, models.Record) error
	CollectDeletion                     func(context.Context, auth.Principal, models.Record) (Deletion, error)
	DeleteGraph                         func(context.Context, auth.Principal, models.Record, func(context.Context, Deletion) error) error
	DeletionMaxObjects, DeletionMaxWork int
}
type ORMStore struct{ config ORMConfig }

func NewORMStore(config ORMConfig) (*ORMStore, error) {
	if config.Store == nil || config.Store.Backend == nil || config.QueryScope == nil || config.ValidateWrite == nil {
		return nil, errors.New("admin: ORM, query scope and write scope validator required")
	}
	factories := map[string]func() models.Model{}
	for key, factory := range config.Factories {
		if factory == nil {
			return nil, errors.New("admin: nil model factory")
		}
		record, err := models.Bind(factory())
		if err != nil {
			return nil, err
		}
		if record.Schema().Key() != key {
			return nil, errors.New("admin: factory key mismatch")
		}
		factories[key] = factory
	}
	config.Factories = factories
	return &ORMStore{config: config}, nil
}
func (s *ORMStore) Scope(ctx context.Context, p auth.Principal, site string, schema models.Schema) (ScopedStore, error) {
	factory, ok := s.config.Factories[schema.Key()]
	if !ok {
		return nil, ErrNotFound
	}
	scope, err := s.config.QueryScope(ctx, p, schema)
	if err != nil {
		return nil, err
	}
	if scope.Identity == "" {
		return nil, auth.ErrPermissionDenied
	}
	return &ormScoped{owner: s, principal: p, site: site, schema: schema, factory: factory, scope: scope}, nil
}

type ormScoped struct {
	owner     *ORMStore
	principal auth.Principal
	site      string
	schema    models.Schema
	factory   func() models.Model
	scope     QueryScope
}

func (s *ormScoped) query() orm.Query[models.Model] {
	return orm.For(s.owner.config.Store, s.factory).Filter(s.scope.Predicate)
}
func (s *ormScoped) List(ctx context.Context, request ListQuery) (Page, error) {
	query := s.query()
	if request.Search != "" {
		predicates := []db.Predicate{}
		for _, name := range request.SearchFields {
			predicates = append(predicates, orm.Q(name+"__icontains", request.Search))
		}
		if len(predicates) > 0 {
			query = query.Filter(orm.Or(predicates...))
		}
	}
	for name, value := range request.Filters {
		query = query.Filter(orm.Q(name, value))
	}
	count, err := query.Count(ctx)
	if err != nil {
		return Page{}, err
	}
	rows, err := query.OrderBy(request.Ordering...).Offset(request.Offset).Limit(request.Limit).All(ctx)
	if err != nil {
		return Page{}, err
	}
	result := Page{Count: count}
	for _, model := range rows {
		record, err := models.Bind(model)
		if err != nil {
			return Page{}, err
		}
		object, err := objectFromRecord(record)
		if err != nil {
			return Page{}, err
		}
		result.Objects = append(result.Objects, object)
	}
	return result, nil
}
func (s *ormScoped) Get(ctx context.Context, key string, lock bool) (Object, error) {
	predicate, err := decodeKey(s.schema, key)
	if err != nil {
		return Object{}, ErrNotFound
	}
	query := s.query().Filter(predicate)
	if lock {
		query = query.SelectForUpdate(false, false)
	}
	model, err := query.Get(ctx)
	if errors.Is(err, orm.ErrNotFound) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, err
	}
	record, err := models.Bind(model)
	if err != nil {
		return Object{}, err
	}
	return objectFromRecord(record)
}
func (s *ormScoped) New(ctx context.Context) (Object, error) {
	record, err := models.Bind(s.factory())
	if err != nil {
		return Object{}, err
	}
	if err = models.ApplyDefaults(record); err != nil {
		return Object{}, err
	}
	if s.owner.config.Initialize != nil {
		if err = s.owner.config.Initialize(ctx, s.principal, record); err != nil {
			return Object{}, err
		}
	}
	return Object{Record: record, Label: s.schema.Name}, nil
}
func (s *ormScoped) Save(ctx context.Context, object Object) (Object, error) {
	if !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) {
		return Object{}, errors.New("admin: save requires transaction")
	}
	if err := s.owner.config.ValidateWrite(ctx, s.principal, object.Record); err != nil {
		return Object{}, err
	}
	model, ok := models.Underlying(object.Record)
	if !ok {
		return Object{}, errors.New("admin: typed model is required")
	}
	options := orm.SaveOptions{ForceInsert: !object.Record.State().Persisted, ForceUpdate: object.Record.State().Persisted}
	options.Guard = func(ctx context.Context, record models.Record) error {
		if record.Schema().Key() != s.schema.Key() {
			return auth.ErrPermissionDenied
		}
		current, err := objectFromRecord(record)
		if err != nil {
			return err
		}
		if options.ForceUpdate && (object.ID == "" || current.ID != object.ID) {
			return auth.ErrPermissionDenied
		}
		return s.owner.config.ValidateWrite(ctx, s.principal, record)
	}
	if err := s.owner.config.Store.Save(ctx, model, options); err != nil {
		return Object{}, err
	}
	written, err := objectFromRecord(object.Record)
	if err != nil {
		return Object{}, err
	}
	if options.ForceUpdate && written.ID != object.ID {
		return Object{}, auth.ErrPermissionDenied
	}
	current, err := s.Get(ctx, written.ID, true)
	if errors.Is(err, ErrNotFound) {
		return Object{}, auth.ErrPermissionDenied
	}
	if err != nil {
		return Object{}, err
	}
	if err = s.owner.config.ValidateWrite(ctx, s.principal, current.Record); err != nil {
		return Object{}, err
	}
	return current, nil
}
func (s *ormScoped) Delete(ctx context.Context, object Object) error {
	return s.DeleteAuthorized(ctx, object, func(context.Context, Deletion) error { return nil })
}
func (s *ormScoped) DeleteAuthorized(ctx context.Context, object Object, authorize func(context.Context, Deletion) error) error {
	if !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) {
		return errors.New("admin: delete requires transaction")
	}
	if authorize == nil {
		return errors.New("admin: deletion authorization callback required")
	}
	if err := s.owner.config.ValidateWrite(ctx, s.principal, object.Record); err != nil {
		return err
	}
	if s.owner.config.DeleteGraph != nil {
		return s.owner.config.DeleteGraph(ctx, s.principal, object.Record, authorize)
	}
	collector := s.collector()
	collector.Authorize = func(ctx context.Context, plan orm.DeletionPlan) error {
		graph, err := s.deletionFromPlan(plan)
		if err != nil {
			return err
		}
		if err = authorize(ctx, graph); err != nil {
			return err
		}
		for _, target := range graph.Objects {
			if err = s.owner.config.ValidateWrite(ctx, s.principal, target.Record); err != nil {
				return err
			}
		}
		projected := map[string]models.Record{}
		for _, update := range graph.Updates {
			// Validate the projected record without mutating the collector snapshot.
			key := update.Object.Record.Schema().Key() + ":" + update.Object.ID
			record, ok := projected[key]
			if !ok {
				record, err = models.NewRecord(update.Object.Record.Schema())
				if err != nil {
					return err
				}
				for _, field := range record.Schema().Fields {
					if !field.IsStored() {
						continue
					}
					value, err := update.Object.Record.Get(field.Name)
					if err != nil {
						return err
					}
					if err = record.Set(field.Name, value); err != nil {
						return err
					}
				}
				projected[key] = record
			}
			if err = record.Set(update.Field, update.Value); err != nil {
				return err
			}
		}
		for _, record := range projected {
			if err = s.owner.config.ValidateWrite(ctx, s.principal, record); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := collector.Execute(ctx, object.Record)
	return err
}
func (s *ormScoped) CollectDeletion(ctx context.Context, object Object) (Deletion, error) {
	if s.owner.config.CollectDeletion != nil {
		return s.owner.config.CollectDeletion(ctx, s.principal, object.Record)
	}
	plan, err := s.collector().Collect(ctx, object.Record)
	if err != nil {
		return Deletion{}, err
	}
	return s.deletionFromPlan(plan)
}

func (s *ormScoped) collector() orm.DeleteCollector {
	return orm.DeleteCollector{Store: s.owner.config.Store, MaxObjects: s.owner.config.DeletionMaxObjects, MaxWork: s.owner.config.DeletionMaxWork, Scope: func(ctx context.Context, schema models.Schema) (db.Predicate, error) {
		scope, err := s.owner.config.QueryScope(ctx, s.principal, schema)
		if err != nil {
			return db.Predicate{}, err
		}
		if scope.Identity == "" {
			return db.Predicate{}, auth.ErrPermissionDenied
		}
		return scope.Predicate, nil
	}}
}

func (s *ormScoped) deletionFromPlan(plan orm.DeletionPlan) (Deletion, error) {
	graph := Deletion{}
	endpoints := map[string]Object{}
	for _, record := range plan.Objects {
		object, err := objectFromRecord(record)
		if err != nil {
			return Deletion{}, err
		}
		graph.Objects = append(graph.Objects, object)
		endpoints[record.Schema().Key()+":"+object.ID] = object
	}
	for _, removal := range plan.JoinRemovals {
		registry := s.owner.config.Store.Registry
		if removal.Record == nil || removal.Endpoint == nil || registry == nil || !registry.IsAutomatic(removal.Record.Schema().Key()) {
			return Deletion{}, errors.New("admin: invalid automatic intermediary provenance")
		}
		schema, ok := registry.Get(removal.Record.Schema().Key())
		if !ok {
			return Deletion{}, errors.New("admin: unknown automatic intermediary")
		}
		field, ok := schema.Field(removal.Field)
		if !ok || field.Relation == nil || field.Relation.Target != removal.Endpoint.Schema().Key() || len(field.Relation.TargetFields) != 1 {
			return Deletion{}, errors.New("admin: invalid automatic intermediary endpoint")
		}
		object, err := objectFromRecord(removal.Endpoint)
		if err != nil {
			return Deletion{}, err
		}
		endpoint, ok := endpoints[removal.Endpoint.Schema().Key()+":"+object.ID]
		if !ok {
			return Deletion{}, auth.ErrPermissionDenied
		}
		joinValue, err := removal.Record.Get(field.Name)
		if err != nil {
			return Deletion{}, err
		}
		endpointValue, err := endpoint.Record.Get(field.Relation.TargetFields[0])
		if err != nil {
			return Deletion{}, err
		}
		left, err := json.Marshal(joinValue)
		if err != nil {
			return Deletion{}, err
		}
		right, err := json.Marshal(endpointValue)
		if err != nil || string(left) != string(right) {
			return Deletion{}, auth.ErrPermissionDenied
		}
		graph.JoinRemovals = append(graph.JoinRemovals, JoinRemoval{Endpoint: endpoint})
	}
	for _, update := range plan.Updates {
		object, err := objectFromRecord(update.Record)
		if err != nil {
			return Deletion{}, err
		}
		graph.Updates = append(graph.Updates, RelatedUpdate{Object: object, Field: update.Field, Value: update.Value})
	}
	for range plan.Protected {
		graph.Protected = append(graph.Protected, "Related data prevents deletion.")
	}
	return graph, nil
}
func (s *ormScoped) Atomic(ctx context.Context, fn func(context.Context) error) error {
	return db.Atomic(ctx, s.owner.config.Store.Backend, db.AtomicOptions{}, fn)
}

func objectFromRecord(record models.Record) (Object, error) {
	values := []any{}
	for _, field := range record.Schema().PKFields() {
		value, err := record.Get(field.Name)
		if err != nil {
			return Object{}, err
		}
		values = append(values, value)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return Object{}, err
	}
	key := base64.RawURLEncoding.EncodeToString(encoded)
	snapshot := map[string]any{}
	for _, field := range record.Schema().Fields {
		if !field.IsStored() {
			continue
		}
		value, err := record.Get(field.Name)
		if err != nil {
			return Object{}, err
		}
		snapshot[field.Name] = value
	}
	content, err := json.Marshal(snapshot)
	if err != nil {
		return Object{}, err
	}
	digest := sha256.Sum256(content)
	return Object{Record: record, ID: key, Version: hex.EncodeToString(digest[:]), Label: record.Schema().Name + " " + fmt.Sprint(values)}, nil
}
func decodeKey(schema models.Schema, key string) (db.Predicate, error) {
	if len(key) > 4096 {
		return db.Predicate{}, ErrNotFound
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return db.Predicate{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var values []any
	if err = decoder.Decode(&values); err != nil || !json.Valid(raw) || len(values) != len(schema.PKFields()) {
		return db.Predicate{}, ErrNotFound
	}
	predicates := []db.Predicate{}
	for i, field := range schema.PKFields() {
		value, err := field.Clean(context.Background(), values[i])
		if err != nil {
			return db.Predicate{}, ErrNotFound
		}
		predicates = append(predicates, orm.Q(field.Name, value))
	}
	return orm.And(predicates...), nil
}

// LogSchema is migrated explicitly when the optional Admin app is installed.
func LogSchema() models.Schema { return (&logRecord{}).Schema() }
func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_admin", Name: "0001_log", Operations: []migrations.Operation{migrations.CreateModel(LogSchema())}}}
}

type logRecord struct {
	models.Base
	ID                                                                       string
	Scope, ActorLabel, Site, Model, ObjectID, ObjectLabel, Action, RequestID string
	ActorID                                                                  *string
	ObjectKey, Changes                                                       json.RawMessage
	OccurredAt                                                               time.Time
}

func (*logRecord) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_admin", Name: "LogEntry", Table: "gogo_admin_log", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary),
		models.CharField("scope", models.WithStructField("Scope"), models.WithMaxLength(512)),
		models.CharField("actor_id", models.WithStructField("ActorID"), models.WithMaxLength(512), models.Nullable),
		models.TextField("actor_label", models.WithStructField("ActorLabel")),
		models.CharField("site", models.WithStructField("Site"), models.WithMaxLength(128)),
		models.CharField("model", models.WithStructField("Model"), models.WithMaxLength(256)),
		models.TextField("object_id", models.WithStructField("ObjectID")),
		models.JSONField("object_key", models.WithStructField("ObjectKey")),
		models.TextField("object_label", models.WithStructField("ObjectLabel")),
		models.CharField("action", models.WithStructField("Action"), models.WithMaxLength(128)),
		models.JSONField("changed_fields", models.WithStructField("Changes")),
		models.CharField("request_id", models.WithStructField("RequestID"), models.WithMaxLength(128)),
		models.DateTimeField("occurred_at", models.WithStructField("OccurredAt")),
	}, Indexes: []models.Index{{Name: "gogo_admin_log_scope_object", Fields: []string{"scope", "site", "model", "object_id"}}}}
}
func (s *ormScoped) Audit(ctx context.Context, entry LogEntry) error {
	if !db.InTransaction(ctx, s.owner.config.Store.Backend.Alias()) {
		return errors.New("admin: audit must share the mutation transaction")
	}
	if entry.Site != s.site || entry.Model != s.schema.Key() || entry.ActorID != s.principal.ID {
		return auth.ErrPermissionDenied
	}
	changes, err := json.Marshal(entry.Changes)
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	random[6] = (random[6] & 15) | 64
	random[8] = (random[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", random[0:4], random[4:6], random[6:8], random[8:10], random[10:16])
	objectKey, err := base64.RawURLEncoding.DecodeString(entry.ObjectID)
	if err != nil || !json.Valid(objectKey) {
		return errors.New("admin: invalid audit object key")
	}
	actorLabel := entry.ActorLabel
	if actorLabel == "" {
		actorLabel = entry.ActorID
	}
	record := &logRecord{ID: id, Scope: s.scope.Identity, ActorID: &entry.ActorID, ActorLabel: actorLabel, Site: entry.Site, Model: entry.Model, ObjectID: entry.ObjectID, ObjectKey: objectKey, ObjectLabel: entry.ObjectLabel, Action: entry.Action, Changes: changes, RequestID: entry.RequestID, OccurredAt: entry.At}
	return s.owner.config.Store.Save(ctx, record, orm.SaveOptions{ForceInsert: true})
}
func (s *ormScoped) History(ctx context.Context, key string, offset, limit int) ([]LogEntry, error) {
	if offset < 0 || limit < 1 || limit > 1000 {
		return nil, errors.New("admin: invalid history bounds")
	}
	rows, err := orm.For(s.owner.config.Store, func() *logRecord { return &logRecord{} }).Filter(orm.Q("scope", s.scope.Identity), orm.Q("site", s.site), orm.Q("model", s.schema.Key()), orm.Q("object_id", key)).OrderBy("-occurred_at", "-id").Offset(offset).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	result := []LogEntry{}
	for _, row := range rows {
		actorID := ""
		if row.ActorID != nil {
			actorID = *row.ActorID
		}
		entry := LogEntry{ActorID: actorID, ActorLabel: row.ActorLabel, RequestID: row.RequestID, Site: row.Site, Model: row.Model, ObjectID: row.ObjectID, ObjectLabel: row.ObjectLabel, Action: row.Action, At: row.OccurredAt}
		if err = json.Unmarshal(row.Changes, &entry.Changes); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}
