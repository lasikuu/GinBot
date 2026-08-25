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

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
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
//
// Every field beyond Content is a REQUEST, not an instruction: a platform that
// cannot express one degrades rather than fails (ADR-0010).
//
// File is the exception ADR-0010 predicted. Dropping it loses the response
// itself, and a file-only response has an empty Content to degrade to, so a
// platform without attachments must say something of its own rather than send
// an empty message.
type Response struct {
	Content string
	// Ephemeral asks the platform to show the response only to the invoker.
	// Platforms without the concept ignore it.
	Ephemeral bool
	// ReRollID, when non-empty, asks the platform to attach a re-invoke control
	// carrying this identifier. Platforms without the concept ignore it.
	ReRollID string
	// File, when non-nil, asks the platform to attach this file to the response.
	// Platforms without attachments should fall back to Content alone rather
	// than dropping the response.
	File *ResponseFile
}

// ResponseFile is an attachment on a command response. It carries bytes rather
// than a URL because the server stores trigger media itself and the platform's
// own CDN URL has expired by the time a trigger fires (ADR-0007).
type ResponseFile struct {
	Name     string
	MIMEType string
	Content  []byte
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
	// Group, when non-empty, nests this command under a shared parent on platforms
	// that support it. Discord renders it as /{Group} {Sub}. Platforms without the
	// concept keep using Name.
	Group string
	// Sub is the command's name WITHIN its group. Required when Group is set,
	// ignored otherwise.
	Sub string
	// Clearance is the minimum required level. Not enforced in this phase.
	Clearance pb.Clearance
	// Slow marks a handler that may outlast a platform's acknowledgement
	// deadline. Discord gives three seconds before it tells the user "the
	// application did not respond" and invalidates the token, which is not
	// enough for a handler whose server side fetches media from a CDN.
	//
	// A platform that must acknowledge before the handler returns does so
	// first and delivers the result afterwards. Because that acknowledgement
	// has to commit to a visibility before the Response exists, Ephemeral is
	// decided by the acknowledgement rather than by the handler on this path —
	// unsupported intent degrades, per ADR-0010. Platforms with no such
	// deadline ignore the field.
	Slow    bool
	Handler Handler
}

// commandGroup is one registered group: the name as it was declared, and its
// members keyed by folded Sub onto the folded canonical command name.
//
// The declared name is kept because a platform renders the group verbatim — the
// folded key exists only so matching is case-insensitive like every other name
// here.
type commandGroup struct {
	name    string
	members map[string]string
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
	// groups is keyed by folded group name. A group shares the canonical
	// namespace: a group name that is also a command name or alias would make
	// ResolveChat ambiguous, so Register refuses it.
	groups map[string]commandGroup
}

func NewRegistry() *Registry {
	return &Registry{
		commands:  make(map[string]Command),
		canonical: make(map[string]string),
		groups:    make(map[string]commandGroup),
	}
}

// Register adds a command. It returns an error if the name or any alias
// collides with an already-registered name, alias or group.
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
	if err := validateGrouping(cmd); err != nil {
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

	group, sub := fold(cmd.Group), fold(cmd.Sub)

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if existing, taken := r.canonical[key]; taken {
			return fmt.Errorf("command %q: %q is already registered by %q", cmd.Name, key, existing)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("command %q declares %q more than once", cmd.Name, key)
		}
		// A flat name that is also a group name would shadow the whole group,
		// since ResolveChat prefers the flat interpretation.
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

// validateGrouping rejects a half-declared or unusable Group/Sub pair.
//
// Both fields are checked before anything is inserted, because a group is a
// second namespace: a command that lands in one under a blank or lopsided key
// would be reachable by neither /{Group} {Sub} nor a sensible chat invocation.
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

// ResolveChat resolves a chat invocation to a command and the arguments that
// remain after the command name is consumed.
//
// It tries a flat name or alias first, so ??remindme keeps working. Failing
// that, when name matches a group and the first argument names one of its
// subcommands, it resolves that and consumes the subcommand token — so
// ??reminder add behaves exactly like ??remindme.
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

// Groups returns every registered group name as it was declared, ordered.
//
// Membership is not returned with it: a group's members are the commands in All
// whose Group matches, which the platform layer already walks to generate its
// own definitions.
func (r *Registry) Groups() []string {
	groups := make([]string, 0, len(r.groups))
	for _, group := range r.groups {
		groups = append(groups, group.name)
	}

	// Map iteration order is random; sorted so a platform generates an identical
	// definition on every start.
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

// fold normalises a name for case-insensitive matching. Command names are
// ASCII by convention, but localised aliases are not, so the comparison is
// Unicode-aware.
func fold(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
