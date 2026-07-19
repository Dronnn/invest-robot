package decision

import "github.com/Dronnn/invest-robot/internal/model"

// Response is the strict output every decision engine returns for one
// cycle: a batch of per-instrument actions plus optional free-text notes
// (e.g. which instruments were skipped and why).
//
// Actions reuses model.Decision directly, per DESIGN.md §6/§8: the same
// value flows from engine output through validation and risk adjustment
// into the decisions table. model.Decision itself carries no JSON struct
// tags (it is shared, engine-agnostic domain state, not a wire contract of
// its own), so its fields marshal under their Go names rather than
// snake_case — a known gap versus the rest of this package's JSON-clean
// fields, left as a follow-up outside internal/decision's boundary.
type Response struct {
	Actions []model.Decision `json:"actions"`
	Notes   string           `json:"notes,omitempty"`
}
