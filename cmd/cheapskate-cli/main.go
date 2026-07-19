// cheapskate-cli is the cheapskate configuration CLI. It manipulates the tag#, member#, and
// override# items in the DynamoDB state table; it never calls the RDS/ECS APIs itself (the
// reconciler Lambda does that).
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
  list                                     all tags with their members and state
  show --tag NAME                          tag config + override + members as JSON
  add --tag NAME --type TYPE <resource flags> [-restore-count N]
                                           add a resource to a tag; creates the tag on
                                           first add (mode=disabled until pinned or
                                           scheduled). A resource belongs to exactly
                                           one tag.
  remove --tag NAME [--type TYPE <resource flags>]
                                           with resource flags: remove that member
                                           (and its status); without: remove the whole
                                           tag, its members, override, and statuses
  pin --tag NAME running|stopped           keep every member in a fixed state
  schedule --tag NAME [-start CRON] [-stop CRON] [-timezone TZ]
                                           cron-based start/stop for all members
  disable --tag NAME                       keep the config but stop managing
  override --tag NAME running|stopped -for DURATION
                                           temporary override for all members (expires
                                           via TTL); rejected while the tag is disabled
  override --tag NAME -clear               remove the override now

Resource flags (add / remove):
  --type rds-instance|rds-cluster|ecs
  --name IDENTIFIER                        for rds-instance and rds-cluster
  --cluster CLUSTER --service SERVICE      for ecs
  -restore-count N                         ecs only: desiredCount used on start
                                           (default: the count saved at stop time, then 1)

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
	case "add":
		return cmdAdd(ctx, s, rest)
	case "remove":
		return cmdRemove(ctx, s, rest)
	case "pin":
		return cmdPin(ctx, s, rest)
	case "schedule":
		return cmdSchedule(ctx, s, rest)
	case "disable":
		return cmdDisable(ctx, s, rest)
	case "override":
		return cmdOverride(ctx, s, rest)
	default:
		global.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

// parseInterleaved parses fs over args, collecting non-flag tokens as positionals so flags and
// positionals (only ever "running"/"stopped" here, which never start with "-") may appear in any
// order — e.g. both "pin --tag dev stopped" and "pin stopped --tag dev" work.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// resourceFlags are the --type/--name/--cluster/--service flags shared by add and remove.
type resourceFlags struct {
	typ, name, cluster, service string
}

func addResourceFlags(fs *flag.FlagSet, r *resourceFlags) {
	fs.StringVar(&r.typ, "type", "", "resource type: rds-instance | rds-cluster | ecs")
	fs.StringVar(&r.name, "name", "", "RDS instance/cluster identifier (rds types only)")
	fs.StringVar(&r.cluster, "cluster", "", "ECS cluster name (ecs only)")
	fs.StringVar(&r.service, "service", "", "ECS service name (ecs only)")
}

func (r *resourceFlags) given() bool {
	return r.typ != "" || r.name != "" || r.cluster != "" || r.service != ""
}

func (r *resourceFlags) resourceID() (string, error) {
	return ops.AssembleResourceID(r.typ, r.name, r.cluster, r.service)
}

func cmdList(ctx context.Context, s *store.Store) error {
	rows, err := ops.List(ctx, s, time.Now())
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "TAG / MEMBER\tMODE\tCONFIG\tOVERRIDE\tLAST ACTION\tOBSERVED(AT LAST ACTION)")
	for _, row := range rows {
		if row.Err != nil {
			fmt.Fprintf(w, "%s\terror: %s\t\t\t\t\n", row.Name, row.Err)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\t\n",
			row.Name, row.Tag.Mode, describeTag(row.Tag), describeOverride(row.Override))
		for _, m := range row.Members {
			fmt.Fprintf(w, "  %s\t\t\t\t%s\t%s\n",
				m.ResourceID, describeAction(m.Status), m.Status.ObservedState)
		}
	}
	return w.Flush()
}

