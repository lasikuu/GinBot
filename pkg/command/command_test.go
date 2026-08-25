package command

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// These assertions exist to fail at compile time if the exported surface drifts
// from the agreed contract. A signature change here breaks every platform client
// at once, so it is worth pinning explicitly rather than only implicitly through
// the behavioural tests below.
var (
	_ Handler = func(ctx context.Context, inv *Invocation) (*Response, error) { return nil, nil }

	_ func(string, []string) (string, []string, bool) = ParseChat
	_ func(Command, []string) (*Invocation, error)    = Bind
	_ func() *Registry                                = NewRegistry

	_ func(*Registry, Command) error          = (*Registry).Register
	_ func(*Registry, string) (Command, bool) = (*Registry).Lookup
	_ func(*Registry) []Command               = (*Registry).All
	_ func(*Registry) []string                = (*Registry).Groups
	_ func(*Invocation, string) string        = (*Invocation).String
	_ func(*Invocation, string) int64         = (*Invocation).Int
	_ func(*Invocation, string) bool          = (*Invocation).Bool
	_ func(*Invocation, string) bool          = (*Invocation).Has

	// Lookup keeps its flat signature above; ResolveChat is the group-aware one,
	// and additionally returns the arguments left after the command name is
	// consumed.
	_ func(*Registry, string, []string) (Command, []string, bool) = (*Registry).ResolveChat

	_ = Response{Content: "", Ephemeral: false, ReRollID: "", File: nil}
	// A response file carries BYTES, not a URL. The platform CDN URL a trigger
	// was created from has expired long before the trigger fires, so a []byte
	// here is the contract and swapping it for a string would silently reopen
	// that hole.
	_ = ResponseFile{Name: "", MIMEType: "", Content: []byte(nil)}
	_ = Arg{Name: "", Description: "", Type: ArgString, Required: false, Default: nil}
	_ = Invocation{Args: map[string]any{}}
	_ = Command{
		Name:        "",
		Aliases:     nil,
		Description: "",
		Args:        nil,
		Group:       "",
		Sub:         "",
		Clearance:   pb.Clearance_CLEARANCE_UNSPECIFIED,
		Handler:     nil,
	}
)

// noopHandler is for tests that only care about registration or resolution.
func noopHandler(ctx context.Context, inv *Invocation) (*Response, error) {
	return &Response{}, nil
}

func TestArgTypeConstants(t *testing.T) {
	// The zero value carries meaning: an Arg written without an explicit Type
	// is a string argument, which is what most commands want.
	if ArgString != 0 {
		t.Errorf("ArgString = %d, want 0 so that a zero-valued Arg is a string", ArgString)
	}

	types := map[ArgType]string{ArgString: "ArgString", ArgInt: "ArgInt", ArgBool: "ArgBool"}
	if len(types) != 3 {
		t.Errorf("ArgString, ArgInt and ArgBool must be distinct, got %v", types)
	}
}

// pkg/command is the platform-neutral layer that both the Discord and the Matrix
// client depend on. A platform import here would make the package unusable from
// the other client and invert the dependency direction on purpose-built code.
func TestPackageStaysPlatformNeutral(t *testing.T) {
	forbidden := []string{
		"github.com/bwmarrin/discordgo",
		"maunium.net/go/mautrix",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	fset := token.NewFileSet()
	inspected := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		inspected++

		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unparsable import literal %s: %v", name, imp.Path.Value, err)
			}
			for _, bad := range forbidden {
				if path == bad || strings.HasPrefix(path, bad+"/") {
					t.Errorf("%s imports %s; this package must stay platform-neutral", name, path)
				}
			}
		}
	}

	if inspected == 0 {
		t.Fatal("no non-test Go files in the package; the check would pass vacuously")
	}
}

// The whole chat path minus the platform glue: parse, resolve, bind, run.
func TestChatRoundTrip(t *testing.T) {
	r := NewRegistry()

	err := r.Register(Command{
		Name:        "say",
		Aliases:     []string{"echo"},
		Description: "Repeat a message",
		Args: []Arg{
			{Name: "message", Description: "What to repeat", Type: ArgString, Required: true},
			{Name: "times", Description: "How many times", Type: ArgInt, Default: int64(1)},
		},
		Handler: func(ctx context.Context, inv *Invocation) (*Response, error) {
			return &Response{Content: strings.Repeat(inv.String("message"), int(inv.Int("times")))}, nil
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"by name", `?say "hi there" 2`, "hi therehi there"},
		{"by alias", `??echo "hi there" 3`, "hi therehi therehi there"},
		{"default applies to the omitted argument", `?say hello`, "hello"},
		{"resolved case-insensitively", `?SAY hello`, "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, raw, ok := ParseChat(tt.content, []string{"?", "??"})
			if !ok {
				t.Fatalf("ParseChat(%q) did not recognise a command", tt.content)
			}

			cmd, found := r.Lookup(name)
			if !found {
				t.Fatalf("Lookup(%q) found nothing", name)
			}

			inv, err := Bind(cmd, raw)
			if err != nil {
				t.Fatalf("Bind(%q): %v", raw, err)
			}

			resp, err := cmd.Handler(context.Background(), inv)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if resp.Content != tt.want {
				t.Errorf("Content = %q, want %q", resp.Content, tt.want)
			}
		})
	}
}

// An unknown chat command must be silently ignored, which at this layer means
// the registry simply does not resolve it and the caller has nothing to send.
func TestUnknownChatCommandDoesNotResolve(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Command{Name: "ping", Handler: noopHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, content := range []string{"?nosuchcommand", "?pin", "?pingg", "?ping2"} {
		t.Run(content, func(t *testing.T) {
			name, _, ok := ParseChat(content, []string{"?"})
			if !ok {
				t.Fatalf("ParseChat(%q) did not recognise a command", content)
			}
			if cmd, found := r.Lookup(name); found {
				t.Errorf("Lookup(%q) resolved to %q, want no match", name, cmd.Name)
			}
		})
	}
}
