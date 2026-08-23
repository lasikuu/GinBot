package discord

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// startedAt approximates the process start. Package variables are initialised
// before main runs, so this is a few milliseconds early at worst.
var startedAt = time.Now()

// unknownVersion is reported when the binary carries no version stamp, which is
// the normal case for a `go build` from a working tree.
const unknownVersion = "unknown"

// shortRevisionLength is how much of a VCS revision is shown. Seven hex
// characters is the conventional abbreviated commit.
const shortRevisionLength = 7

// unsetValue is shown for a preference the user has never set.
const unsetValue = "not set"

// defaultTimezone is what the bot assumes for a user who has not set one.
// Reporting a default beats reporting nothing, and beats failing.
const defaultTimezone = "UTC"

func pingCommand() command.Command {
	return command.Command{
		Name:        "ping",
		Description: "Check that the bot is responding, and how quickly",
		Handler:     ping,
	}
}

// ping measures the round trip entirely on this side: the clock is read before
// the call and after the response, so no clock agreement with the server is
// needed. A server-side timestamp diffed against a client one would report
// clock skew as latency.
//
// The Discord gateway heartbeat used to be reported alongside it and was
// removed, because reading it is a data race that cannot be fixed from here.
// discordgo's Session.HeartbeatLatency reads LastHeartbeatAck and
// LastHeartbeatSent under no lock at all, while LastHeartbeatAck is written
// under Session's own RWMutex and LastHeartbeatSent under the unexported
// wsMutex, from two other goroutines. ping runs on a discordgo dispatch
// goroutine, so both reads race. Taking discordSession.RLock() would only
// serialise the first of them — wsMutex is not reachable from this package — so
// the race would remain, just less visibly. The gRPC round trip is what the
// requirement asks for, and it is measured correctly.
func ping(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	start := time.Now()

	resp, err := client.UtilityServiceClient.Ping(ctx, &emptypb.Empty{})
	if err != nil {
		log.Z.Error("failed to call Ping.", zap.Error(err))
		return nil, err
	}

	roundTrip := time.Since(start)

	content := fmt.Sprintf("%s — gRPC round trip %s", resp.GetMessage(), formatLatency(roundTrip))

	return &command.Response{Content: content}, nil
}

func formatLatency(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

func infoCommand() command.Command {
	return command.Command{
		Name:        "info",
		Description: "Show the bot's version, environment and uptime",
		Handler:     info,
	}
}

func info(_ context.Context, _ *command.Invocation) (*command.Response, error) {
	content := fmt.Sprintf(
		"GinBot %s\nEnvironment: %s\nUptime: %s\nCommands: %d",
		botVersion(),
		config.AppEnvironment,
		time.Since(startedAt).Round(time.Second),
		len(commandRegistry.All()),
	)

	return &command.Response{Content: content}, nil
}

// botVersion reads what the Go toolchain already stamps into the binary. There
// is no build system here to inject a version, and inventing one for a single
// command would be out of proportion.
func botVersion() string {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownVersion
	}

	for _, setting := range buildInfo.Settings {
		if setting.Key != "vcs.revision" || setting.Value == "" {
			continue
		}
		if len(setting.Value) > shortRevisionLength {
			return setting.Value[:shortRevisionLength]
		}
		return setting.Value
	}

	// Set for a module built as a dependency; "(devel)" for a working tree.
	if buildInfo.Main.Version != "" {
		return buildInfo.Main.Version
	}

	return unknownVersion
}

func helpCommand() command.Command {
	return command.Command{
		Name:        "help",
		Description: "List the commands, or explain one of them",
		Args: []command.Arg{
			{
				Name:        "command",
				Description: "Command to explain. Omit to list them all",
				Type:        command.ArgString,
			},
		},
		Handler: help,
	}
}

// help is answered from the local registry rather than by an RPC, and
// UtilityService has no Help method for that reason.
//
// pkg/command holds the only copy of command names, aliases, descriptions and
// argument specs, and it lives in this process. A server-side Help would have
// to be shipped that registry to say anything, and would then be a second copy
// that can disagree with the one actually dispatching commands. It would also
// be wrong for whichever client asked, since the registries differ per
// platform.
func help(_ context.Context, inv *command.Invocation) (*command.Response, error) {
	if name := inv.String("command"); name != "" {
		cmd, ok := commandRegistry.Lookup(name)
		if !ok {
			// InvalidArgument rather than NotFound: the caller supplied a bad
			// argument, and errorMessage only passes InvalidArgument and
			// FailedPrecondition through verbatim. Under NotFound the user is told
			// "Not found." and never learns which command was unknown.
			return nil, status.Errorf(codes.InvalidArgument, "there is no %q command", name)
		}

		return &command.Response{Content: describeCommand(cmd), Ephemeral: true}, nil
	}

	return &command.Response{Content: listCommands(), Ephemeral: true}, nil
}

