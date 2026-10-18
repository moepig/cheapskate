package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	"cheapskate/internal/wire"
)

var Usage = `cheapskate-cli — cheapskate configuration CLI

Usage:
  cheapskate-cli [-config PATH] [-context NAME] [-table TABLE] [-output text|json] <command> [arguments]

Commands:
  config set-context NAME --table TABLE [--profile PROFILE] [--region REGION] [--timezone ZONE]
  config use-context NAME
  config get-contexts
  config current-context
  list
  show --group NAME
  schedule --group NAME -start CRON -stop CRON
  override --group NAME running|stopped|disabled [-for DURATION]
  clear-override --group NAME
  ecs-max-count --group NAME COUNT
  clear-ecs-max-count --group NAME
  remove --group NAME

Short options:
  -c NAME        Alias for -context NAME
  -o text|json   Alias for -output text|json
  -g NAME        Alias for --group NAME

Resources belong to a group through the fixed tag cheapskate:group=<group>.
Contexts and the current selection are saved in the user config directory under cheapskate/config.json.
Use -config or CHEAPSKATE_CONFIG to select another config file.
The table name comes from -table, the selected context, CHEAPSKATE_TABLE, or STATE_TABLE_NAME.
`

type commandError struct {
	err      error
	code     int
	reported bool
}

func (e *commandError) Error() string { return e.err.Error() }
func (e *commandError) Unwrap() error { return e.err }

func Main(args []string) {
	if code := Execute(args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func Execute(args []string, stdout, stderr io.Writer) int {
	err := run(args, stdout, stderr)
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stderr, Usage)
		return 0
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		if !commandErr.reported {
			fmt.Fprintln(stderr, commandErr.err)
		}
		return commandErr.code
	}
	fmt.Fprintln(stderr, err)
	return 1
}

func Run(args []string, out io.Writer) error {
	return run(args, out, io.Discard)
}

