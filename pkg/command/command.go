// Package command holds the platform-neutral command catalogue. Nothing here
// may import a platform SDK.
package command

import (
	"context"
	"fmt"
	"slices"
	"strings"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

type ArgType int

const (
	ArgString ArgType = iota
	ArgInt
	ArgBool
)

type Arg struct {
	Name        string
	Description string
	Type        ArgType
	Required    bool
	// Default applies when the argument is absent. Ignored when Required.
	Default any
}

// Response is a platform-neutral command result. Every field beyond Content is
// a request a platform may degrade rather than fail on (ADR-0010).
type Response struct {
	Content string
	// Ephemeral asks that the response be shown only to the invoker.
	Ephemeral bool
	// ReRollID, when non-empty, asks for a re-invoke control carrying this id.
	ReRollID string
	// File must not be silently dropped: an empty Content is not a response.
	File *ResponseFile
	// DirectWhenLong asks that content too long for one message be delivered
	// privately rather than truncated. See ADR-0040.
	DirectWhenLong bool
}

// ResponseFile carries bytes rather than a URL: see ADR-0007.
type ResponseFile struct {
	Name     string
	MIMEType string
	Content  []byte
}

// Invocation is a command's declared arguments bound to the caller's values.
// Caller identity travels in the context instead, see pkg/grpc/callermeta.
type Invocation struct {
	// Args holds only supplied arguments; the accessors fall back to specs, so
	// Has still distinguishes supplied from defaulted.
	Args map[string]any

	specs map[string]Arg
}

func (inv *Invocation) String(name string) string {
	if value, ok := inv.Args[name].(string); ok {
		return value
	}
	if value, ok := inv.specs[name].Default.(string); ok {
		return value
	}

	return ""
}

func (inv *Invocation) Int(name string) int64 {
	if value, ok := toInt64(inv.Args[name]); ok {
		return value
	}
	if value, ok := toInt64(inv.specs[name].Default); ok {
		return value
	}

	return 0
}

func (inv *Invocation) Bool(name string) bool {
	if value, ok := inv.Args[name].(bool); ok {
		return value
	}
	if value, ok := inv.specs[name].Default.(bool); ok {
		return value
	}

	return false
}

func (inv *Invocation) Has(name string) bool {
	_, ok := inv.Args[name]
	return ok
}

// toInt64 also accepts int: an untyped literal Default lands as one.
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

// Handler executes a command; ctx already carries caller identity.
type Handler func(ctx context.Context, inv *Invocation) (*Response, error)

type Command struct {
	Name        string
	Aliases     []string
	Description string
	Args        []Arg
	// Group nests this command under a shared parent, /{Group} {Sub} on
	// Discord; platforms without the concept use Name.
	Group string
	// Sub is the name WITHIN the group, required when Group is set.
	Sub string
	// Clearance is the minimum required level. Not enforced in this phase.
	Clearance pb.Clearance
	// Slow marks a handler that may outlast Discord's three-second
	// acknowledgement deadline, so the platform acknowledges before running it.
	Slow bool
	// Ephemeral asks that this command's responses be shown only to the
	// invoker. Read before the handler runs, so a deferred interaction is
	// acknowledged with the right visibility. See ADR-0038.
	Ephemeral bool
	Handler   Handler
}

type commandGroup struct {
	name    string
	members map[string]string
}

// Registry holds the commands. Register mutates unsynchronised maps, so build
// it completely before serving; concurrent reads afterwards are safe.
type Registry struct {
	// commands is keyed by folded canonical name.
	commands map[string]Command
	// canonical maps a folded name or alias onto the folded canonical name.
	canonical map[string]string
	// groups is keyed by folded group name, sharing the canonical namespace.
	groups map[string]commandGroup
}

func NewRegistry() *Registry {
	return &Registry{
		commands:  make(map[string]Command),
		canonical: make(map[string]string),
		groups:    make(map[string]commandGroup),
	}
}

// Register errors on any collision with a registered name, alias or group, and
// on a declaration dispatch could not satisfy.
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
	if err := validateGrouping(cmd); err != nil {
		return err
	}

	// Collect first, insert second: a second alias colliding must not
	// half-register the command.
	keys := make([]string, 0, 1+len(cmd.Aliases))
	keys = append(keys, name)
	for _, alias := range cmd.Aliases {
		folded := fold(alias)
		if folded == "" {
			return fmt.Errorf("command %q has an empty alias", cmd.Name)
		}
		keys = append(keys, folded)
	}

	group, sub := fold(cmd.Group), fold(cmd.Sub)

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if existing, taken := r.canonical[key]; taken {
			return fmt.Errorf("command %q: %q is already registered by %q", cmd.Name, key, existing)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("command %q declares %q more than once", cmd.Name, key)
		}
		// ResolveChat prefers the flat name, which would shadow the group.
		if existing, taken := r.groups[key]; taken {
			return fmt.Errorf("command %q: %q is already a command group (%q)", cmd.Name, key, existing.name)
		}
		if key == group {
			return fmt.Errorf("command %q declares %q as both its own name or alias and its group", cmd.Name, key)
		}
		seen[key] = struct{}{}
	}

	if group != "" {
		if existing, taken := r.canonical[group]; taken {
			return fmt.Errorf("command %q: group %q is already a command name or alias of %q", cmd.Name, cmd.Group, existing)
		}
		if existing, taken := r.groups[group].members[sub]; taken {
			return fmt.Errorf("command %q: %q %q is already registered by %q", cmd.Name, cmd.Group, cmd.Sub, existing)
		}
	}

	r.commands[name] = cmd
	for _, key := range keys {
		r.canonical[key] = name
	}
	if group != "" {
		existing, ok := r.groups[group]
		if !ok {
			existing = commandGroup{name: strings.TrimSpace(cmd.Group), members: make(map[string]string)}
		}
		existing.members[sub] = name
		r.groups[group] = existing
	}

	return nil
}

