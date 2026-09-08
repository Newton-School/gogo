package db

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/Newton-School/gogo/core/models"
)

// RouteOperation describes an ORM operation, not permission to execute raw SQL.
type RouteOperation string

const (
	RouteRead        RouteOperation = "read"
	RouteWrite       RouteOperation = "write"
	RouteTransaction RouteOperation = "transaction"
)

var ErrRouting = errors.New("db: database routing refused")

// DatabaseRoutingError contains only validated routing metadata. It never
// retains a policy error, SQL, parameter values or connection configuration.
type DatabaseRoutingError struct {
	Model, Alias string
	Operation    RouteOperation
	reason       string
}

func (*DatabaseRoutingError) Error() string                { return ErrRouting.Error() }
func (*DatabaseRoutingError) Unwrap() error                { return ErrRouting }
func (e *DatabaseRoutingError) GoString() string           { return e.Error() }
func (e *DatabaseRoutingError) Format(s fmt.State, _ rune) { _, _ = fmt.Fprint(s, e.Error()) }

// RouteRequest contains no record values or mutable schema descriptors. Alias
// is an explicit selection; it bypasses Select, never Allow. PrimaryRequired
// describes an admission constraint, not a replica freshness measurement.
type RouteRequest struct {
	Model, Alias    string
	Operation       RouteOperation
	PrimaryRequired bool
}

// RoutingRule is trusted cooperative configuration. Select may abstain with an
// empty alias. The first nonempty selection wins; every Allow still runs. These
// checks govern database placement, not row/object authorization or query scope.
type RoutingRule interface {
	Select(context.Context, RouteRequest) (string, error)
	Allow(context.Context, RouteRequest, string) error
}

// RoutedDatabase binds one explicit backend handle. Empty Primary means this
// alias is a primary; a replica names a registered primary directly. This is an
// application topology assertion, not automatic replication discovery.
type RoutedDatabase struct {
	Alias, Primary string
	Backend        Backend
}
type RouterConfig struct {
	Databases      []RoutedDatabase
	Default        string
	EnableReplicas bool
	Rules          []RoutingRule
}

// Router owns immutable routing configuration, never pools or connections.
// Its zero value refuses operations. Do not mutate backend/rule internals
// concurrently; replacing the original Router handle cannot retarget a binding.
type Router struct{ state *routerState }

// Configured reports whether this handle was created by NewRouter. It performs
// no callback and grants no permission; operations still resolve independently.
func (r *Router) Configured() bool { return r != nil && r.state != nil }

type routerSource struct {
	alias, primary string
	backend        Backend
}
type routerState struct {
	sources      map[string]*routerSource
	defaultAlias string
	replicas     bool
	rules        []RoutingRule
}

func routingName(value string) bool {
	return len(value) > 0 && len(value) <= 128 && models.ValidIdentifier(value)
}
func routingModel(value string) bool {
	app, model, ok := strings.Cut(value, ".")
	return ok && routingName(app) && routingName(model)
}
func routingNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
func routingFailure(request RouteRequest, alias, reason string) error {
	e := &DatabaseRoutingError{reason: reason}
	switch request.Operation {
	case RouteRead, RouteWrite, RouteTransaction:
		e.Operation = request.Operation
	}
	if routingModel(request.Model) {
		e.Model = request.Model
	}
	if routingName(alias) {
		e.Alias = alias
	}
	return e
}
func routingContext(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = routingFailure(RouteRequest{}, "", "context")
		}
	}()
	if routingNil(ctx) {
		return routingFailure(RouteRequest{}, "", "context")
	}
	switch e := ctx.Err(); e {
	case nil, context.Canceled, context.DeadlineExceeded:
		return e
	default:
		return routingFailure(RouteRequest{}, "", "context")
	}
}

// NewRouter performs no SQL, pool acquisition, Ping or policy callback. Backend
// aliases are checked only after the complete bounded declaration is detached.
func NewRouter(config RouterConfig) (router *Router, err error) {
	defer func() {
		if recover() != nil {
			router, err = nil, routingFailure(RouteRequest{}, "", "configuration")
		}
	}()
	if len(config.Databases) < 1 || len(config.Databases) > 64 || len(config.Rules) > 16 || !routingName(config.Default) {
		return nil, routingFailure(RouteRequest{}, "", "configuration")
	}
	databases, rules := slices.Clone(config.Databases), slices.Clone(config.Rules)
	state := &routerState{sources: map[string]*routerSource{}, defaultAlias: config.Default, replicas: config.EnableReplicas, rules: rules}
	for _, rule := range rules {
		if routingNil(rule) {
			return nil, routingFailure(RouteRequest{}, "", "configuration")
		}
	}
	for _, item := range databases {
		if !routingName(item.Alias) || state.sources[item.Alias] != nil || routingNil(item.Backend) || !reflect.TypeOf(item.Backend).Comparable() {
			return nil, routingFailure(RouteRequest{}, "", "configuration")
		}
		primary := item.Primary
		if primary == "" {
			primary = item.Alias
		}
		if !routingName(primary) {
			return nil, routingFailure(RouteRequest{}, "", "configuration")
		}
		state.sources[item.Alias] = &routerSource{alias: item.Alias, primary: primary, backend: item.Backend}
	}
	for _, source := range state.sources {
		primary := state.sources[source.primary]
		if primary == nil || primary.primary != primary.alias {
			return nil, routingFailure(RouteRequest{}, source.alias, "topology")
		}
	}
	if primary := state.sources[state.defaultAlias]; primary == nil || primary.primary != primary.alias {
		return nil, routingFailure(RouteRequest{}, "", "default")
	}
	// Preserve declaration order for provider metadata callbacks.
	for _, item := range databases {
		if item.Backend.Alias() != item.Alias {
			return nil, routingFailure(RouteRequest{}, item.Alias, "alias")
		}
	}
	return &Router{state: state}, nil
}