func describeTag(item model.TagItem) string {
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

// showMember/showOutput shape cmdShow's JSON so members resolve their status inline, per the
// "confirmation commands resolve the tag's applied resources" requirement.
type showMember struct {
	ResourceID   string       `json:"resource_id"`
	Type         string       `json:"type"`
	RestoreCount *int32       `json:"restore_count,omitempty"`
	Status       model.Status `json:"status"`
}

type showOutput struct {
	Tag      model.TagItem `json:"tag"`
	Override any           `json:"override,omitempty"`
	Members  []showMember  `json:"members"`
}

func cmdShow(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	var tag string
	fs.StringVar(&tag, "tag", "", "tag name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}
	row, err := ops.Get(ctx, s, tag, time.Now())
	if err != nil {
		return err
	}
	out := showOutput{Tag: row.Tag}
	if row.Override != nil {
		out.Override = map[string]any{
			"desired":    row.Override.Desired,
			"expires_at": time.Unix(row.Override.ExpiresAt, 0).UTC().Format(time.RFC3339),
		}
	}
	for _, m := range row.Members {
		out.Members = append(out.Members, showMember{
			ResourceID: m.ResourceID, Type: m.Member.Type, RestoreCount: m.Member.RestoreCount, Status: m.Status,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdAdd(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	var tag string
	var rf resourceFlags
	var restoreCount int
	fs.StringVar(&tag, "tag", "", "tag name")
	addResourceFlags(fs, &rf)
	fs.IntVar(&restoreCount, "restore-count", 0, "ecs only: desiredCount used on start")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}
	resourceID, err := rf.resourceID()
	if err != nil {
		return err
	}
	tagCreated, err := ops.Add(ctx, s, tag, resourceID, restoreCount)
	if err != nil {
		return err
	}
	fmt.Printf("added %s to tag %q\n", resourceID, tag)
	if tagCreated {
		fmt.Printf("(created tag %q, mode=disabled — pin or schedule it)\n", tag)
	}
	return nil
}

func cmdRemove(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	var tag string
	var rf resourceFlags
	fs.StringVar(&tag, "tag", "", "tag name")
	addResourceFlags(fs, &rf)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}
	if rf.given() {
		resourceID, err := rf.resourceID()
		if err != nil {
			return err
		}
		if err := ops.RemoveMember(ctx, s, tag, resourceID); err != nil {
			return err
		}
		fmt.Printf("removed %s from tag %q\n", resourceID, tag)
		return nil
	}
	if err := ops.RemoveTag(ctx, s, tag); err != nil {
		return err
	}
	fmt.Printf("removed tag %q\n", tag)
	return nil
}

func cmdPin(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	var tag string
	fs.StringVar(&tag, "tag", "", "tag name")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}
	if len(pos) != 1 {
		return fmt.Errorf("pin requires exactly one of running|stopped, got %q", pos)
	}
	if err := ops.Pin(ctx, s, tag, pos[0]); err != nil {
		return err
	}
	fmt.Printf("pinned tag %q to %s\n", tag, pos[0])
	return nil
}

func cmdSchedule(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	var tag string
	fs.StringVar(&tag, "tag", "", "tag name")
	startCron := fs.String("start", "", "cron for starting (5-field)")
	stopCron := fs.String("stop", "", "cron for stopping (5-field)")
	timezone := fs.String("timezone", "", "IANA timezone for the crons")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}

	item, err := ops.Schedule(ctx, s, tag, ops.ScheduleSpec{
		StartCron: *startCron, StopCron: *stopCron, Timezone: *timezone,
	})
	if err != nil {
		return err
	}
	fmt.Printf("scheduled tag %q (%s)\n", tag, describeTag(item))
	return nil
}

func cmdDisable(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("disable", flag.ContinueOnError)
	var tag string
	fs.StringVar(&tag, "tag", "", "tag name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}
	if err := ops.Disable(ctx, s, tag); err != nil {
		return err
	}
	fmt.Printf("disabled tag %q\n", tag)
	return nil
}

func cmdOverride(ctx context.Context, s *store.Store, args []string) error {
	fs := flag.NewFlagSet("override", flag.ContinueOnError)
	var tag string
	fs.StringVar(&tag, "tag", "", "tag name")
	duration := fs.Duration("for", 0, "how long the override lasts (e.g. 2h, 90m)")
	clear := fs.Bool("clear", false, "remove the override")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if tag == "" {
		return fmt.Errorf("--tag is required")
	}

	if *clear {
		if err := ops.ClearOverride(ctx, s, tag); err != nil {
			return err
		}
		fmt.Printf("cleared override on tag %q\n", tag)
		return nil
	}
	if len(pos) != 1 {
		return fmt.Errorf("override requires a desired state and -for DURATION (or -clear)")
	}
	desired := pos[0]
	if *duration <= 0 {
		return fmt.Errorf("override requires a desired state and -for DURATION (or -clear)")
	}
	expiresAt, err := ops.SetOverride(ctx, s, tag, desired, *duration, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("override: tag %q %s until %s\n", tag, desired, expiresAt.Local().Format("2006-01-02 15:04"))
	return nil
}