func validateGrouping(cmd Command) error {
	if cmd.Group == "" && cmd.Sub == "" {
		return nil
	}
	if cmd.Group == "" {
		return fmt.Errorf("command %q declares sub %q without a group", cmd.Name, cmd.Sub)
	}
	if cmd.Sub == "" {
		return fmt.Errorf("command %q declares group %q without a sub", cmd.Name, cmd.Group)
	}
	if fold(cmd.Group) == "" {
		return fmt.Errorf("command %q declares a blank group %q", cmd.Name, cmd.Group)
	}
	if fold(cmd.Sub) == "" {
		return fmt.Errorf("command %q declares a blank sub %q", cmd.Name, cmd.Sub)
	}

	return nil
}

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

// ResolveChat returns the command and the arguments left after the name is
// consumed. A flat name wins; failing that, a group plus a consumed sub token.
func (r *Registry) ResolveChat(name string, args []string) (cmd Command, rest []string, ok bool) {
	if cmd, found := r.Lookup(name); found {
		return cmd, args, true
	}

	group, isGroup := r.groups[fold(name)]
	if !isGroup || len(args) == 0 {
		return Command{}, nil, false
	}

	canonical, found := group.members[fold(args[0])]
	if !found {
		return Command{}, nil, false
	}

	cmd, found = r.commands[canonical]
	if !found {
		return Command{}, nil, false
	}

	return cmd, args[1:], true
}

// Groups returns every group name as declared, ordered.
func (r *Registry) Groups() []string {
	groups := make([]string, 0, len(r.groups))
	for _, group := range r.groups {
		groups = append(groups, group.name)
	}

	// Sorted so a platform generates the same definition on every start.
	slices.Sort(groups)

	return groups
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

// fold is Unicode-aware: localised aliases are not ASCII.
func fold(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
