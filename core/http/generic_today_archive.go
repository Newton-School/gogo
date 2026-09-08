package http

import (
	"net/http"
	"time"
)

// TodayArchiveViewOptions selects today's local calendar date without a route
// or request date decoder. Its other read, field, pagination and navigation
// contracts are the same as DayArchiveViewOptions.
type TodayArchiveViewOptions struct {
	ListViewOptions
	DateField  string
	AllowEmpty bool
	// AllowFuture includes later instants today. It never changes which day
	// this view selects. Clock is still needed when this option is true.
	AllowFuture bool
	// Clock defaults to time.Now and runs exactly once per eligible GET/HEAD,
	// after request/model authorization and scope capture. The same instant
	// selects the local date and, unless AllowFuture, the publication cutoff.
	// It must be read-only and cooperate with the view's bounded operation.
	Clock func() time.Time
}

// NewTodayArchiveView creates a scoped, paginated GET/HEAD archive of today's
// Date/DateTime rows. The privately captured template locale resolver selects
// the timezone; without one the inherited locale or UTC applies, never the
// host timezone. DST day boundaries and navigation match NewDayArchiveView.
func NewTodayArchiveView(options TodayArchiveViewOptions) (http.Handler, error) {
	return newDayArchiveView(DayArchiveViewOptions{
		ListViewOptions: options.ListViewOptions,
		DateField:       options.DateField,
		AllowEmpty:      options.AllowEmpty,
		AllowFuture:     options.AllowFuture,
		Clock:           options.Clock,
	}, true)
}
