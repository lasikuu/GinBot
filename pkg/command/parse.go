package command

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"connectrpc.com/connect"
)

// ParseChat splits a chat message into a command name and its raw arguments.
//
// ok is false when the message does not start with one of the prefixes, or
// carries no command name. Prefixes are matched longest-first so that "??" wins
// over "?" when both are configured.
//
// Arguments are whitespace-separated. A double-quoted run is one argument, with
// the quotes stripped. An unterminated quote takes the rest of the line.
//
// Only the ASCII double quote delimits. There is no escape, so an argument
// cannot contain a literal quote, and the curly quotes that mobile keyboards
// autocorrect to ("smart quotes") are ordinary characters. Both are deliberate:
// a chat command is typed by a human in a hurry, and a quoting language they
// have to think about is worse than not being able to quote a quote.
func ParseChat(content string, prefixes []string) (name string, args []string, ok bool) {
	prefix, found := matchPrefix(content, prefixes)
	if !found {
		return "", nil, false
	}

	tokens := tokenize(content[len(prefix):])
	if len(tokens) == 0 || tokens[0] == "" {
		return "", nil, false
	}

	return tokens[0], tokens[1:], true
}

// HasPrefix reports whether content carries one of the configured command
// prefixes, whether or not a command name follows it.
//
// ParseChat answers a narrower question — "is this a dispatchable command" — and
// says no to a bare prefix, which has no name to dispatch. A caller deciding
// whether a message was ADDRESSED to a bot at all needs the wider answer, and it
// has to come from this same matcher: two subtly different answers to "is this a
// command" is precisely the divergence that made config drop its own prefix
// regex.
func HasPrefix(content string, prefixes []string) bool {
	_, found := matchPrefix(content, prefixes)

	return found
}

// matchPrefix returns the longest configured prefix that content starts with.
// An empty prefix list disables chat commands entirely, and an empty prefix
// would match every message, so both yield no match.
//
// The longest match wins so that "??" is preferred over "?" and the command
// name does not silently keep a leading "?". It is found by a single scan
// rather than by sorting: this runs on every message in every guild, and the
// result must not depend on the order the prefixes were configured in.
func matchPrefix(content string, prefixes []string) (string, bool) {
	longest := ""

	for _, prefix := range prefixes {
		if prefix == "" || len(prefix) <= len(longest) {
			continue
		}
		if strings.HasPrefix(content, prefix) {
			longest = prefix
		}
	}

	return longest, longest != ""
}

// tokenize splits an argument line on whitespace, honouring double quotes.
func tokenize(line string) []string {
	var tokens []string
	var current strings.Builder

	inQuotes := false
	// started tracks whether current holds a token, so that "" yields one empty
	// argument rather than none, and runs of whitespace do not yield empties.
	started := false

	for _, r := range line {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			started = true
		case !inQuotes && unicode.IsSpace(r):
			if started {
				tokens = append(tokens, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}

	// An unterminated quote simply ends with the line.
	if started {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// Bind maps positional raw arguments onto a command's declared Args.
// It returns an error when a required argument is missing or an argument does
// not parse as its declared type.
//
// Raw arguments beyond the declared list are ignored: a chat command should not
// fail because someone typed a trailing word.
func Bind(cmd Command, raw []string) (*Invocation, error) {
	supplied := make(map[string]any, len(cmd.Args))

	for i, arg := range cmd.Args {
		if i >= len(raw) {
			break
		}

		value, err := parseArg(cmd, arg, raw[i])
		if err != nil {
			return nil, err
		}
		supplied[arg.Name] = value
	}

	return BindNamed(cmd, supplied)
}

// BindNamed maps named arguments onto a command's declared Args. It is the
// counterpart to Bind for platforms that deliver already-typed named arguments,
// such as Discord slash command options.
//
// Values must already match their declared type; unknown names are ignored.
func BindNamed(cmd Command, args map[string]any) (*Invocation, error) {
	bound := make(map[string]any, len(args))
	specs := make(map[string]Arg, len(cmd.Args))

	for _, arg := range cmd.Args {
		specs[arg.Name] = arg

		value, supplied := args[arg.Name]
		if !supplied {
			if arg.Required {
				return nil, connect.NewError(connect.CodeInvalidArgument,
					fmt.Errorf("%s: %s is required", cmd.Name, arg.Name))
			}
			continue
		}

		typed, err := coerceArg(cmd, arg, value)
		if err != nil {
			return nil, err
		}
		bound[arg.Name] = typed
	}

	return &Invocation{Args: bound, specs: specs}, nil
}

// parseArg converts one raw chat token into the argument's declared type.
func parseArg(cmd Command, arg Arg, raw string) (any, error) {
	switch arg.Type {
	case ArgString:
		return raw, nil

	case ArgInt:
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("%s: %s must be a whole number, got %q", cmd.Name, arg.Name, raw))
		}
		return value, nil

	case ArgBool:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("%s: %s must be true or false, got %q", cmd.Name, arg.Name, raw))
		}
		return value, nil

	default:
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("%s: %s has an unknown argument type", cmd.Name, arg.Name))
	}
}

// coerceArg checks an already-typed value against its declared type.
func coerceArg(cmd Command, arg Arg, value any) (any, error) {
	switch arg.Type {
	case ArgString:
		if typed, ok := value.(string); ok {
			return typed, nil
		}

	case ArgInt:
		if typed, ok := toInt64(value); ok {
			return typed, nil
		}

	case ArgBool:
		if typed, ok := value.(bool); ok {
			return typed, nil
		}

	default:
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("%s: %s has an unknown argument type", cmd.Name, arg.Name))
	}

	return nil, connect.NewError(connect.CodeInvalidArgument,
		fmt.Errorf("%s: %s has the wrong type", cmd.Name, arg.Name))
}
