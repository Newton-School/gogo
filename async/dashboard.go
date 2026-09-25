package async

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DashboardConfig mounts a read-only, server-rendered operator dashboard. Both
// Authorize (HTTP access) and ClientConfig.Authorize (object access) are required.
// Inventory requires inspect_dashboard with an empty scope and the page kind as
// ID; metadata requires inspect with the object's scope/ID. Events retain their
// inspect_events grants. This is an operator inventory, not a tenant portal.
type DashboardConfig struct {
	Client      *Client
	Catalog     DashboardCatalog
	Authorize   func(*http.Request) error
	BasePath    string
	Title       string
	Events      EventReader
	EventScopes []string
	PageSize    int
	Timeout     time.Duration
}

//go:embed dashboard_assets/page.html dashboard_assets/style.css
var dashboardAssets embed.FS

type Dashboard struct {
	config   DashboardConfig
	template *template.Template
	style    []byte
}
type dashboardNav struct {
	Name, Path string
	Active     bool
}
type dashboardCell struct{ Text, Link, Badge string }
type dashboardRow struct{ Cells []dashboardCell }
type dashboardFact struct{ Name, Value, Link string }
type dashboardPage struct {
	Brand, Base, Kind, Title, Description, Observed, Refresh, Next, Error, Query, State, Queue, Scope string
	Nav                                                                                               []dashboardNav
	Columns                                                                                           []string
	Rows                                                                                              []dashboardRow
	Facts                                                                                             []dashboardFact
	Metrics                                                                                           []dashboardFact
	States, Queues, Scopes                                                                            []string
	Detail, Filter, EventFilter                                                                       bool
}

// NewDashboard does not open a listener or start workers. Mount the returned
// handler after session/identity middleware at BasePath (default /async/).
// Providers and authorization callbacks must honor the request deadline.
func NewDashboard(config DashboardConfig) (*Dashboard, error) {
	if config.Client == nil || config.Client.config.Authorize == nil || config.Catalog == nil || config.Authorize == nil {
		return nil, ErrInvalid
	}
	if config.BasePath == "" {
		config.BasePath = "/async/"
	}
	if config.BasePath != "/" {
		if !strings.HasPrefix(config.BasePath, "/") || !strings.HasSuffix(config.BasePath, "/") || strings.Contains(config.BasePath, "//") {
			return nil, ErrInvalid
		}
		for _, part := range strings.Split(strings.Trim(config.BasePath, "/"), "/") {
			if !namePattern.MatchString(part) {
				return nil, ErrInvalid
			}
		}
	}
	if config.Title == "" {
		config.Title = "Gogo Async"
	}
	if len(config.Title) > 80 || !validEventScope(config.Title) {
		return nil, ErrInvalid
	}
	if config.PageSize == 0 {
		config.PageSize = 25
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}
	if config.PageSize < 1 || config.PageSize > 100 || config.Timeout < time.Millisecond || config.Timeout > 30*time.Second {
		return nil, ErrInvalid
	}
	if len(config.Client.config.Queues) > 64 {
		return nil, ErrInvalid
	}
	if len(config.EventScopes) == 0 {
		config.EventScopes = []string{""}
	}
	if len(config.EventScopes) > 64 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, scope := range config.EventScopes {
		if !validEventScope(scope) || seen[scope] {
			return nil, ErrInvalid
		}
		seen[scope] = true
	}
	config.EventScopes = slices.Clone(config.EventScopes)
	client := *config.Client
	config.Client = &client
	t, err := template.ParseFS(dashboardAssets, "dashboard_assets/page.html")
	if err != nil {
		return nil, err
	}
	style, err := dashboardAssets.ReadFile("dashboard_assets/style.css")
	if err != nil {
		return nil, err
	}
	return &Dashboard{config: config, template: t, style: style}, nil
}