// RoutingBackend is an intentionally non-dispatching handle for an unbound
// routed ORM Store. No method chooses a default database or owns pool cleanup.
func (r *Router) RoutingBackend() Backend { return unboundRoutingBackend{} }

// CheckBoundBackend rejects only the sealed unbound routing facade. It does not
// approve an arbitrary backend, run a policy, or establish transaction ownership.
func CheckBoundBackend(backend Backend) error {
	if _, unbound := backend.(unboundRoutingBackend); unbound {
		return routingFailure(RouteRequest{}, "", "unbound")
	}
	return nil
}

type unboundRoutingBackend struct{}

func (unboundRoutingBackend) Alias() string              { return "" }
func (unboundRoutingBackend) Dialect() Dialect           { return unboundRoutingDialect{} }
func (unboundRoutingBackend) Capabilities() Capabilities { return Capabilities{} }
func (unboundRoutingBackend) Exec(context.Context, string, ...any) (Result, error) {
	return nil, routingFailure(RouteRequest{}, "", "unbound")
}
func (unboundRoutingBackend) Query(context.Context, string, ...any) (Rows, error) {
	return nil, routingFailure(RouteRequest{}, "", "unbound")
}
func (unboundRoutingBackend) BeginTx(context.Context, TxOptions) (Transaction, error) {
	return nil, routingFailure(RouteRequest{}, "", "unbound")
}
func (unboundRoutingBackend) Acquire(context.Context) (Connection, error) {
	return nil, routingFailure(RouteRequest{}, "", "unbound")
}
func (unboundRoutingBackend) Ping(context.Context) error {
	return routingFailure(RouteRequest{}, "", "unbound")
}
func (unboundRoutingBackend) Close() error { return routingFailure(RouteRequest{}, "", "unbound") }

type unboundRoutingDialect struct{}

func (unboundRoutingDialect) Name() string { return "unbound-routing" }
func (unboundRoutingDialect) QuoteIdentifier(string) (string, error) {
	return "", routingFailure(RouteRequest{}, "", "unbound")
}
func (unboundRoutingDialect) Placeholder(int) string { return "" }
func (unboundRoutingDialect) FieldType(models.Field) (string, error) {
	return "", routingFailure(RouteRequest{}, "", "unbound")
}

// RouteBinding is one authorized, immutable operation selection. It does not
// acquire a connection. Backend is for the ORM's compiled operation; possession
// of this raw SQL capability is privileged, not object/row authorization.
type RouteBinding struct {
	backend *routeBackend
	context context.Context
}

// Context returns Resolve's frozen private transaction/consistency observations,
// or nil for an invalid binding. Use it for the remainder of that operation.
// Cancellation remains live and application values remain trusted, opaque and
// cooperative. It does not extend an Atomic witness's lifetime or make sharing
// a transaction between goroutines safe.
func (b *RouteBinding) Context() context.Context {
	if b == nil || b.backend == nil {
		return nil
	}
	return b.context
}

func (b *RouteBinding) Alias() string {
	if b == nil || b.backend == nil {
		return ""
	}
	return b.backend.source.alias
}
func (b *RouteBinding) Backend() Backend {
	if b == nil || b.backend == nil {
		return unboundRoutingBackend{}
	}
	return b.backend
}

