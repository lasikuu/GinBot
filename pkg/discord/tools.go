package discord

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/command"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/log"
	"go.uber.org/zap"
)

// toolsCallTimeout bounds every outgoing RPC in this file; the handler context
// is rooted at context.Background with no deadline of its own.
const toolsCallTimeout = 20 * time.Second

func boundedToolsCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, toolsCallTimeout)
}

var startedAt = time.Now()

const unknownVersion = "unknown"

const shortRevisionLength = 7

const unsetValue = "not set"

const defaultTimezone = "UTC"

func pingCommand() command.Command {
	return command.Command{
		Name:        "ping",
		Description: "Check that the bot is responding, and how quickly",
		Handler:     ping,
	}
}

// ping measures the round trip entirely on this side, so no clock agreement
// with the server is needed.
func ping(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	start := time.Now()

	callCtx, cancel := boundedToolsCall(ctx)
	defer cancel()

	resp, err := clientsFrom(ctx).Utility.Ping(callCtx, connect.NewRequest(pb.PingReq_builder{}.Build()))
	if err != nil {
		log.Z.Error("failed to call Ping.", zap.Error(err))
		return nil, err
	}

	roundTrip := time.Since(start)

	content := fmt.Sprintf("%s — round trip %s", resp.Msg.GetMessage(), formatLatency(roundTrip))

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

// botVersion reads what the Go toolchain stamps into the binary.
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

// help is answered from the local registry rather than by an RPC, since
// pkg/command holds the only copy of command metadata and it lives in-process.
func help(_ context.Context, inv *command.Invocation) (*command.Response, error) {
	if name := inv.String("command"); name != "" {
		cmd, ok := commandRegistry.Lookup(name)
		if !ok {
			// InvalidArgument, not NotFound: errorMessage only passes InvalidArgument
			// and FailedPrecondition through verbatim, so the user sees which command.
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("there is no %q command", name))
		}

		return &command.Response{Content: describeCommand(cmd), Ephemeral: true}, nil
	}

	return &command.Response{Content: listCommands(), Ephemeral: true}, nil
}

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
	// The slash surface exposes only the grouped form, so list it too.
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

	// Advisory only: clearance is enforced by the server, not the registry.
	if cmd.Clearance != pb.Clearance_CLEARANCE_UNSPECIFIED {
		fmt.Fprintf(&b, "\nRequires %s clearance.\n", clearanceName(cmd.Clearance))
	}

	return b.String()
}

func usageLine(cmd command.Command) string {
	return usageLineFor(cmd, cmd.Name)
}

// groupedUsageLine renders the grouped form, "reminder add <when> …", for a
// command that has one.
func groupedUsageLine(cmd command.Command) string {
	if cmd.Group == "" {
		return ""
	}

	return usageLineFor(cmd, cmd.Group+" "+cmd.Sub)
}

// usageLineFor renders the argument shape after a prefix: angle brackets for
// required, square for optional.
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

// clearanceName turns CLEARANCE_MODERATOR into "moderator".
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

// register takes no arguments: identity travels in a request header and the
// display name comes from the invoking Discord user, not from caller input.
func register(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	user, ok := invokerFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("cannot identify the invoking user"))
	}

	username := user.Username
	req := pb.RegisterReq_builder{
		Username: &username,
	}.Build()

	callCtx, cancel := boundedToolsCall(ctx)
	defer cancel()

	if _, err := clientsFrom(ctx).User.Register(callCtx, connect.NewRequest(req)); err != nil {
		if connect.CodeOf(err) == connect.CodeAlreadyExists {
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

// userInfo is self-only: GetUser identifies by GinBot UUID, which a Discord
// caller cannot supply.
func userInfo(ctx context.Context, _ *command.Invocation) (*command.Response, error) {
	callCtx, cancel := boundedToolsCall(ctx)
	defer cancel()

	// An empty request means "me": the server takes the caller from a header.
	resp, err := clientsFrom(ctx).User.GetUser(callCtx, connect.NewRequest(pb.GetUserReq_builder{}.Build()))
	if err != nil {
		log.Z.Error("failed to call GetUser.", zap.Error(err))
		return nil, err
	}

	return &command.Response{Content: formatUserInfo(resp.Msg.GetUser()), Ephemeral: true}, nil
}

// formatUserInfo renders the /userinfo view.
func formatUserInfo(user *pb.User) string {
	timezone := user.GetTimezone()
	if timezone == "" {
		timezone = defaultTimezone + " (default)"
	}

	locale := user.GetLocale()
	if locale == "" {
		locale = unsetValue
	}

	// A missing created_at would render as <t:0:R> ("56 years ago").
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

	callCtx, cancel := boundedToolsCall(ctx)
	defer cancel()

	if _, err := clientsFrom(ctx).User.SetLocale(callCtx, connect.NewRequest(req)); err != nil {
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

	callCtx, cancel := boundedToolsCall(ctx)
	defer cancel()

	if _, err := clientsFrom(ctx).User.SetTimezone(callCtx, connect.NewRequest(req)); err != nil {
		log.Z.Error("failed to call SetTimezone.", zap.Error(err))
		return nil, err
	}

	return &command.Response{
		Content:   fmt.Sprintf("Timezone set to %s.", timezone),
		Ephemeral: true,
	}, nil
}