func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	ctx, cancel := context.WithTimeout(r.Context(), d.config.Timeout)
	defer cancel()
	r = r.WithContext(ctx)
	// Build the complete page before sending bytes. Panics, late cancellation,
	// malformed providers and permission outages never render partial records.
	page := d.page(r)
	status, asset := http.StatusOK, false
	func() {
		defer func() {
			if recover() != nil {
				status = http.StatusServiceUnavailable
			}
		}()
		if err := d.config.Authorize(r); err != nil {
			if errors.Is(err, ErrDenied) {
				status = 403
			} else {
				status = 503
			}
			return
		}
		if err := ctx.Err(); err != nil {
			status = 503
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			status = 405
			w.Header().Set("Allow", "GET, HEAD")
			return
		}
		if r.URL.Path == strings.TrimSuffix(d.config.BasePath, "/") && d.config.BasePath != "/" {
			status = 308
			return
		}
		if !strings.HasPrefix(r.URL.Path, d.config.BasePath) {
			status = 404
			return
		}
		path := strings.TrimPrefix(r.URL.Path, d.config.BasePath)
		if path == "style.css" {
			asset = true
			return
		}
		if len(r.URL.RawQuery) > 2048 {
			status = 400
			return
		}
		if err := d.load(r, &page, path); err != nil {
			switch {
			case errors.Is(err, ErrDenied):
				status = 403
			case errors.Is(err, ErrNotFound), errors.Is(err, ErrResultExpired):
				status = 404
			case errors.Is(err, ErrInvalid):
				status = 400
			default:
				status = 503
			}
		}
		if ctx.Err() != nil {
			status = 503
		}
	}()
	if status == 308 {
		w.Header().Set("Location", d.config.BasePath)
		w.WriteHeader(status)
		return
	}
	if status != 200 {
		page = d.page(r)
		page.Title = http.StatusText(status)
		switch status {
		case 403:
			page.Error = "You do not have permission to view this page."
		case 404:
			page.Error = "This page or record is not available. It may have expired or may not have been dispatched yet."
		case 400:
			page.Error = "The address or filter is invalid. Return to the overview and try again."
		case 405:
			page.Error = "This dashboard is read-only. Use links and search to inspect records."
		default:
			page.Error = "Monitoring data is unavailable. Check the backend or access policy, then refresh. No partial data is shown."
		}
	}
	if asset && status == 200 {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(d.style)
		}
		return
	}
	var out bytes.Buffer
	if err := d.template.ExecuteTemplate(&out, "page.html", page); err != nil {
		http.Error(w, "Dashboard unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(out.Bytes())
	}
}

func (d *Dashboard) page(r *http.Request) dashboardPage {
	p := dashboardPage{Brand: d.config.Title, Base: d.config.BasePath, Title: "Overview", Observed: time.Now().UTC().Format("02 Jan 2006 · 15:04:05 UTC"), Refresh: d.config.BasePath}
	for _, item := range [][2]string{{"Overview", ""}, {"Tasks", "tasks"}, {"Workers", "workers"}, {"Queues", "queues"}, {"Beat schedules", "beat"}, {"Beat instances", "schedulers"}, {"Workflows", "workflows"}, {"Events", "events"}} {
		p.Nav = append(p.Nav, dashboardNav{Name: item[0], Path: d.config.BasePath + item[1], Active: r.URL.Path == d.config.BasePath+item[1] || item[1] != "" && strings.HasPrefix(r.URL.Path, d.config.BasePath+item[1]+"/")})
	}
	return p
}

func (d *Dashboard) grant(ctx context.Context, kind string) error {
	err := d.config.Client.config.Authorize(ctx, "inspect_dashboard", "", kind)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	return ErrUnavailable
}

