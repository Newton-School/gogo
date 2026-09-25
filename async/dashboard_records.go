package async

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func dashboardTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("02 Jan 2006 15:04:05 UTC")
}
func cell(s string) dashboardCell { return dashboardCell{Text: s} }
func badge(s string) dashboardCell {
	c := "neutral"
	switch strings.ToLower(s) {
	case "succeeded", "online", "enabled":
		c = "good"
	case "failed", "error", "lost", "expired":
		c = "bad"
	case "running", "queued", "scheduled", "retry_wait":
		c = "pending"
	}
	return dashboardCell{Text: s, Badge: c}
}
func (d *Dashboard) link(kind, id, label string) dashboardCell {
	return dashboardCell{Text: label, Link: d.config.BasePath + kind + "/" + url.PathEscape(id)}
}
func fact(p *dashboardPage, name, value string) {
	p.Facts = append(p.Facts, dashboardFact{Name: name, Value: value})
}
func (d *Dashboard) relation(ctx context.Context, p *dashboardPage, name, kind, scope, id string) error {
	if id == "" {
		return nil
	}
	if err := (Control{Client: d.config.Client}).authorizeInspection(ctx, scope, id); err != nil {
		if err == ErrDenied {
			return nil
		}
		return err
	}
	p.Facts = append(p.Facts, dashboardFact{Name: name, Value: id, Link: d.config.BasePath + kind + "/" + url.PathEscape(id)})
	return nil
}
func (p *dashboardPage) matches(name, id, state, queue string) bool {
	return (p.Query == "" || strings.Contains(strings.ToLower(name+" "+id), strings.ToLower(p.Query))) && (p.State == "" || p.State == state) && (p.Queue == "" || p.Queue == queue)
}