// listCommands renders the catalogue. Registry.All is ordered by name, so the
// listing is stable between invocations.
func listCommands() string {
	var b strings.Builder
	b.WriteString("**Commands**\n")

	for _, cmd := range commandRegistry.All() {
		fmt.Fprintf(&b, "`%s` — %s\n", cmd.Name, cmd.Description)
	}

	b.WriteString("\nUse `help <command>` for arguments and aliases.")

	return b.String()
}

// describeCommand renders one command's usage, arguments and aliases.
func describeCommand(cmd command.Command) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**%s** — %s\n", cmd.Name, cmd.Description)
	fmt.Fprintf(&b, "Usage: `%s`\n", usageLine(cmd))
	// A grouped command is reachable both ways, and the slash surface exposes
	// ONLY the grouped form, so listing just the flat one would hide it.
	if grouped := groupedUsageLine(cmd); grouped != "" {
		fmt.Fprintf(&b, "Also: `%s` (slash: `/%s %s`)\n", grouped, cmd.Group, cmd.Sub)
	}

	if len(cmd.Args) > 0 {
		b.WriteString("\n**Arguments**\n")
		for _, arg := range cmd.Args {
			requirement := "optional"
			if arg.Required {
				requirement = "required"
			}
			fmt.Fprintf(&b, "`%s` (%s, %s) — %s\n", arg.Name, argTypeName(arg.Type), requirement, arg.Description)
		}
	}

	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(&b, "\nAliases: %s\n", strings.Join(cmd.Aliases, ", "))
	}

	// Clearance is enforced by the server, not by the registry, so this is
	// advisory: it says what will be refused rather than what is refused here.
	if cmd.Clearance != pb.Clearance_CLEARANCE_UNSPECIFIED {
		fmt.Fprintf(&b, "\nRequires %s clearance.\n", clearanceName(cmd.Clearance))
	}

	return b.String()
}

// usageLine renders the argument shape: angle brackets for required, square
// for optional, which is the convention every CLI help uses.
func usageLine(cmd command.Command) string {
	return usageLineFor(cmd, cmd.Name)
}

// groupedUsageLine renders the grouped form, "reminder add <when> …", for a
// command that has one. Help would otherwise advertise only the flat name while
// the slash surface exposes only the grouped one, so half of what the bot
// accepts would be undiscoverable from help.
func groupedUsageLine(cmd command.Command) string {
	if cmd.Group == "" {
		return ""
	}

	return usageLineFor(cmd, cmd.Group+" "+cmd.Sub)
}

// usageLineFor renders the argument shape after a given invocation prefix:
// angle brackets for required, square for optional, which is the convention
// every CLI help uses.
func usageLineFor(cmd command.Command, prefix string) string {
	parts := make([]string, 0, 1+len(cmd.Args))
	parts = append(parts, prefix)

	for _, arg := range cmd.Args {
		if arg.Required {
			parts = append(parts, "<"+arg.Name+">")
			continue
		}
		parts = append(parts, "["+arg.Name+"]")
	}

	return strings.Join(parts, " ")
}

func argTypeName(argType command.ArgType) string {
	switch argType {
	case command.ArgInt:
		return "number"
	case command.ArgBool:
		return "true/false"
	default:
		return "text"
	}
}

// clearanceName turns CLEARANCE_MODERATOR into "moderator", which is what a
// user should be shown.
func clearanceName(clearance pb.Clearance) string {
	return strings.ToLower(strings.TrimPrefix(clearance.String(), "CLEARANCE_"))
}

func registerCommand() command.Command {
	return command.Command{
		Name:        "register",
		Description: "Create your GinBot account",
		Handler:     register,
	}
}

