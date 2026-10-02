// Package settings has no app, adapter or startup imports. Every executable can
// share these definitions without importing another executable's dependencies.
package settings

import "github.com/Newton-School/gogo/core/conf"

func Schema() conf.Schema { return conf.CoreSchema() }