func (d *Dashboard) appendRecord(ctx context.Context, p *dashboardPage, kind, id string, detail bool) error {
	if kind == "workers" || kind == "schedulers" {
		if !ValidWorkerID(id) {
			return ErrInvalid
		}
	} else if !idPattern.MatchString(id) {
		return ErrInvalid
	}
	control := Control{Client: d.config.Client}
	switch kind {
	case "tasks":
		raw, err := d.config.Client.config.Results.Lookup(ctx, id)
		if err != nil {
			return err
		}
		r, err := resultRecord(raw, id)
		if err != nil {
			return err
		}
		if err := control.authorizeInspection(ctx, r.Envelope.Scope, id); err != nil {
			return err
		}
		e := r.Envelope
		if !detail {
			if p.matches(e.Task, id, string(r.State), e.Queue) {
				p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link(kind, id, e.Task+" · "+id), badge(string(r.State)), cell(e.Queue), cell(dashboardTime(e.CreatedAt)), cell(strconv.Itoa(e.Retries))}})
			}
			return nil
		}
		p.Title = e.Task
		p.Description = "Task metadata only. Arguments, results, progress payloads, and error messages are intentionally not displayed."
		fact(p, "Task ID", id)
		fact(p, "State", string(r.State))
		fact(p, "Queue", e.Queue)
		fact(p, "Created", dashboardTime(e.CreatedAt))
		fact(p, "Scheduled for", dashboardTime(e.ETA))
		fact(p, "Finished", dashboardTime(r.FinishedAt))
		maxRetries := 0
		if e.MaxRetries != nil {
			maxRetries = *e.MaxRetries
		}
		fact(p, "Retries", fmt.Sprintf("%d / %d", e.Retries, maxRetries))
		fact(p, "Deliveries", strconv.FormatInt(r.DeliveryCount, 10))
		fact(p, "Cancellation requested", strconv.FormatBool(r.CancelRequested))
		if !r.FinishedAt.IsZero() {
			fact(p, "Time to completion", r.FinishedAt.Sub(e.CreatedAt).Round(time.Millisecond).String()+" (includes waiting and retries)")
		}
		fact(p, "Execution lease expires", dashboardTime(r.LeaseUntil))
		fact(p, "Task expiry", dashboardTime(e.ExpiresAt))
		fact(p, "Payload expires", dashboardTime(r.PayloadExpiresAt))
		for _, link := range [][3]string{{"Workflow", "workflows", e.WorkflowID}, {"Parent task", "tasks", e.ParentID}, {"Root task", "tasks", e.RootID}, {"Replacement task", "tasks", r.ReplacementID}} {
			if err := d.relation(ctx, p, link[0], link[1], e.Scope, link[2]); err != nil {
				return err
			}
		}
	case "workers":
		rows, err := control.InspectWorkers(ctx, []string{id})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return ErrUnavailable
		}
		r := rows[0]
		s := r.Snapshot
		if !detail {
			if p.matches(id, id, "", "") {
				p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link(kind, id, id), badge(string(r.Status)), cell(strings.Join(s.Queues, ", ")), cell(strconv.Itoa(s.Concurrency)), cell(dashboardTime(r.ObservedAt))}})
			}
			return nil
		}
		p.Title = id
		p.Description = "Latest heartbeat and authorized active tasks. Active work is a snapshot, not a complete execution history."
		fact(p, "Health", string(r.Status))
		fact(p, "Observed", dashboardTime(r.ObservedAt))
		fact(p, "Expires", dashboardTime(r.ExpiresAt))
		fact(p, "Concurrency", strconv.Itoa(s.Concurrency))
		fact(p, "Queues", strings.Join(s.Queues, ", "))
		fact(p, "Registered tasks", strings.Join(s.Registered, ", "))
		p.Columns = []string{"Active task", "Started", "Retries"}
		for _, a := range s.Active {
			p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link("tasks", a.ID, a.Task+" · "+a.ID), cell(dashboardTime(a.StartedAt)), cell(strconv.Itoa(a.Retries))}})
		}
	case "schedulers":
		if err := control.authorizeInspection(ctx, "", id); err != nil {
			return err
		}
		r, err := d.config.Catalog.ReadDashboardBeat(ctx, id)
		if err != nil {
			return err
		}
		if r.ID != id || r.ObservedAt.IsZero() || r.ExpiresAt.IsZero() || !strings.Contains("|online|offline|error|lost|", "|"+r.Status+"|") {
			return ErrUnavailable
		}
		if err := control.authorizeInspection(ctx, "", id); err != nil {
			return err
		}
		if !detail {
			if p.matches(id, id, "", "") {
				p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link(kind, id, id), badge(r.Status), cell(dashboardTime(r.ObservedAt)), cell(dashboardTime(r.ExpiresAt))}})
			}
			return nil
		}
		p.Title = id
		p.Description = "Tick heartbeat only. Inspect schedules for dispatch intent, and task records for execution outcomes."
		fact(p, "Health", r.Status)
		fact(p, "Observed", dashboardTime(r.ObservedAt))
		fact(p, "Expires", dashboardTime(r.ExpiresAt))
	case "beat":
		r, err := d.config.Catalog.ReadDashboardSchedule(ctx, id)
		if err != nil {
			return err
		}
		if r.ID != id || r.Validate() != nil || !namePattern.MatchString(r.Signature.Task) || !validEventScope(r.Signature.Options.Scope) {
			return ErrUnavailable
		}
		if err := control.authorizeInspection(ctx, r.Signature.Options.Scope, id); err != nil {
			return err
		}
		rule := r.Rule.Expression
		if r.Rule.Kind == "interval" {
			rule = "Every " + r.Rule.Interval.String()
		}
		if rule == "" {
			rule = r.Rule.Kind
		}
		enabled := "disabled"
		if r.Enabled {
			enabled = "enabled"
		}
		last := cell("—")
		if r.LastTaskID != "" {
			if !idPattern.MatchString(r.LastTaskID) {
				return ErrUnavailable
			}
			if err := control.authorizeInspection(ctx, r.Signature.Options.Scope, r.LastTaskID); err == nil {
				last = d.link("tasks", r.LastTaskID, r.LastTaskID)
			} else if err != ErrDenied {
				return err
			}
		}
		if !detail {
			if p.matches(r.Signature.Task, id, "", r.Signature.Options.Queue) {
				p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link(kind, id, r.Signature.Task+" · "+id), badge(enabled), cell(rule), cell(dashboardTime(r.NextDue)), last}})
			}
			return nil
		}
		p.Title = r.Signature.Task
		p.Description = "Schedule configuration. Last committed task identifies a dispatch intent; it may not have been published yet."
		fact(p, "Schedule ID", id)
		fact(p, "Status", enabled)
		fact(p, "Rule", rule)
		fact(p, "Timezone", r.Rule.Timezone)
		fact(p, "Fixed delay", strconv.FormatBool(r.Rule.FixedDelay))
		fact(p, "DST fold / gap", r.Rule.FoldPolicy+" / "+r.Rule.GapPolicy)
		fact(p, "Next due", dashboardTime(r.NextDue))
		fact(p, "Ends", dashboardTime(r.EndAt))
		fact(p, "Misfire policy", r.Misfire)
		fact(p, "Catch-up limit", strconv.Itoa(r.CatchUpLimit))
		fact(p, "Overlap", r.Overlap)
		fact(p, "Schedule lease expires", dashboardTime(r.LeaseUntil))
		p.Facts = append(p.Facts, dashboardFact{Name: "Last committed task", Value: last.Text, Link: last.Link})
	case "workflows":
		if d.config.Client.config.Workflows == nil {
			return ErrUnavailable
		}
		raw, err := d.config.Client.config.Workflows.ReadGraph(ctx, id)
		if err != nil {
			return err
		}
		g, err := groupRecord(raw, id)
		if err != nil {
			return err
		}
		if err := control.authorizeInspection(ctx, g.Scope, id); err != nil {
			return err
		}
		if !detail {
			if p.matches(g.Kind, id, string(g.State), "") {
				p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link(kind, id, id), cell(g.Kind), badge(string(g.State))}})
			}
			return nil
		}
		p.Title = "Workflow"
		p.Description = "Authorized task references. Missing records may be awaiting dispatch or may have expired. Internal collector nodes are not task executions."
		fact(p, "Workflow ID", id)
		fact(p, "Kind", g.Kind)
		fact(p, "State", string(g.State))
		fact(p, "Cancellation requested", strconv.FormatBool(g.CancelRequested))
		p.Columns = []string{"Task", "Workflow observation"}
		add := func(taskID, name string) error {
			if err := control.authorizeInspection(ctx, g.Scope, taskID); err != nil {
				if err == ErrDenied {
					return nil
				}
				return err
			}
			state := "Not completed"
			if member, ok := g.Members[taskID]; ok {
				state = string(member.State)
			}
			p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{d.link("tasks", taskID, name+" · "+taskID), badge(state)}})
			return nil
		}
		seen := map[string]bool{}
		for _, e := range g.Children {
			seen[e.ID] = true
			if err := add(e.ID, e.Task); err != nil {
				return err
			}
		}
		for _, node := range g.Plan {
			if node.Signature != nil && !seen[node.ID] {
				if err := add(node.ID, node.Signature.Task); err != nil {
					return err
				}
			}
		}
		if g.CallbackID != "" {
			if err := add(g.CallbackID, "Callback"); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (d *Dashboard) queues(ctx context.Context, p *dashboardPage, kind string) error {
	p.Title = "Queues"
	p.Description = "Broker observations, not task execution totals. Pending deliveries may be waiting for acknowledgement or recovery."
	p.Columns = []string{"Queue", "Queued", "Pending", "Quarantined"}
	if kind == "overview" {
		p.Title = "Overview"
		p.Description = "A quiet place to inspect background work. Read-only monitoring; task execution remains independent of this dashboard."
	}
	queues := d.config.Client.config.Queues
	if len(queues) == 0 {
		return nil
	}
	rows, err := (Control{Client: d.config.Client}).InspectQueues(ctx, queues)
	if err != nil {
		return err
	}
	var queued, pending, quarantined int64
	for _, r := range rows {
		p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{cell(r.Queue), cell(strconv.FormatInt(r.Queued, 10)), cell(strconv.FormatInt(r.Pending, 10)), cell(strconv.FormatInt(r.Quarantined, 10))}})
		queued += r.Queued
		pending += r.Pending
		quarantined += r.Quarantined
	}
	if kind == "overview" {
		p.Metrics = []dashboardFact{{Name: "Configured queues", Value: strconv.Itoa(len(rows))}, {Name: "Queued deliveries", Value: strconv.FormatInt(queued, 10)}, {Name: "Pending deliveries", Value: strconv.FormatInt(pending, 10)}, {Name: "Quarantined", Value: strconv.FormatInt(quarantined, 10)}}
	}
	return nil
}

