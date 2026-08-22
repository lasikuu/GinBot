// Package command holds the platform-neutral command catalogue.
//
// The same command must be reachable as a Discord slash command, as a chat
// message with a prefix, and eventually as a Matrix message. Keeping the
// catalogue here means one registration per command instead of one per
// platform, and it lets Discord's ApplicationCommand definitions be generated
// from the registry so the two invocation paths cannot diverge.
//
// Nothing in this package may import a platform SDK. Platform layers translate
// their own invocation shape into an Invocation and render a Response.
package command

import (
	"context"
	"fmt"
	"slices"
	"strings"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// ArgType is the type of a command argument.
type ArgType int

const (
	ArgString ArgType = iota
	ArgInt
	ArgBool
)

// Arg declares one argument of a command.
type Arg struct {
	Name        string
	Description string
	Type        ArgType
	Required    bool
	// Default is used when the argument is absent. Ignored when Required.
	Default any
}

// Response is a platform-neutral command result. The platform layer renders it.
type Response struct {
	Content string
	// Ephemeral asks the platform to show the response only to the invoker.
	// Platforms without the concept ignore it.
	Ephemeral bool
	// ReRollID, when non-empty, asks the platform to attach a re-invoke control
	// carrying this identifier. Platforms without the concept ignore it.
	ReRollID string
}

// Invocation is a resolved call: a command's declared arguments bound to the
// values supplied by the caller.
//
// Caller identity is deliberately absent. It travels in the context as gRPC
// metadata (see pkg/grpc/callermeta), so a handler never has to choose between
// two sources of truth for who is calling.
type Invocation struct {
	// Args holds only the arguments that were supplied. An absent optional
	// argument is resolved by the accessors from its declared Default, so that
	// Has can still distinguish "supplied" from "defaulted".
	Args map[string]any

	// specs carries the declared arguments so the accessors can fall back to
	// Arg.Default without the caller passing the Command back in.
	specs map[string]Arg
}

// String returns a string argument, or its default, or "".
func (inv *Invocation) String(name string) string {
	if value, ok := inv.Args[name].(string); ok {
		return value
	}
	if value, ok := inv.specs[name].Default.(string); ok {
		return value
	}

	return ""
}

// Int returns an integer argument, or its default, or 0.
func (inv *Invocation) Int(name string) int64 {
	if value, ok := toInt64(inv.Args[name]); ok {
		return value
	}
	if value, ok := toInt64(inv.specs[name].Default); ok {
		return value
	}

	return 0
}

// Bool returns a boolean argument, or its default, or false.
func (inv *Invocation) Bool(name string) bool {
	if value, ok := inv.Args[name].(bool); ok {
		return value
	}
	if value, ok := inv.specs[name].Default.(bool); ok {
		return value
	}

	return false
}

// Has reports whether the argument was supplied.
func (inv *Invocation) Has(name string) bool {
	_, ok := inv.Args[name]
	return ok
}

// toInt64 accepts the integer kinds a Default may be written as: an untyped
// literal assigned to an `any` field lands as int, not int64.
func toInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case int64:
		return number, true
	case int:
		return int64(number), true
	case int32:
		return int64(number), true
	}

	return 0, false
}

// Handler executes a command. ctx already carries caller identity metadata.
type Handler func(ctx context.Context, inv *Invocation) (*Response, error)

// Command is one registered command.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Args        []Arg
	// Clearance is the minimum required level. Not enforced in this phase.
	Clearance pb.Clearance
	Handler   Handler
}

// Registry holds the commands.
//
// A Registry is NOT safe for concurrent use while it is being written to.
// Build it completely before serving: platform clients dispatch handlers from
// many goroutines, and Register mutates unsynchronised maps. Concurrent reads
// after the last Register are safe.
type Registry struct {
	// commands is keyed by folded canonical name.
	commands map[string]Command
	// canonical maps a folded name or alias onto the folded canonical name, so
	// that alias resolution costs one lookup and collisions are detectable.
	canonical map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		commands:  make(map[string]Command),
		canonical: make(map[string]string),
	}
}

// Register adds a command. It returns an error if the name or any alias
// collides with an already-registered name or alias.
//
// It also rejects a command that cannot work once dispatched — no handler, no
// name, a duplicate argument name, or an optional argument before a required
// one. Chat arguments bind positionally, so a required argument after an
// optional one is unbindable. Registration happens at startup, where a loud
// failure is cheap; a silently broken command is not.
func (r *Registry) Register(cmd Command) error {
	name := fold(cmd.Name)
	if name == "" {
		return fmt.Errorf("command name must not be empty")
	}
	if cmd.Handler == nil {
		return fmt.Errorf("command %q has no handler", cmd.Name)
	}
	if err := validateArgs(cmd); err != nil {
		return err
	}

	// Collect first, insert second: a command must not half-register when its
	// second alias collides.
	keys := make([]string, 0, 1+len(cmd.Aliases))
	keys = append(keys, name)
	for _, alias := range cmd.Aliases {
		folded := fold(alias)
		if folded == "" {
			return fmt.Errorf("command %q has an empty alias", cmd.Name)
		}
		keys = append(keys, folded)
	}

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if existing, taken := r.canonical[key]; taken {
			return fmt.Errorf("command %q: %q is already registered by %q", cmd.Name, key, existing)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("command %q declares %q more than once", cmd.Name, key)
		}
		seen[key] = struct{}{}
	}

	r.commands[name] = cmd
	for _, key := range keys {
		r.canonical[key] = name
	}

	return nil
}

// validateArgs rejects argument declarations that positional binding cannot
// satisfy.
func validateArgs(cmd Command) error {
	seen := make(map[string]struct{}, len(cmd.Args))
	optionalSeen := false

	for _, arg := range cmd.Args {
		name := fold(arg.Name)
		if name == "" {
			return fmt.Errorf("command %q has an unnamed argument", cmd.Name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("command %q declares argument %q more than once", cmd.Name, arg.Name)
		}
		seen[name] = struct{}{}

		// An out-of-range type would otherwise surface as codes.Internal at
		// dispatch, and only for an argument the caller actually supplied.
		if arg.Type < ArgString || arg.Type > ArgBool {
			return fmt.Errorf("command %q declares argument %q with unknown type %d", cmd.Name, arg.Name, arg.Type)
		}

		if arg.Required && optionalSeen {
			return fmt.Errorf("command %q declares required argument %q after an optional one", cmd.Name, arg.Name)
		}
		if !arg.Required {
			optionalSeen = true
		}
	}

	return nil
}

// Lookup resolves a name or alias, case-insensitively.
func (r *Registry) Lookup(name string) (Command, bool) {
	canonical, ok := r.canonical[fold(name)]
	if !ok {
		return Command{}, false
	}

	cmd, ok := r.commands[canonical]
	return cmd, ok
}

// All returns every registered command, ordered by name.
func (r *Registry) All() []Command {
	all := make([]Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		all = append(all, cmd)
	}

	slices.SortFunc(all, func(a, b Command) int {
		return strings.Compare(a.Name, b.Name)
	})

	return all
}

// fold normalises a name for case-insensitive matching. Command names are
// ASCII by convention, but localised aliases are not, so the comparison is
// Unicode-aware.
func fold(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
