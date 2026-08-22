package command

import (
	"context"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
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
			// Lookup is case-insensitive, so a differently cased duplicate would
			// make one of the two commands permanently unreachable.
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

// Registration happens once at startup, so a declaration that no dispatch path
// could ever satisfy is worth refusing loudly instead of shipping a command that
// fails for every user.
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
			// Chat arguments bind positionally, so the required one could never
			// be reached without also supplying the optional one.
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

// A rejected Register must not leave half of the command behind: the first alias
// below is free, the second collides. If the first one were kept, the registry
// would hold an alias pointing at a command that was never registered.
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
			// The stored name keeps the case it was registered with; only
			// matching is case-insensitive.
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

	// Whitespace-padded queries are deliberately absent: the contract says
	// nothing about trimming, and ParseChat splits on whitespace, so a padded
	// name cannot reach Lookup from the chat path.
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

// All() feeds the Discord slash-command registration, so its order must be
// deterministic; otherwise every restart looks like a command change.
func TestAllIsOrderedByName(t *testing.T) {
	r := NewRegistry()

	// Registered deliberately out of order. All names are lower case so the
	// expectation holds under both byte-wise and case-insensitive sorting.
	for _, name := range []string{"triples", "doubles", "number", "healthcheck", "quads"} {
		if err := r.Register(Command{Name: name, Handler: noopHandler}); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	want := []string{"doubles", "healthcheck", "number", "quads", "triples"}

	// Called twice: the order must be stable across calls, not just sorted once.
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

// Clearance is carried but deliberately not enforced in this phase: an owner-only
// command must still resolve and run so that the field can be wired up later
// without changing the registry contract.
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
