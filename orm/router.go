package orm

import "github.com/Newton-School/gogo/models"

// OpinionRouter can make optional routing decisions.
type OpinionRouter interface {
	DBForReadOpinion(models.Metadata) (string, bool)
	DBForWriteOpinion(models.Metadata) (string, bool)
	AllowRelationOpinion(models.Metadata, models.Metadata) (bool, bool)
	AllowMigrateOpinion(db, appLabel, modelName string) (bool, bool)
}

// DefaultRouter provides Django-style default routing behavior.
type DefaultRouter struct{}

// DBForRead returns the default read database.
func (DefaultRouter) DBForRead(models.Metadata) string {
	return DefaultDatabase
}

// DBForWrite returns the default write database.
func (DefaultRouter) DBForWrite(models.Metadata) string {
	return DefaultDatabase
}

// AllowRelation allows relations by default.
func (DefaultRouter) AllowRelation(models.Metadata, models.Metadata) bool {
	return true
}

// AllowMigrate allows migrations by default.
func (DefaultRouter) AllowMigrate(db, appLabel, modelName string) bool {
	return true
}

// FuncRouter is a convenient functional router implementation.
type FuncRouter struct {
	Read     func(models.Metadata) (string, bool)
	Write    func(models.Metadata) (string, bool)
	Relation func(models.Metadata, models.Metadata) (bool, bool)
	Migrate  func(db, appLabel, modelName string) (bool, bool)
}

// DBForReadOpinion returns a custom read database decision.
func (r FuncRouter) DBForReadOpinion(meta models.Metadata) (string, bool) {
	if r.Read == nil {
		return "", false
	}
	return r.Read(meta)
}

// DBForWriteOpinion returns a custom write database decision.
func (r FuncRouter) DBForWriteOpinion(meta models.Metadata) (string, bool) {
	if r.Write == nil {
		return "", false
	}
	return r.Write(meta)
}

// AllowRelationOpinion returns a custom relation decision.
func (r FuncRouter) AllowRelationOpinion(a, b models.Metadata) (bool, bool) {
	if r.Relation == nil {
		return false, false
	}
	return r.Relation(a, b)
}

// AllowMigrateOpinion returns a custom migration decision.
func (r FuncRouter) AllowMigrateOpinion(db, appLabel, modelName string) (bool, bool) {
	if r.Migrate == nil {
		return false, false
	}
	return r.Migrate(db, appLabel, modelName)
}

// RouterSet applies routers in order and falls back to DefaultRouter.
type RouterSet struct {
	routers       []OpinionRouter
	defaultRouter DefaultRouter
}

// NewRouterSet creates a router chain.
func NewRouterSet(routers ...OpinionRouter) *RouterSet {
	return &RouterSet{routers: append([]OpinionRouter(nil), routers...)}
}

// DBForRead returns the first router read opinion or the default database.
func (r *RouterSet) DBForRead(meta models.Metadata) string {
	for _, router := range r.routers {
		if db, ok := router.DBForReadOpinion(meta); ok {
			return db
		}
	}
	return r.defaultRouter.DBForRead(meta)
}

// DBForWrite returns the first router write opinion or the default database.
func (r *RouterSet) DBForWrite(meta models.Metadata) string {
	for _, router := range r.routers {
		if db, ok := router.DBForWriteOpinion(meta); ok {
			return db
		}
	}
	return r.defaultRouter.DBForWrite(meta)
}

// AllowRelation returns the first router relation opinion or allows by default.
func (r *RouterSet) AllowRelation(a, b models.Metadata) bool {
	for _, router := range r.routers {
		if allowed, ok := router.AllowRelationOpinion(a, b); ok {
			return allowed
		}
	}
	return r.defaultRouter.AllowRelation(a, b)
}

// AllowMigrate returns the first router migration opinion or allows by default.
func (r *RouterSet) AllowMigrate(db, appLabel, modelName string) bool {
	for _, router := range r.routers {
		if allowed, ok := router.AllowMigrateOpinion(db, appLabel, modelName); ok {
			return allowed
		}
	}
	return r.defaultRouter.AllowMigrate(db, appLabel, modelName)
}
