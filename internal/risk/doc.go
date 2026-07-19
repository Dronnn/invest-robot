// Package risk applies the configured safety limits to a decision engine's
// proposed actions before they reach execution (DESIGN.md §6, the
// "risk-check" stage). It is pure: no I/O, no clock, no engine coupling — it
// sees only model.Decision values and a State snapshot the caller (a later
// internal/cycle step) assembles from the portfolio and market data.
//
// Hard rule (DESIGN.md §8): risk never widens anything. Limits come only
// from config.RiskConfig; nothing here can make an action more permissive
// than the configured limits allow. Every quantity Check cannot prove
// safe — because price or instrument data is missing, or an arithmetic
// result would overflow model.Decimal — is treated conservatively: the
// action is stripped, never passed through on an optimistic default.
//
// Check evaluates seven rules in a fixed order, each seeing the result of
// every rule before it:
//
//  1. daily-loss kill switch — halts the cycle and strips every action that
//     is not a sell or close (flatten-only mode)
//  2. instrument allowlist — strips non-allowlisted buys (sells/closes
//     always pass: an existing position must always be exitable)
//  3. per-cycle order cap — keeps the first N order-producing actions
//  4. per-day order cap — keeps as many more as the day's budget allows
//  5. per-instrument position notional — shrinks or strips buys so existing
//     position + pending intents + kept buys stay within the per-instrument
//     limit
//  6. total exposure — shrinks or strips buys, across all instruments, so
//     the portfolio-wide notional stays within the total limit
//  7. cash floor — shrinks or strips buys so estimated cash after cost
//     (plus a slippage and fee buffer) never drops below the floor
//
// Every modification — a full strip or a lot-quantity shrink — is recorded
// as an Adjustment naming the rule that caused it; there is no silent
// mutation. hold actions are never touched by rules 2-7 (they carry no risk
// by themselves) and pass through unchanged unless the kill switch is
// engaged, in which case only sell/close survive.
package risk