func (d *Dashboard) load(r *http.Request, p *dashboardPage, path string) error {
	parts := strings.Split(path, "/")
	if len(parts) > 2 || len(parts) == 2 && parts[1] == "" {
		return ErrNotFound
	}
	kind := parts[0]
	if kind == "" {
		kind = "overview"
	}
	if !dashboardKind(kind) && kind != "overview" && kind != "queues" && kind != "events" {
		return ErrNotFound
	}
	if err := d.grant(r.Context(), kind); err != nil {
		return err
	}
	p.Kind = kind
	p.Refresh = d.config.BasePath + path
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 || !slices.Contains([]string{"q", "state", "queue", "scope", "cursor"}, key) {
			return ErrInvalid
		}
	}
	p.Query = query.Get("q")
	p.State = query.Get("state")
	p.Queue = query.Get("queue")
	p.Scope = query.Get("scope")
	if !query.Has("scope") {
		p.Scope = d.config.EventScopes[0]
	}
	if len(p.Query) > 192 || !validEventScope(p.Query) || !validInventoryCursor(query.Get("cursor")) {
		return ErrInvalid
	}
	p.States = []string{"SCHEDULED", "QUEUED", "RUNNING", "RETRY_WAIT", "SUCCEEDED", "FAILED", "REVOKED", "EXPIRED"}
	p.Queues = slices.Clone(d.config.Client.config.Queues)
	p.Scopes = d.config.EventScopes
	if p.State != "" && !slices.Contains(p.States, p.State) || p.Queue != "" && !slices.Contains(p.Queues, p.Queue) {
		return ErrInvalid
	}
	if r.URL.RawQuery != "" {
		p.Refresh += "?" + query.Encode()
	}
	if len(parts) == 2 {
		p.Detail = true
		return d.detail(r.Context(), p, kind, parts[1])
	}
	if kind == "overview" || kind == "queues" {
		return d.queues(r.Context(), p, kind)
	}
	if kind == "events" {
		return d.events(r.Context(), p, query)
	}
	p.Filter = true
	switch kind {
	case "tasks":
		p.Title = "Tasks"
		p.Description = "Retained task records. Filter by name or ID, state, and queue. Inventory order is not chronological."
		p.Columns = []string{"Task", "State", "Queue", "Created", "Retries"}
	case "workers":
		p.Title = "Workers"
		p.Description = "Worker heartbeats. Lost means the heartbeat expired—not proof that the process stopped. Records are retained for 24 hours."
		p.Columns = []string{"Worker", "Health", "Queues", "Concurrency", "Observed"}
	case "beat":
		p.Title = "Beat schedules"
		p.Description = "Periodic schedules and dispatch intent. A committed occurrence is not proof of task execution."
		p.Columns = []string{"Task / Schedule", "Enabled", "Rule", "Next due", "Last committed task"}
	case "schedulers":
		p.Title = "Beat instances"
		p.Description = "Best-effort observations after each tick. A heartbeat is not a leadership lease. Records are retained for 24 hours."
		p.Columns = []string{"Instance", "Health", "Observed", "Expires"}
	case "workflows":
		p.Title = "Workflows"
		p.Description = "Retained groups, chains, chords, and composed workflows. Open a record to inspect its visible tasks."
		p.Columns = []string{"Workflow", "Kind", "State"}
	}
	// Exact identity lookups also cover records predating discovery indexes.
	if idPattern.MatchString(p.Query) || (kind == "workers" || kind == "schedulers") && p.Query != "" {
		if err := d.appendRecord(r.Context(), p, kind, p.Query, false); err != nil {
			return err
		}
		return nil
	}
	cursor := query.Get("cursor")
	seen := map[string]bool{}
	reads := 0
	for calls := 0; calls < 128; calls++ {
		limit := min(d.config.PageSize-len(p.Rows), 200-reads)
		if limit < 1 {
			break
		}
		batch, err := d.config.Catalog.ListDashboard(r.Context(), kind, cursor, limit)
		if err != nil {
			return err
		}
		if batch.Validate(kind, limit) != nil || batch.NextCursor != "" && (batch.NextCursor == cursor || seen[batch.NextCursor]) {
			return ErrUnavailable
		}
		for _, id := range batch.IDs {
			reads++
			if err := d.appendRecord(r.Context(), p, kind, id, false); err != nil && !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrResultExpired) {
				return err
			}
		}
		cursor = batch.NextCursor
		if cursor == "" {
			break
		}
		seen[cursor] = true
		if len(p.Rows) >= d.config.PageSize || reads >= 200 {
			break
		}
	}
	if cursor != "" {
		query.Set("cursor", cursor)
		p.Next = d.config.BasePath + kind + "?" + query.Encode()
	}
	return nil
}

func (d *Dashboard) detail(ctx context.Context, p *dashboardPage, kind, id string) error {
	if !dashboardKind(kind) {
		return ErrNotFound
	}
	return d.appendRecord(ctx, p, kind, id, true)
}
