package db

// WindowSpec defines partitioning independently of the query's final ordering.
// Expressions refer to the query's stored fields or scalar aliases; they are
// not SQL fragments. A nil frame retains the backend's native default frame.
type WindowSpec struct {
	PartitionBy []Expression
	OrderBy     []WindowOrder
	Frame       *WindowFrame
}

type WindowOrder struct {
	Expression                  Expression
	Desc, NullsFirst, NullsLast bool
}

type WindowFrameMode string

const (
	WindowRows  WindowFrameMode = "rows"
	WindowRange WindowFrameMode = "range"
)

type WindowBoundKind string

const (
	WindowUnboundedPreceding WindowBoundKind = "unbounded_preceding"
	WindowPreceding          WindowBoundKind = "preceding"
	WindowCurrentRow         WindowBoundKind = "current_row"
	WindowFollowing          WindowBoundKind = "following"
	WindowUnboundedFollowing WindowBoundKind = "unbounded_following"
)

// Offset is a non-negative bound parameter for PRECEDING/FOLLOWING in ROWS
// frames. It must be zero for other kinds. RANGE offsets require a future
// type-specific numeric/interval contract and are explicitly unsupported.
type WindowBound struct {
	Kind   WindowBoundKind
	Offset int64
}

type WindowExclusion string

const (
	WindowExcludeCurrentRow WindowExclusion = "current_row"
	WindowExcludeGroup      WindowExclusion = "group"
	WindowExcludeTies       WindowExclusion = "ties"
	WindowExcludeNoOthers   WindowExclusion = "no_others"
)

type WindowFrame struct {
	Mode       WindowFrameMode
	Start, End WindowBound
	Exclusion  WindowExclusion
}
