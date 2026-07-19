// cheapskate-cli is the cheapskate configuration CLI. It manipulates the config#, override#, and status# items in the DynamoDB state table; it never calls the RDS/ECS APIs itself (the reconciler Lambda does that).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	_ "time/tzdata"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/model"
	"cheapskate/internal/ops"
	"cheapskate/internal/store"
)

const usage = `cheapskate-cli — cheapskate configuration CLI

Usage:
  cheapskate-cli [-table TABLE] <command> [arguments]

Commands:
  list                                     registered resources and their state
  show <resource-id>                       config + override + status as JSON
  pin <resource-id> running|stopped        keep the resource in a fixed state
  schedule <resource-id> [-start CRON] [-stop CRON] [-timezone TZ] [-restore-count N]
                                           cron-based start/stop
  disable <resource-id>                    keep the config but stop managing
  override <resource-id> running|stopped -for DURATION
                                           temporary override (expires via TTL)
                                           rejected if the resource is disabled: disabled is a
                                           stronger stop than override, so schedule or pin it first
  override <resource-id> -clear            remove the override now
  remove <resource-id>                     delete config, override, and status

Resource IDs:
  rds-instance#<identifier> | rds-cluster#<identifier> | ecs#<cluster>/<service>

The table name comes from -table or the CHEAPSKATE_TABLE environment variable. AWS credentials/region use the standard SDK chain.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cheapskate-cli:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("cheapskate-cli", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	table := global.String("table", os.Getenv("CHEAPSKATE_TABLE"), "DynamoDB state table name")
	if err := global.Parse(args); err != nil {
		return err
	}

	rest := global.Args()
	if len(rest) == 0 {
		global.Usage()
		return fmt.Errorf("missing command")
	}
	command, rest := rest[0], rest[1:]

	if *table == "" {
		return fmt.Errorf("state table not set (use -table or CHEAPSKATE_TABLE)")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	s := store.New(dynamodb.NewFromConfig(cfg), *table)

	switch command {
	case "list":
		return cmdList(ctx, s)
	case "show":
		return cmdShow(ctx, s, rest)
	case "pin":
		return cmdPin(ctx, s, rest)
	case "schedule":
		return cmdSchedule(ctx, s, rest)
	case "disable":
		return cmdDisable(ctx, s, rest)
	case "override":
		return cmdOverride(ctx, s, rest)
	case "remove":
		return cmdRemove(ctx, s, rest)
	default:
		global.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func cmdList(ctx context.Context, s *store.Store) error {
	rows, err := ops.List(ctx, s, time.Now())
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "RESOURCE\tMODE\tCONFIG\tOVERRIDE\tLAST ACTION\tOBSERVED(AT LAST ACTION)")
	for _, row := range rows {
		if row.Err != nil {
			fmt.Fprintf(w, "%s\terror: %s\t\t\t\t\n", row.ResourceID, row.Err)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.ResourceID, row.Config.Mode, describeConfig(row.Config), describeOverride(row.Override),
			describeAction(row.Status), row.Status.ObservedState)
	}
	return w.Flush()
}

func describeConfig(item model.ConfigItem) string {
	switch item.Mode {
	case model.ModePinned:
		return item.Desired
	case model.ModeSchedule:
		parts := []string{}
		if item.StartCron != "" {
			parts = append(parts, "start["+item.StartCron+"]")
		}
		if item.StopCron != "" {
			parts = append(parts, "stop["+item.StopCron+"]")
		}
		if item.Timezone != "" {
			parts = append(parts, item.Timezone)
		}
		return strings.Join(parts, " ")
	default:
		return "-"
	}
}

func describeOverride(o *model.Override) string {
	if o == nil {
		return "-"
	}
	return fmt.Sprintf("%s until %s", o.Desired, time.Unix(o.ExpiresAt, 0).Local().Format("2006-01-02 15:04"))
}

func describeAction(st model.Status) string {
	if st.LastAction == "" {
		return "-"
	}
	return st.LastAction + " at " + st.LastActionAt
}

func cmdShow(ctx context.Context, s *store.Store, args []string) error {
	resourceID, err := resourceIDArg(args, 1)
	if err != nil {
		return err
	}
	row, err := ops.Get(ctx, s, resourceID, time.Now())
	if err != nil {
		return err
	}
	out := map[string]any{"config": row.Config, "status": row.Status}
	if row.Override != nil {
		out["override"] = map[string]any{
			"desired":    row.Override.Desired,
			"expires_at": time.Unix(row.Override.ExpiresAt, 0).UTC().Format(time.RFC3339),
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdPin(ctx context.Context, s *store.Store, args []string) error {
	resourceID, err := resourceIDArg(args, 2)
	if err != nil {
		return err
	}
	if err := ops.Pin(ctx, s, resourceID, args[1]); err != nil {
		return err
	}
	fmt.Printf("pinned %s to %s\n", resourceID, args[1])
	return nil
}

func cmdSchedule(ctx context.Context, s *store.Store, args []string) error {
	resourceID, err := resourceIDArg(args, 1)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	startCron := fs.String("start", "", "cron for starting (5-field)")
	stopCron := fs.String("stop", "", "cron for stopping (5-field)")
	timezone := fs.String("timezone", "", "IANA timezone for the crons")
	restoreCount := fs.Int("restore-count", 0, "ECS desiredCount on start")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	item, err := ops.Schedule(ctx, s, resourceID, ops.ScheduleSpec{
		StartCron: *startCron, StopCron: *stopCron, Timezone: *timezone, RestoreCount: *restoreCount,
	})
	if err != nil {
		return err
	}
	fmt.Printf("scheduled %s (%s)\n", resourceID, describeConfig(item))
	return nil
}

func cmdDisable(ctx context.Context, s *store.Store, args []string) error {
	resourceID, err := resourceIDArg(args, 1)
	if err != nil {
		return err
	}
	if err := ops.Disable(ctx, s, resourceID); err != nil {
		return err
	}
	fmt.Printf("disabled %s\n", resourceID)
	return nil
}

func cmdOverride(ctx context.Context, s *store.Store, args []string) error {
	resourceID, err := resourceIDArg(args, 1)
	if err != nil {
		return err
	}
	rest := args[1:]
	var desired string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		desired, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("override", flag.ContinueOnError)
	duration := fs.Duration("for", 0, "how long the override lasts (e.g. 2h, 90m)")
	clear := fs.Bool("clear", false, "remove the override")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	if *clear {
		if err := ops.ClearOverride(ctx, s, resourceID); err != nil {
			return err
		}
		fmt.Printf("cleared override on %s\n", resourceID)
		return nil
	}
	if desired == "" || *duration <= 0 {
		return fmt.Errorf("override requires a desired state and -for DURATION (or -clear)")
	}
	expiresAt, err := ops.SetOverride(ctx, s, resourceID, desired, *duration, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("override: %s %s until %s\n", resourceID, desired, expiresAt.Local().Format("2006-01-02 15:04"))
	return nil
}

func cmdRemove(ctx context.Context, s *store.Store, args []string) error {
	resourceID, err := resourceIDArg(args, 1)
	if err != nil {
		return err
	}
	if err := ops.Remove(ctx, s, resourceID); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", resourceID)
	return nil
}

func resourceIDArg(args []string, want int) (string, error) {
	if len(args) < want {
		return "", fmt.Errorf("missing argument (see cheapskate-cli -h)")
	}
	resourceID := args[0]
	if _, err := model.ResourceIDType(resourceID); err != nil {
		return "", err
	}
	return resourceID, nil
}