// register takes no arguments on purpose. The platform identity goes over gRPC
// metadata, and the display name is read from the invoking Discord user, so
// there is nothing for the caller to type — and nothing for them to falsify.
func register(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	user, ok := invokerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "cannot identify the invoking user")
	}

	username := user.Username
	req := pb.RegisterReq_builder{
		Username: &username,
	}.Build()

	if _, err := client.UserServiceClient.Register(ctx, req); err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return &command.Response{Content: "You are already registered.", Ephemeral: true}, nil
		}
		log.Z.Error("failed to call Register.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   "Registered. Set your timezone with `timezone <IANA zone>`.",
		Ephemeral: true,
	}, nil
}

func userInfoCommand() command.Command {
	return command.Command{
		Name:        "userinfo",
		Description: "Show your GinBot account",
		Clearance:   pb.Clearance_CLEARANCE_REGISTERED,
		Handler:     userInfo,
	}
}

// userInfo is self-only, and takes no target argument.
//
// Of the two options the server supports — self-only, or another user's row at
// CLEARANCE_MODERATOR — self-only is what Discord can actually express.
// GetUser identifies a user by their GinBot account UUID, and a Discord caller
// only ever has a snowflake or a mention; there is no RPC that maps one to the
// other, so a target argument would have nothing to send. The server still
// enforces the moderator floor for a lookup of someone else, for whatever
// client can name one.
func userInfo(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	// An empty request means "me": the server takes the caller from metadata.
	resp, err := client.UserServiceClient.GetUser(ctx, pb.GetUserReq_builder{}.Build())
	if err != nil {
		log.Z.Error("failed to call GetUser.", zap.Error(err))
		return nil, err
	}

	return &command.Response{Content: formatUserInfo(resp.GetUser()), Ephemeral: true}, nil
}

// formatUserInfo renders the /userinfo view. Pure so it can be unit-tested
// without a gRPC client.
//
// The account line used to be a whole-day age computed here. It is now a
// relative Discord timestamp tag on the creation instant, which is strictly more
// informative — the client renders "3 months ago" and hovering shows the exact
// date, in the viewer's own zone and locale, neither of which this process knows.
func formatUserInfo(user *pb.User) string {
	timezone := user.GetTimezone()
	if timezone == "" {
		timezone = defaultTimezone + " (default)"
	}

	locale := user.GetLocale()
	if locale == "" {
		locale = unsetValue
	}

	// A missing created_at would otherwise render as <t:0:R>, i.e. "56 years
	// ago", which reads as data rather than as absence.
	created := unsetValue
	if user.HasCreatedAt() {
		created = timestampTag(user.GetCreatedAt().AsTime(), timestampRelative)
	}

	return fmt.Sprintf(
		"**%s**\nClearance: %s\nLocale: %s\nTimezone: %s\nAccount created: %s",
		user.GetUsername(),
		clearanceName(user.GetClearance()),
		locale,
		timezone,
		created,
	)
}

func localeCommand() command.Command {
	return command.Command{
		Name:        "locale",
		Description: "Set your language. Supported: en, fi, ja",
		Args: []command.Arg{
			{
				Name:        "locale",
				Description: "Language code: en, fi or ja",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   setLocale,
	}
}

func setLocale(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	locale := inv.String("locale")

	req := pb.SetLocaleReq_builder{
		Locale: &locale,
	}.Build()

	if _, err := client.UserServiceClient.SetLocale(ctx, req); err != nil {
		log.Z.Error("failed to call SetLocale.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   fmt.Sprintf("Locale set to %s.", locale),
		Ephemeral: true,
	}, nil
}

func timezoneCommand() command.Command {
	return command.Command{
		Name:        "timezone",
		Description: "Set your timezone, e.g. Europe/Helsinki",
		Args: []command.Arg{
			{
				Name:        "timezone",
				Description: "IANA timezone name, e.g. Europe/Helsinki",
				Type:        command.ArgString,
				Required:    true,
			},
		},
		Clearance: pb.Clearance_CLEARANCE_REGISTERED,
		Handler:   setTimezone,
	}
}

func setTimezone(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
	timezone := inv.String("timezone")

	req := pb.SetTimezoneReq_builder{
		Timezone: &timezone,
	}.Build()

	if _, err := client.UserServiceClient.SetTimezone(ctx, req); err != nil {
		log.Z.Error("failed to call SetTimezone.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   fmt.Sprintf("Timezone set to %s.", timezone),
		Ephemeral: true,
	}, nil
}
