package main

import (
	"fmt"
	"strings"
)

// parsedArgs is the result of splitting a tinvest-style argv into its command
// path, positional arguments, and flags.
type parsedArgs struct {
	// command is the space-joined command path, e.g. "quotes last",
	// "orders place", "version". It is the key scenario rules match on.
	command string
	// args are the positional arguments after the command path (instrument ids,
	// order ids, search text).
	args []string
	// flags maps each seen flag name (with its leading dashes) to its value(s).
	// Boolean and value-optional flags map to a single empty string. Repeatable
	// flags such as --instrument accumulate every value.
	flags map[string][]string
	// flagErr is set when a value flag was the last token with no value
	// (neither "--flag=value" nor a following "--flag value" token), mirroring
	// pflag's "flag needs an argument" parse failure. Parsing stops at that
	// point, matching pflag's fail-fast behavior.
	flagErr string
}

// flag names that consume the following token as their value (the "--flag
// value" form). Everything else is treated as a boolean unless written as
// "--flag=value". This mirrors the real cobra flag surface for the commands the
// fake speaks.
var valueFlags = map[string]bool{
	"-o": true, "--output": true, "--account": true, "--profile": true,
	"--timeout": true, "--token-file": true, "--depth": true, "--interval": true,
	"--from": true, "--to": true, "--instrument": true, "--direction": true,
	"--quantity": true, "--type": true, "--price": true, "--tif": true,
	"--order-id": true, "--cursor": true, "--status": true, "--input": true,
}

// value-optional flags (cobra NoOptDefVal): "--candles" and "--orderbook" only
// take a value in the "--flag=value" form; "--flag value" leaves value as the
// default and treats the next token as a positional, exactly as cobra does.
var noOptDefValFlags = map[string]bool{
	"--candles": true, "--orderbook": true,
}

// command groups that own a subcommand as their second token. When the first
// positional is one of these, the command key spans two tokens.
var commandGroups = map[string]bool{
	"instruments": true, "quotes": true, "orderbook": true, "candles": true,
	"portfolio": true, "positions": true, "operations": true, "orders": true,
	"stop-orders": true, "stream": true, "trades": true, "balance": true,
	"accounts": true, "signals": true, "sandbox": true, "user": true, "token": true,
}

// parseArgs splits a tinvest-style argv (without the program name) into its
// command path, positional arguments, and flags.
func parseArgs(argv []string) parsedArgs {
	flags := map[string][]string{}
	var positional []string
	var flagErr string

	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			positional = append(positional, tok)
			continue
		}
		name, inlineVal, hasInline := strings.Cut(tok, "=")
		if hasInline {
			flags[name] = append(flags[name], inlineVal)
			continue
		}
		if valueFlags[name] && !noOptDefValFlags[name] {
			if i+1 < len(argv) {
				flags[name] = append(flags[name], argv[i+1])
				i++
				continue
			}
			if flagErr == "" {
				flagErr = fmt.Sprintf("flag needs an argument: %s", name)
			}
			break
		}
		// Boolean or value-optional flag with no inline value.
		flags[name] = append(flags[name], "")
	}

	p := parsedArgs{flags: flags, flagErr: flagErr}
	if len(positional) == 0 {
		return p
	}
	if commandGroups[positional[0]] && len(positional) >= 2 {
		p.command = positional[0] + " " + positional[1]
		p.args = positional[2:]
	} else {
		p.command = positional[0]
		p.args = positional[1:]
	}
	return p
}

// flag returns the first value of a flag, checking both the long name and an
// optional short alias.
func (p parsedArgs) flag(names ...string) string {
	for _, n := range names {
		if v, ok := p.flags[n]; ok && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// flagAll returns every value collected for a repeatable flag.
func (p parsedArgs) flagAll(name string) []string {
	out := []string{}
	for _, v := range p.flags[name] {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// has reports whether a boolean flag was present.
func (p parsedArgs) has(name string) bool {
	_, ok := p.flags[name]
	return ok
}