func (d *Dashboard) events(ctx context.Context, p *dashboardPage, q url.Values) error {
	p.Title = "Events"
	p.Description = "Retained observation events, oldest first. Delivery is best-effort: this is not a complete audit log or proof of execution."
	p.EventFilter = true
	p.Columns = []string{"Observed", "Event", "Task / Worker", "State"}
	if d.config.Events == nil {
		p.Description = "Event collection is not configured. Connect an EventReader and publish events from your clients and workers to enable this page."
		return nil
	}
	if !slicesContains(d.config.EventScopes, p.Scope) || !ValidEventCursor(q.Get("cursor")) {
		return ErrInvalid
	}
	page, err := (Control{Client: d.config.Client}).ReadEvents(ctx, d.config.Events, p.Scope, q.Get("cursor"), d.config.PageSize)
	if err != nil {
		return err
	}
	for _, e := range page.Entries {
		target := d.link("workers", e.Event.WorkerID, e.Event.WorkerID)
		if e.Event.TaskID != "" {
			target = d.link("tasks", e.Event.TaskID, e.Event.Task+" · "+e.Event.TaskID)
		}
		p.Rows = append(p.Rows, dashboardRow{[]dashboardCell{cell(dashboardTime(e.Event.At)), cell(e.Event.Kind), target, badge(string(e.Event.State))}})
	}
	if page.NextCursor != "" && page.NextCursor != q.Get("cursor") {
		q.Set("cursor", page.NextCursor)
		p.Next = d.config.BasePath + "events?" + q.Encode()
	}
	return nil
}
func slicesContains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
