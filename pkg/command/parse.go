package command

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"connectrpc.com/connect"
)

// ParseChat splits a chat message into a name and raw whitespace-separated
// arguments. Only the ASCII double quote groups one, and there is no escape.
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

// HasPrefix accepts a bare prefix, which ParseChat rejects for having no name.
func HasPrefix(content string, prefixes []string) bool {
	_, found := matchPrefix(content, prefixes)

	return found
}

// matchPrefix takes the longest prefix, so "??" beats "?"; "" never matches.
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

func tokenize(line string) []string {
	var tokens []string
	var current strings.Builder

	inQuotes := false
	// started makes "" yield one empty argument while whitespace runs yield none.
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

	if started {
		tokens = append(tokens, current.String())
	}

	return tokens
}

// Bind maps positional raw arguments onto a command's declared Args. Extra raw
// arguments are ignored.
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

// BindNamed is Bind for already-typed values; unknown names are ignored.
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
