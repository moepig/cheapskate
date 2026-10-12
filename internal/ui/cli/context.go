package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
)

type cliContext struct {
	Table    string `json:"table"`
	Profile  string `json:"profile,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

type cliConfig struct {
	CurrentContext string                `json:"current_context"`
	Contexts       map[string]cliContext `json:"contexts"`
}

func cliConfigPath(path string) (string, error) {
	if path = firstNonEmpty(path, os.Getenv("CHEAPSKATE_CONFIG")); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find CLI config directory: %w", err)
	}
	return filepath.Join(dir, "cheapskate", "config.json"), nil
}

func loadCLIConfig(path string) (*cliConfig, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &cliConfig{Contexts: map[string]cliContext{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read CLI config %q: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var saved *cliConfig
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("read CLI config %q: %w", path, err)
	}
	if saved == nil {
		return nil, fmt.Errorf("read CLI config %q: expected a JSON object", path)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read CLI config %q: expected a single JSON object", path)
	}
	if saved.Contexts == nil {
		saved.Contexts = map[string]cliContext{}
	}
	return saved, nil
}

func saveCLIConfig(path string, saved *cliConfig) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("save CLI config %q: %w", path, err)
		}
	}()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := writeJSON(file, saved); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (saved *cliConfig) context(name string) (cliContext, error) {
	if name == "" {
		return cliContext{}, nil
	}
	selected, ok := saved.Contexts[name]
	if !ok {
		return cliContext{}, fmt.Errorf("context %q not found (see: cheapskate-cli config get-contexts)", name)
	}
	if strings.TrimSpace(selected.Table) == "" {
		return cliContext{}, fmt.Errorf("context %q has no table (use: cheapskate-cli config set-context NAME --table TABLE)", name)
	}
	return selected, nil
}

func cmdConfig(args []string, out io.Writer, output, path string, saved *cliConfig) error {
	if len(args) == 0 {
		return fmt.Errorf("missing config command (see: cheapskate-cli -h)")
	}
	command, args := args[0], args[1:]
	switch command {
	case "set-context":
		fs := newFlagSet("config set-context")
		var values cliContext
		fs.StringVar(&values.Table, "table", "", "DynamoDB state table name")
		fs.StringVar(&values.Profile, "profile", "", "AWS shared config profile")
		fs.StringVar(&values.Region, "region", "", "AWS region")
		fs.StringVar(&values.Timezone, "timezone", "", "IANA schedule time zone")
		names, err := parseInterleaved(fs, args)
		if err != nil {
			return err
		}
		if len(names) != 1 || names[0] == "" || strings.IndexFunc(names[0], unicode.IsSpace) >= 0 {
			return fmt.Errorf("config set-context requires one context name without whitespace")
		}
		name := names[0]
		selected := saved.Contexts[name]
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "table":
				selected.Table = values.Table
			case "profile":
				selected.Profile = values.Profile
			case "region":
				selected.Region = values.Region
			case "timezone":
				selected.Timezone = values.Timezone
			}
		})
		if strings.TrimSpace(selected.Table) == "" {
			return fmt.Errorf("--table is required for a new context and cannot be empty")
		}
		if selected.Timezone != "" {
			if _, err := time.LoadLocation(selected.Timezone); err != nil {
				return fmt.Errorf("invalid timezone %q: %w", selected.Timezone, err)
			}
		}
		saved.Contexts[name] = selected
		if err := saveCLIConfig(path, saved); err != nil {
			return err
		}
		if output == "json" {
			return writeJSON(out, struct {
				Name string `json:"name"`
				cliContext
			}{name, selected})
		}
		_, err = fmt.Fprintf(out, "Context %q saved.\n", name)
		return err
	case "use-context":
		if len(args) != 1 || args[0] == "" {
			return fmt.Errorf("config use-context requires one context name")
		}
		if _, err := saved.context(args[0]); err != nil {
			return err
		}
		saved.CurrentContext = args[0]
		if err := saveCLIConfig(path, saved); err != nil {
			return err
		}
		if output == "json" {
			return writeJSON(out, map[string]string{"current_context": saved.CurrentContext})
		}
		_, err := fmt.Fprintf(out, "Switched to context %q.\n", saved.CurrentContext)
		return err
	case "current-context":
		if len(args) != 0 {
			return fmt.Errorf("config current-context takes no arguments")
		}
		if saved.CurrentContext == "" {
			return fmt.Errorf("current context not set (use: cheapskate-cli config use-context NAME)")
		}
		if output == "json" {
			return writeJSON(out, map[string]string{"current_context": saved.CurrentContext})
		}
		_, err := fmt.Fprintln(out, saved.CurrentContext)
		return err
	case "get-contexts":
		if len(args) != 0 {
			return fmt.Errorf("config get-contexts takes no arguments")
		}
		if output == "json" {
			return writeJSON(out, saved)
		}
		names := make([]string, 0, len(saved.Contexts))
		for name := range saved.Contexts {
			names = append(names, name)
		}
		slices.Sort(names)
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CURRENT\tNAME\tTABLE\tPROFILE\tREGION\tTIMEZONE")
		for _, name := range names {
			selected := saved.Contexts[name]
			current := ""
			if name == saved.CurrentContext {
				current = "*"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", current, name, selected.Table, selected.Profile, selected.Region, selected.Timezone)
		}
		return w.Flush()
	default:
		return fmt.Errorf("unknown config command %q (see: cheapskate-cli -h)", command)
	}
}
