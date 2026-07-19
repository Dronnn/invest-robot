// Package deps blank-imports Phase 1 dependencies that are pre-added to
// go.mod before the packages that will use them exist. This keeps
// `go mod tidy` from dropping them between scaffold and the steps that
// introduce internal/tui and internal/store/sqlite. Remove this file once
// those steps land and import the packages for real.
package deps

import (
	_ "github.com/charmbracelet/bubbles/list"
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/lipgloss"
)