// Resolve selects once, checks every routing rule, and snapshots the selected
// dialect/capabilities before returning. A failed selection never probes or
// falls back to another backend. Contexts without WithConsistency use primaries.
func (r *Router) Resolve(ctx context.Context, request RouteRequest) (binding *RouteBinding, err error) {
	defer func() {
		if recover() != nil {
			binding, err = nil, routingFailure(request, "", "callback")
		}
	}()
	if r == nil || r.state == nil {
		return nil, routingFailure(request, "", "configuration")
	}
	state := r.state
	if request.Operation != RouteRead && request.Operation != RouteWrite && request.Operation != RouteTransaction || request.Operation != RouteTransaction && !routingModel(request.Model) || request.Operation == RouteTransaction && request.Model != "" || request.Alias != "" && !routingName(request.Alias) {
		return nil, routingFailure(request, "", "request")
	}
	ctx, err = state.snapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	consistency := state.consistency(ctx)
	witness, err := state.ambient(ctx)
	if err != nil {
		return nil, err
	}
	request.PrimaryRequired = request.PrimaryRequired || request.Operation != RouteRead || consistency == nil || witness != nil
	alias := request.Alias
	if alias == "" && witness != nil {
		alias = witness.source.alias
	}
	if alias == "" {
		for _, rule := range state.rules {
			selected, e := rule.Select(ctx, request)
			if e != nil {
				return nil, routingFailure(request, "", "selection")
			}
			if e := routingContext(ctx); e != nil {
				return nil, e
			}
			if selected != "" {
				alias = selected
				break
			}
		}
	}
	if alias == "" {
		alias = state.defaultAlias
	}
	if !routingName(alias) {
		return nil, routingFailure(request, "", "selection")
	}
	source := state.sources[alias]
	if source == nil {
		return nil, routingFailure(request, "", "selection")
	}
	if consistency != nil && consistency.primaryRequired(source.primary) {
		request.PrimaryRequired = true
	}
	if source.alias != source.primary {
		if !state.replicas {
			return nil, routingFailure(request, source.alias, "replica disabled")
		}
		if request.PrimaryRequired {
			if request.Alias != "" {
				return nil, routingFailure(request, source.alias, "primary required")
			}
			source = state.sources[source.primary]
		}
	}
	if witness != nil && witness.source != source {
		return nil, routingFailure(request, source.alias, "transaction mismatch")
	}
	for _, rule := range state.rules {
		if e := rule.Allow(ctx, request, source.alias); e != nil {
			return nil, routingFailure(request, source.alias, "denied")
		}
		if e := routingContext(ctx); e != nil {
			return nil, e
		}
	}
	selected := &routeBackend{state: state, source: source, request: request, consistency: consistency}
	if e := selected.admit(ctx, false); e != nil {
		return nil, e
	}
	selected.dialect = source.backend.Dialect()
	if routingNil(selected.dialect) {
		return nil, routingFailure(request, source.alias, "dialect")
	}
	capabilities := source.backend.Capabilities()
	if len(capabilities) > 256 {
		return nil, routingFailure(request, source.alias, "capabilities")
	}
	selected.capabilities = make(Capabilities, len(capabilities))
	for name, value := range capabilities {
		if len(name) > 128 {
			return nil, routingFailure(request, source.alias, "capabilities")
		}
		selected.capabilities[name] = value
	}
	selected.maximum = 500
	if limiter, ok := source.backend.(ParameterLimiter); ok {
		selected.maximum = limiter.MaxParameters()
	}
	if selected.maximum < 1 {
		return nil, routingFailure(request, source.alias, "parameters")
	}
	if e := selected.admit(ctx, false); e != nil {
		return nil, e
	}
	return &RouteBinding{backend: selected, context: ctx}, nil
}

type routeBackend struct {
	state        *routerState
	source       *routerSource
	request      RouteRequest
	consistency  *routingConsistency
	dialect      Dialect
	capabilities Capabilities
	maximum      int
}

func (b *routeBackend) Alias() string    { return b.source.alias }
func (b *routeBackend) Dialect() Dialect { return b.dialect }
func (b *routeBackend) Capabilities() Capabilities {
	copy := make(Capabilities, len(b.capabilities))
	for k, v := range b.capabilities {
		copy[k] = v
	}
	return copy
}
func (b *routeBackend) MaxParameters() int { return b.maximum }
func (b *routeBackend) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	if b.request.Operation != RouteWrite {
		return nil, routingFailure(b.request, b.source.alias, "write binding required")
	}
	operation, witness, e := b.operation(ctx, true)
	if e != nil {
		return nil, e
	}
	if witness != nil {
		return witness.transaction.tx.Exec(operation, query, args...)
	}
	return b.source.backend.Exec(operation, query, args...)
}
func (b *routeBackend) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	operation, witness, e := b.operation(ctx, b.request.Operation == RouteWrite)
	if e != nil {
		return nil, e
	}
	if witness != nil {
		return witness.transaction.tx.Query(operation, query, args...)
	}
	return b.source.backend.Query(operation, query, args...)
}
func (b *routeBackend) BeginTx(context.Context, TxOptions) (Transaction, error) {
	return nil, routingFailure(b.request, b.source.alias, "routed Atomic required")
}
func (b *routeBackend) Acquire(context.Context) (Connection, error) {
	return nil, routingFailure(b.request, b.source.alias, "raw connection unsupported")
}
func (b *routeBackend) Ping(context.Context) error {
	return routingFailure(b.request, b.source.alias, "pool operation unsupported")
}
func (b *routeBackend) Close() error {
	return routingFailure(b.request, b.source.alias, "pool ownership retained")
}

// BeforeWrite pins consistency before hooks/defaults or a first attempted write.
// A refusal/rollback/unknown outcome never clears that conservative pin.
func (b *RouteBinding) BeforeWrite(ctx context.Context) error {
	if b == nil || b.backend == nil || b.backend.request.Operation != RouteWrite {
		return routingFailure(RouteRequest{}, "", "write binding required")
	}
	return b.backend.admit(ctx, true)
}