func run(args []string, stdout, stderr io.Writer) error {
	global := newFlagSet("cheapskate-cli")
	configFile := global.String("config", "", "CLI config file path (or CHEAPSKATE_CONFIG)")
	contextName := global.String("context", "", "context for this invocation")
	global.StringVar(contextName, "c", "", "alias for -context")
	table := global.String("table", "", "DynamoDB state table name")
	output := global.String("output", "text", "output format: text or json")
	global.StringVar(output, "o", "text", "alias for -output")
	if err := global.Parse(args); err != nil {
		return err
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("invalid -output %q: want text or json", *output)
	}
	rest := global.Args()
	if len(rest) == 0 {
		return fmt.Errorf("missing command (see: cheapskate-cli -h)")
	}
	path, err := cliConfigPath(*configFile)
	if err != nil {
		return err
	}
	saved, err := loadCLIConfig(path)
	if err != nil {
		return err
	}
	command, commandArgs := rest[0], rest[1:]
	if command == "config" {
		return cmdConfig(commandArgs, stdout, *output, path, saved)
	}
	selected, err := saved.context(firstNonEmpty(*contextName, saved.CurrentContext))
	if err != nil {
		return err
	}
	tableName := firstNonEmpty(*table, selected.Table, os.Getenv("CHEAPSKATE_TABLE"), os.Getenv("STATE_TABLE_NAME"))
	if tableName == "" {
		return fmt.Errorf("state table not set (use -table, a context, CHEAPSKATE_TABLE, or STATE_TABLE_NAME)")
	}
	timezone := firstNonEmpty(selected.Timezone, os.Getenv("DEFAULT_TIMEZONE"), "UTC")
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}

	ctx := context.Background()
	var awsOptions []func(*config.LoadOptions) error
	if selected.Profile != "" {
		awsOptions = append(awsOptions, config.WithSharedConfigProfile(selected.Profile))
	}
	if selected.Region != "" {
		awsOptions = append(awsOptions, config.WithRegion(selected.Region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, awsOptions...)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	service := groups.New(
		state.New(dynamodb.NewFromConfig(cfg), tableName),
		wire.Discoverer(cfg, nil),
		wire.Describers(cfg),
		location,
	)
	switch command {
	case "list":
		return cmdList(ctx, service, commandArgs, stdout, stderr, *output, time.Now())
	case "show":
		return cmdShow(ctx, service, commandArgs, stdout, stderr, *output, time.Now())
	case "schedule":
		return cmdSchedule(ctx, service, commandArgs, stdout, *output, time.Now())
	case "override":
		return cmdOverride(ctx, service, commandArgs, stdout, *output, time.Now())
	case "clear-override":
		return cmdClearOverride(ctx, service, commandArgs, stdout, *output, time.Now())
	case "ecs-max-count":
		return cmdEcsMaxCount(ctx, service, commandArgs, stdout, *output, time.Now())
	case "clear-ecs-max-count":
		return cmdClearEcsMaxCount(ctx, service, commandArgs, stdout, *output, time.Now())
	case "remove":
		return cmdRemove(ctx, service, commandArgs, stdout, *output)
	default:
		return fmt.Errorf("unknown command %q (see: cheapskate-cli -h)", command)
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// 指定された FlagSet にグループ名の通常オプションと省略形を登録し、共通の値を返す。
func groupNameFlag(fs *flag.FlagSet) *string {
	group := fs.String("group", "", "group name")
	fs.StringVar(group, "g", "", "alias for --group")
	return group
}

func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

type groupJSON struct {
	Name              string         `json:"name"`
	StartCron         string         `json:"start_cron,omitempty"`
	StopCron          string         `json:"stop_cron,omitempty"`
	Override          model.Override `json:"override,omitempty"`
	OverrideExpiresAt string         `json:"override_expires_at,omitempty"`
	EcsMaxCount       int32          `json:"ecs_max_count,omitempty"`
}

func jsonGroup(group model.GroupSpec) groupJSON {
	result := groupJSON{Name: group.Name, StartCron: group.StartCron, StopCron: group.StopCron, Override: group.Override, EcsMaxCount: group.EcsMaxCount}
	if group.OverrideExpiresAt != 0 {
		result.OverrideExpiresAt = time.Unix(group.OverrideExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	return result
}

type configErrorJSON struct {
	Group string `json:"group"`
	Error string `json:"error"`
}

type listOutput struct {
	Groups []groupJSON       `json:"groups"`
	Errors []configErrorJSON `json:"errors"`
}

func cmdList(ctx context.Context, service *groups.Service, args []string, stdout, stderr io.Writer, output string, now time.Time) error {
	if len(args) != 0 {
		return fmt.Errorf("list takes no arguments")
	}
	rows, err := service.List(ctx, now)
	if err != nil {
		return err
	}
	result := listOutput{Groups: []groupJSON{}, Errors: []configErrorJSON{}}
	for _, row := range rows {
		if row.ConfigErr != nil {
			result.Errors = append(result.Errors, configErrorJSON{Group: row.Name, Error: row.ConfigErr.Error()})
			continue
		}
		result.Groups = append(result.Groups, jsonGroup(row.Group))
	}
	if output == "json" {
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else {
		for _, group := range result.Groups {
			fmt.Fprintln(stdout, textGroup(group))
		}
		for _, configErr := range result.Errors {
			fmt.Fprintf(stderr, "%s: %s\n", configErr.Group, configErr.Error)
		}
	}
	if len(result.Errors) > 0 {
		return &commandError{err: groups.ErrInvalidConfig, code: 2, reported: true}
	}
	return nil
}

type showResource struct {
	Type      model.ResourceType `json:"type"`
	Ref       string             `json:"ref"`
	ARN       string             `json:"arn"`
	Config    any                `json:"config,omitempty"`
	Live      *model.Observation `json:"live,omitempty"`
	LiveError string             `json:"live_error,omitempty"`
}

type showOutput struct {
	Group     groupJSON      `json:"group"`
	Resources []showResource `json:"resources"`
}

func cmdShow(ctx context.Context, service *groups.Service, args []string, stdout, stderr io.Writer, output string, now time.Time) error {
	fs := newFlagSet("show")
	group := groupNameFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *group == "" {
		return fmt.Errorf("--group is required")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("show takes no positional arguments")
	}
	detail, err := service.Show(ctx, *group, now)
	if err != nil {
		if errors.Is(err, groups.ErrInvalidConfig) {
			if output == "json" {
				if writeErr := writeJSON(stdout, map[string]any{"error": map[string]string{"group": *group, "message": err.Error()}}); writeErr != nil {
					return writeErr
				}
			} else {
				fmt.Fprintln(stderr, err)
			}
			return &commandError{err: err, code: 2, reported: true}
		}
		return err
	}
	result := showOutput{Group: jsonGroup(detail.Group), Resources: []showResource{}}
	for _, row := range detail.Resources {
		resource := showResource{Type: row.Resource.Type, Ref: row.Resource.Ref, ARN: row.Resource.ARN, Config: resourceConfig(row.Resource), Live: row.Live}
		if row.LiveErr != nil {
			resource.LiveError = row.LiveErr.Error()
		}
		result.Resources = append(result.Resources, resource)
	}
	if output == "json" {
		return writeJSON(stdout, result)
	}
	fmt.Fprintln(stdout, textGroup(result.Group))
	for _, resource := range result.Resources {
		state := "unknown"
		if resource.Live != nil {
			state = string(resource.Live.State)
		}
		if resource.LiveError != "" {
			state = "error: " + resource.LiveError
		}
		fmt.Fprintf(stdout, "%s %s %s\n", resource.Type, resource.Ref, state)
	}
	return nil
}

func cmdSchedule(ctx context.Context, service *groups.Service, args []string, stdout io.Writer, output string, now time.Time) error {
	fs := newFlagSet("schedule")
	group := groupNameFlag(fs)
	start := fs.String("start", "", "5-field start cron")
	stop := fs.String("stop", "", "5-field stop cron")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *group == "" {
		return fmt.Errorf("--group is required")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("schedule takes no positional arguments")
	}
	next, err := service.Schedule(ctx, *group, model.ScheduleSpec{StartCron: *start, StopCron: *stop}, now)
	if err != nil {
		return classify(err)
	}
	return writeMutation(stdout, output, "schedule", next)
}

func cmdOverride(ctx context.Context, service *groups.Service, args []string, stdout io.Writer, output string, now time.Time) error {
	fs := newFlagSet("override")
	group := groupNameFlag(fs)
	duration := fs.Duration("for", 0, "override duration")
	positionals, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if *group == "" {
		return fmt.Errorf("--group is required")
	}
	if len(positionals) != 1 {
		return fmt.Errorf("override requires exactly one of running|stopped|disabled")
	}
	durationSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "for" {
			durationSet = true
		}
	})
	if durationSet && *duration <= 0 {
		return fmt.Errorf("-for must be a positive duration")
	}
	override, err := model.ParseOverride(positionals[0])
	if err != nil {
		return err
	}
	next, err := service.Override(ctx, *group, override, *duration, now)
	if err != nil {
		return classify(err)
	}
	return writeMutation(stdout, output, "override", next)
}

func cmdClearOverride(ctx context.Context, service *groups.Service, args []string, stdout io.Writer, output string, now time.Time) error {
	group, err := groupFlag("clear-override", args)
	if err != nil {
		return err
	}
	next, err := service.ClearOverride(ctx, group, now)
	if err != nil {
		return classify(err)
	}
	return writeMutation(stdout, output, "clear-override", next)
}

func cmdEcsMaxCount(ctx context.Context, service *groups.Service, args []string, stdout io.Writer, output string, now time.Time) error {
	fs := newFlagSet("ecs-max-count")
	group := groupNameFlag(fs)
	positionals, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if *group == "" {
		return fmt.Errorf("--group is required")
	}
	if len(positionals) != 1 {
		return fmt.Errorf("ecs-max-count requires exactly one count")
	}
	maximum, err := strconv.ParseInt(positionals[0], 10, 32)
	if err != nil || maximum <= 0 {
		return fmt.Errorf("ecs_max_count must be an integer between 1 and 2147483647")
	}
	next, err := service.SetEcsMaxCount(ctx, *group, int32(maximum), now)
	if err != nil {
		return classify(err)
	}
	return writeMutation(stdout, output, "ecs-max-count", next)
}

func cmdClearEcsMaxCount(ctx context.Context, service *groups.Service, args []string, stdout io.Writer, output string, now time.Time) error {
	group, err := groupFlag("clear-ecs-max-count", args)
	if err != nil {
		return err
	}
	next, err := service.ClearEcsMaxCount(ctx, group, now)
	if err != nil {
		return classify(err)
	}
	return writeMutation(stdout, output, "clear-ecs-max-count", next)
}

func cmdRemove(ctx context.Context, service *groups.Service, args []string, stdout io.Writer, output string) error {
	group, err := groupFlag("remove", args)
	if err != nil {
		return err
	}
	if err := service.Remove(ctx, group); err != nil {
		return classify(err)
	}
	if output == "json" {
		return writeJSON(stdout, map[string]string{"command": "remove", "group": group})
	}
	fmt.Fprintf(stdout, "removed %s\n", group)
	return nil
}

func groupFlag(command string, args []string) (string, error) {
	fs := newFlagSet(command)
	group := groupNameFlag(fs)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *group == "" {
		return "", fmt.Errorf("--group is required")
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("%s takes no positional arguments", command)
	}
	return *group, nil
}

func classify(err error) error {
	if errors.Is(err, state.ErrConflict) || errors.Is(err, state.ErrInvalidGroup) || errors.Is(err, groups.ErrInvalidConfig) {
		return &commandError{err: err, code: 2}
	}
	return err
}

func writeMutation(out io.Writer, output, command string, group model.GroupSpec) error {
	if output == "json" {
		return writeJSON(out, struct {
			Command string    `json:"command"`
			Group   groupJSON `json:"group"`
		}{Command: command, Group: jsonGroup(group)})
	}
	fmt.Fprintln(out, textGroup(jsonGroup(group)))
	return nil
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func textGroup(group groupJSON) string {
	parts := []string{group.Name}
	if group.EcsMaxCount != 0 {
		parts = append(parts, fmt.Sprintf("ecs_max_count=%d", group.EcsMaxCount))
	}
	if group.StartCron != "" {
		parts = append(parts, "start="+group.StartCron, "stop="+group.StopCron)
	}
	if group.Override != "" {
		parts = append(parts, "override="+string(group.Override))
	}
	if group.OverrideExpiresAt != "" {
		parts = append(parts, "expires="+group.OverrideExpiresAt)
	}
	return strings.Join(parts, " ")
}

func resourceConfig(resource model.Resource) any {
	config := resource.Config()
	if len(config) == 0 {
		return nil
	}
	result := make(map[string]string, len(config))
	for _, value := range config {
		result[value.Name] = value.Value
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
