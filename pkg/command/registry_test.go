package command

import (
	"context"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

func TestRegisterAndLookup(t *testing.T) {
	r := NewRegistry()

	want := Command{
		Name:        "healthcheck",
		Aliases:     []string{"health", "hc"},
		Description: "Check that the server is alive",
		Clearance:   pb.Clearance_CLEARANCE_MODERATOR,
		Handler: func(ctx context.Context, inv *Invocation) (*Response, error) {
			return &Response{Content: "ok", Ephemeral: true}, nil
		},
	}

	if err := r.Register(want); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Lookup("healthcheck")
	if !ok {
		t.Fatal("Lookup found nothing after a successful Register")
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Description != want.Description {
		t.Errorf("Description = %q, want %q", got.Description, want.Description)
	}
	if got.Clearance != want.Clearance {
		t.Errorf("Clearance = %v, want %v", got.Clearance, want.Clearance)
	}
	if len(got.Aliases) != len(want.Aliases) {
		t.Errorf("Aliases = %q, want %q", got.Aliases, want.Aliases)
	}
	if got.Handler == nil {
		t.Fatal("Handler is nil; the registered handler was dropped")
	}

	resp, err := got.Handler(context.Background(), &Invocation{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if resp.Content != "ok" || !resp.Ephemeral {
		t.Errorf("Response = %+v, want the registered handler's own response", resp)
	}
}

func TestRegisterRejectsCollisions(t *testing.T) {
	seed := []Command{
		{Name: "ping", Aliases: []string{"p", "pong"}, Handler: noopHandler},
		{Name: "doubles", Aliases: []string{"tuplat"}, Handler: noopHandler},
	}

	tests := []struct {
		name string
		add  Command
	}{
		{
			name: "duplicate name",
			add:  Command{Name: "ping", Handler: noopHandler},
		},
		{
			name: "duplicate name in a different case",
			add:  Command{Name: "PING", Handler: noopHandler},
		},
		{
			name: "duplicate alias",
			add:  Command{Name: "unique", Aliases: []string{"pong"}, Handler: noopHandler},
		},
		{
			name: "duplicate alias in a different case",
			add:  Command{Name: "unique", Aliases: []string{"PONG"}, Handler: noopHandler},
		},
		{
			name: "alias collides with an existing name",
			add:  Command{Name: "unique", Aliases: []string{"doubles"}, Handler: noopHandler},
		},
		{
			name: "name collides with an existing alias",
			add:  Command{Name: "tuplat", Handler: noopHandler},
		},
		{
			name: "alias collides with the command's own name",
			add:  Command{Name: "unique", Aliases: []string{"unique"}, Handler: noopHandler},
		},
		{
			name: "the same alias declared twice in one command",
			add:  Command{Name: "unique", Aliases: []string{"dup", "dup"}, Handler: noopHandler},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for _, cmd := range seed {
				if err := r.Register(cmd); err != nil {
					t.Fatalf("seeding %q: %v", cmd.Name, err)
				}
			}

			if err := r.Register(tt.add); err == nil {
				t.Fatalf("Register(%+v) succeeded, want a collision error", tt.add)
			}
		})
	}
}

func TestRegisterRejectsBrokenDeclarations(t *testing.T) {
	tests := []struct {
		name string
		add  Command
	}{
		{
			name: "empty name",
			add:  Command{Name: "", Handler: noopHandler},
		},
		{
			name: "whitespace-only name",
			add:  Command{Name: "   ", Handler: noopHandler},
		},
		{
			name: "no handler",
			add:  Command{Name: "ping"},
		},
		{
			name: "empty alias",
			add:  Command{Name: "ping", Aliases: []string{""}, Handler: noopHandler},
		},
		{
			name: "unnamed argument",
			add: Command{
				Name:    "ping",
				Args:    []Arg{{Name: "", Type: ArgString}},
				Handler: noopHandler,
			},
		},
		{
			name: "duplicate argument name",
			add: Command{
				Name:    "ping",
				Args:    []Arg{{Name: "n", Type: ArgInt}, {Name: "n", Type: ArgString}},
				Handler: noopHandler,
			},
		},
		{
			name: "required argument after an optional one",
			add: Command{
				Name: "ping",
				Args: []Arg{
					{Name: "optional", Type: ArgString},
					{Name: "required", Type: ArgString, Required: true},
				},
				Handler: noopHandler,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			if err := r.Register(tt.add); err == nil {
				t.Fatalf("Register(%+v) succeeded, want a validation error", tt.add)
			}
			if got := len(r.All()); got != 0 {
				t.Errorf("All() has %d commands after a rejected Register, want 0", got)
			}
		})
	}
}

func TestRegisterIsAtomicOnFailure(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Command{Name: "ping", Handler: noopHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := r.Register(Command{Name: "broken", Aliases: []string{"free", "ping"}, Handler: noopHandler}); err == nil {
		t.Fatal("Register succeeded despite an alias colliding with an existing name")
	}

	for _, name := range []string{"broken", "free"} {
		if cmd, ok := r.Lookup(name); ok {
			t.Errorf("Lookup(%q) resolved to %q after a failed Register", name, cmd.Name)
		}
	}
	if got := len(r.All()); got != 1 {
		t.Errorf("All() has %d commands, want only the one that registered successfully", got)
	}
}

func TestRegisterRejectsBrokenGrouping(t *testing.T) {
	tests := []struct {
		name string
		seed []Command
		add  Command
	}{
		{
			name: "sub without group",
			add:  Command{Name: "remind", Sub: "add", Handler: noopHandler},
		},
		{
			name: "group without sub",
			add:  Command{Name: "remind", Group: "reminder", Handler: noopHandler},
		},
		{
			name: "whitespace-only group",
			add:  Command{Name: "remind", Group: "   ", Sub: "add", Handler: noopHandler},
		},
		{
			name: "whitespace-only sub",
			add:  Command{Name: "remind", Group: "reminder", Sub: "  ", Handler: noopHandler},
		},
		{
			name: "duplicate group and sub",
			seed: []Command{{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler}},
			add:  Command{Name: "reminderadd2", Group: "reminder", Sub: "add", Handler: noopHandler},
		},
		{
			name: "duplicate sub in a different case",
			seed: []Command{{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler}},
			add:  Command{Name: "reminderadd2", Group: "REMINDER", Sub: "ADD", Handler: noopHandler},
		},
		{
			name: "group collides with an existing command name",
			seed: []Command{{Name: "reminder", Handler: noopHandler}},
			add:  Command{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler},
		},
		{
			name: "group collides with an existing alias",
			seed: []Command{{Name: "notes", Aliases: []string{"reminder"}, Handler: noopHandler}},
			add:  Command{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler},
		},
		{
			name: "name collides with an existing group",
			seed: []Command{{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler}},
			add:  Command{Name: "reminder", Handler: noopHandler},
		},
		{
			name: "alias collides with an existing group",
			seed: []Command{{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler}},
			add:  Command{Name: "notes", Aliases: []string{"reminder"}, Handler: noopHandler},
		},
		{
			name: "name collides with an existing group in a different case",
			seed: []Command{{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler}},
			add:  Command{Name: "REMINDER", Handler: noopHandler},
		},
		{
			name: "command is named after its own group",
			add:  Command{Name: "reminder", Group: "reminder", Sub: "list", Handler: noopHandler},
		},
		{
			name: "alias collides with the command's own group",
			add: Command{
				Name:    "remind",
				Aliases: []string{"reminder"},
				Group:   "reminder",
				Sub:     "add",
				Handler: noopHandler,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			for _, cmd := range tt.seed {
				if err := r.Register(cmd); err != nil {
					t.Fatalf("seeding %q: %v", cmd.Name, err)
				}
			}

			if err := r.Register(tt.add); err == nil {
				t.Fatalf("Register(%+v) succeeded, want a grouping error", tt.add)
			}
			if got := len(r.All()); got != len(tt.seed) {
				t.Errorf("All() has %d commands after a rejected Register, want %d", got, len(tt.seed))
			}
			if _, ok := r.Lookup(tt.add.Name); ok && len(tt.seed) == 0 {
				t.Errorf("Lookup(%q) resolved after a rejected Register", tt.add.Name)
			}
		})
	}
}

func groupedRegistry(t *testing.T) *Registry {
	t.Helper()

	r := NewRegistry()
	seed := []Command{
		{
			Name:    "remind",
			Aliases: []string{"remindme"},
			Group:   "reminder",
			Sub:     "add",
			Args: []Arg{
				{Name: "when", Type: ArgString, Required: true},
				{Name: "message", Type: ArgString, Required: true},
			},
			Handler: noopHandler,
		},
		{Name: "reminders", Group: "reminder", Sub: "list", Handler: noopHandler},
		{Name: "ping", Aliases: []string{"pong"}, Handler: noopHandler},
	}

	for _, cmd := range seed {
		if err := r.Register(cmd); err != nil {
			t.Fatalf("seeding %q: %v", cmd.Name, err)
		}
	}

	return r
}

func TestResolveChat(t *testing.T) {
	r := groupedRegistry(t)

	tests := []struct {
		name     string
		invoked  string
		args     []string
		wantName string
		wantRest []string
	}{
		{
			name:     "flat name",
			invoked:  "remind",
			args:     []string{"in 2h", "tea"},
			wantName: "remind",
			wantRest: []string{"in 2h", "tea"},
		},
		{
			name:     "flat alias",
			invoked:  "remindme",
			args:     []string{"in 2h", "tea"},
			wantName: "remind",
			wantRest: []string{"in 2h", "tea"},
		},
		{
			name:     "group and sub",
			invoked:  "reminder",
			args:     []string{"add", "in 2h", "tea"},
			wantName: "remind",
			wantRest: []string{"in 2h", "tea"},
		},
		{
			name:     "group and sub with no further arguments",
			invoked:  "reminder",
			args:     []string{"list"},
			wantName: "reminders",
			wantRest: nil,
		},
		{
			name:     "group in a different case",
			invoked:  "REMINDER",
			args:     []string{"add", "in 2h", "tea"},
			wantName: "remind",
			wantRest: []string{"in 2h", "tea"},
		},
		{
			name:     "sub in a different case",
			invoked:  "reminder",
			args:     []string{"ADD", "in 2h", "tea"},
			wantName: "remind",
			wantRest: []string{"in 2h", "tea"},
		},
		{
			name:     "flat name with no arguments",
			invoked:  "ping",
			wantName: "ping",
			wantRest: nil,
		},
		{
			name:     "a flat name is preferred over a group interpretation",
			invoked:  "remind",
			args:     []string{"add", "tea"},
			wantName: "remind",
			wantRest: []string{"add", "tea"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, rest, ok := r.ResolveChat(tt.invoked, tt.args)
			if !ok {
				t.Fatalf("ResolveChat(%q, %q) ok = false, want true", tt.invoked, tt.args)
			}
			if cmd.Name != tt.wantName {
				t.Errorf("resolved to %q, want %q", cmd.Name, tt.wantName)
			}
			if !equalArgs(rest, tt.wantRest) {
				t.Errorf("rest = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

func TestResolveChatMiss(t *testing.T) {
	r := groupedRegistry(t)

	tests := []struct {
		name    string
		invoked string
		args    []string
	}{
		{
			name:    "unknown name",
			invoked: "nosuchcommand",
			args:    []string{"add"},
		},
		{
			name:    "group with no following argument",
			invoked: "reminder",
		},
		{
			name:    "group with an unknown sub",
			invoked: "reminder",
			args:    []string{"nope", "in 2h"},
		},
		{
			name:    "group followed by a member's flat name",
			invoked: "reminder",
			args:    []string{"remind", "in 2h"},
		},
		{
			name:    "sub used as a top-level name",
			invoked: "add",
			args:    []string{"in 2h"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, rest, ok := r.ResolveChat(tt.invoked, tt.args)
			if ok {
				t.Fatalf("ResolveChat(%q, %q) resolved to %q, want no match", tt.invoked, tt.args, cmd.Name)
			}
			if cmd.Name != "" {
				t.Errorf("Name = %q, want the zero Command on a miss", cmd.Name)
			}
			if len(rest) != 0 {
				t.Errorf("rest = %q, want empty on a miss", rest)
			}
		})
	}
}

func TestGroups(t *testing.T) {
	if got := NewRegistry().Groups(); len(got) != 0 {
		t.Errorf("Groups() = %v on an empty registry, want empty", got)
	}

	r := NewRegistry()
	seed := []Command{
		{Name: "remind", Group: "reminder", Sub: "add", Handler: noopHandler},
		{Name: "reminders", Group: "reminder", Sub: "list", Handler: noopHandler},
		{Name: "squadadd", Group: "squad", Sub: "add", Handler: noopHandler},
		{Name: "ping", Handler: noopHandler},
	}
	for _, cmd := range seed {
		if err := r.Register(cmd); err != nil {
			t.Fatalf("seeding %q: %v", cmd.Name, err)
		}
	}

	want := []string{"reminder", "squad"}

	for attempt := range 2 {
		if got := r.Groups(); !equalArgs(got, want) {
			t.Errorf("attempt %d: Groups() = %q, want %q", attempt, got, want)
		}
	}
}

func TestGroupedCommandKeepsItsGroupAndSub(t *testing.T) {
	r := groupedRegistry(t)

	cmd, ok := r.Lookup("remind")
	if !ok {
		t.Fatal("Lookup(remind) found nothing")
	}
	if cmd.Group != "reminder" || cmd.Sub != "add" {
		t.Errorf("Group/Sub = %q/%q, want %q/%q", cmd.Group, cmd.Sub, "reminder", "add")
	}

	ping, ok := r.Lookup("ping")
	if !ok {
		t.Fatal("Lookup(ping) found nothing")
	}
	if ping.Group != "" || ping.Sub != "" {
		t.Errorf("ungrouped command has Group/Sub = %q/%q, want both empty", ping.Group, ping.Sub)
	}

	for _, query := range []string{"reminder", "add", "list"} {
		if got, found := r.Lookup(query); found {
			t.Errorf("Lookup(%q) resolved to %q; Lookup must stay flat", query, got.Name)
		}
	}
}

func TestLookupIsCaseInsensitive(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Command{Name: "HealthCheck", Aliases: []string{"HC"}, Handler: noopHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	queries := []string{
		"HealthCheck",
		"healthcheck",
		"HEALTHCHECK",
		"hEaLtHcHeCk",
		"HC",
		"hc",
		"hC",
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			cmd, ok := r.Lookup(query)
			if !ok {
				t.Fatalf("Lookup(%q) found nothing", query)
			}
			if cmd.Name != "HealthCheck" {
				t.Errorf("Name = %q, want %q", cmd.Name, "HealthCheck")
			}
		})
	}
}

func TestLookupMiss(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Command{Name: "ping", Aliases: []string{"p"}, Handler: noopHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, query := range []string{"", "pi", "pingg", "ping-", "q", "healthcheck"} {
		t.Run(query, func(t *testing.T) {
			cmd, ok := r.Lookup(query)
			if ok {
				t.Fatalf("Lookup(%q) resolved to %q, want no match", query, cmd.Name)
			}
			if cmd.Name != "" {
				t.Errorf("Name = %q, want the zero Command on a miss", cmd.Name)
			}
		})
	}
}

func TestAllIsOrderedByName(t *testing.T) {
	r := NewRegistry()

	for _, name := range []string{"triples", "doubles", "number", "healthcheck", "quads"} {
		if err := r.Register(Command{Name: name, Handler: noopHandler}); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	want := []string{"doubles", "healthcheck", "number", "quads", "triples"}

	for attempt := range 2 {
		all := r.All()
		if len(all) != len(want) {
			t.Fatalf("attempt %d: All() has %d commands, want %d", attempt, len(all), len(want))
		}
		for i := range all {
			if all[i].Name != want[i] {
				t.Errorf("attempt %d: All()[%d].Name = %q, want %q", attempt, i, all[i].Name, want[i])
			}
		}
	}
}

func TestAllOnEmptyRegistry(t *testing.T) {
	if got := NewRegistry().All(); len(got) != 0 {
		t.Errorf("All() = %v, want empty", got)
	}
}

func TestClearanceIsStoredButNotEnforced(t *testing.T) {
	r := NewRegistry()

	clearances := []pb.Clearance{
		pb.Clearance_CLEARANCE_UNSPECIFIED,
		pb.Clearance_CLEARANCE_REGISTERED,
		pb.Clearance_CLEARANCE_OWNER,
	}

	for _, clearance := range clearances {
		name := "cmd" + clearance.String()
		if err := r.Register(Command{Name: name, Clearance: clearance, Handler: noopHandler}); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}

		cmd, ok := r.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) found nothing", name)
		}
		if cmd.Clearance != clearance {
			t.Errorf("Clearance = %v, want %v", cmd.Clearance, clearance)
		}
		if _, err := cmd.Handler(context.Background(), &Invocation{}); err != nil {
			t.Errorf("handler for clearance %v returned %v; nothing should be enforced yet", clearance, err)
		}
	}
}
