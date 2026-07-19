// Package model defines the data model shared across the reconciler and CLI.
package model

import (
	"fmt"
	"strings"
)

const (
	ConfigPrefix   = "config#"
	OverridePrefix = "override#"
	StatusPrefix   = "status#"
)

const (
	TypeRdsInstance = "rds-instance"
	TypeRdsCluster  = "rds-cluster"
	TypeEcsService  = "ecs-service"
)

const (
	ModePinned   = "pinned"
	ModeSchedule = "schedule"
	ModeDisabled = "disabled"
)

const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

// ResourceTypes maps the resource_id prefix (before the first "#") to the type.
var ResourceTypes = map[string]string{
	"rds-instance": TypeRdsInstance,
	"rds-cluster":  TypeRdsCluster,
	"ecs":          TypeEcsService,
}

// ConfigItem is the raw shape of a user-managed `config#` item in DynamoDB. The reconciler never writes these.
type ConfigItem struct {
	PK           string `dynamodbav:"pk" json:"pk"`
	Type         string `dynamodbav:"type,omitempty" json:"type,omitempty"`
	Mode         string `dynamodbav:"mode,omitempty" json:"mode,omitempty"`
	Desired      string `dynamodbav:"desired,omitempty" json:"desired,omitempty"`
	StartCron    string `dynamodbav:"start_cron,omitempty" json:"start_cron,omitempty"`
	StopCron     string `dynamodbav:"stop_cron,omitempty" json:"stop_cron,omitempty"`
	Timezone     string `dynamodbav:"timezone,omitempty" json:"timezone,omitempty"`
	RestoreCount *int32 `dynamodbav:"restore_count,omitempty" json:"restore_count,omitempty"`
}

// Config is a validated ConfigItem.
type Config struct {
	ResourceID   string // e.g. "rds-cluster#my-aurora", "ecs#my-cluster/my-service"
	Type         string
	Mode         string
	Desired      string
	StartCron    string
	StopCron     string
	Timezone     string
	RestoreCount *int32
}

// Ref is the target-specific identifier (part after the type prefix).
func (c Config) Ref() string {
	_, ref, _ := strings.Cut(c.ResourceID, "#")
	return ref
}

// Override is a time-limited `override#` item taking precedence over config.
type Override struct {
	Desired   string `dynamodbav:"desired"`
	ExpiresAt int64  `dynamodbav:"expires_at"` // epoch seconds; DynamoDB TTL removes the item lazily
}

// Status is the reconciler-owned `status#` item (audit + ECS restore data). A converged cycle writes nothing (by design, to avoid a write every 5 minutes for every steady-state resource), so every field here is a snapshot as of last_action_at/last_error_at, not the current live state (B-10) — e.g. ObservedState can go stale if something outside cheapskate changes the resource after the last action.
type Status struct {
	ObservedState     string `dynamodbav:"observed_state,omitempty" json:"observed_state,omitempty"`
	LastAction        string `dynamodbav:"last_action,omitempty" json:"last_action,omitempty"`
	LastActionAt      string `dynamodbav:"last_action_at,omitempty" json:"last_action_at,omitempty"`
	LastError         string `dynamodbav:"last_error,omitempty" json:"last_error,omitempty"`
	LastErrorAt       string `dynamodbav:"last_error_at,omitempty" json:"last_error_at,omitempty"`
	SavedDesiredCount *int32 `dynamodbav:"saved_desired_count,omitempty" json:"saved_desired_count,omitempty"`
	SavedScalingMin   *int32 `dynamodbav:"saved_scaling_min,omitempty" json:"saved_scaling_min,omitempty"`
	SavedScalingMax   *int32 `dynamodbav:"saved_scaling_max,omitempty" json:"saved_scaling_max,omitempty"`
}

// SavedState is target-specific state captured before a Stop mutates AWS, so Start can restore it later. A nil field means "leave whatever was previously saved alone" — either the target has nothing to save for that field, or the current AWS value looks like cheapskate's own doing (see EcsServiceTarget.PrepareStop) and must not clobber the real saved value.
type SavedState struct {
	DesiredCount *int32
	ScalingMin   *int32
	ScalingMax   *int32
}

// Observation is the actual state of a target as seen via Describe APIs.
type Observation struct {
	State  string // "running" | "stopped" | "transitioning" | "not-found"
	Detail string
	// DesiredCount is only set for ECS services.
	DesiredCount *int32
}

const (
	StateRunning       = "running"
	StateStopped       = "stopped"
	StateTransitioning = "transitioning"
	StateNotFound      = "not-found"
)

// ResourceIDType derives the resource type from a resource_id such as "rds-cluster#my-aurora" or "ecs#my-cluster/my-service".
func ResourceIDType(resourceID string) (string, error) {
	prefix, _, found := strings.Cut(resourceID, "#")
	if !found {
		return "", fmt.Errorf("malformed resource_id (want <prefix>#<identifier>): %q", resourceID)
	}
	t, ok := ResourceTypes[prefix]
	if !ok {
		return "", fmt.Errorf("%s: unknown resource prefix %q", resourceID, prefix)
	}
	return t, nil
}

// ParseConfig validates a raw config item.
func ParseConfig(item ConfigItem) (Config, error) {
	rest, hasPrefix := strings.CutPrefix(item.PK, ConfigPrefix)
	if !hasPrefix || !strings.Contains(rest, "#") {
		return Config{}, fmt.Errorf("malformed config key: %q", item.PK)
	}
	resourceID := rest

	switch item.Type {
	case TypeRdsInstance, TypeRdsCluster, TypeEcsService:
	default:
		return Config{}, fmt.Errorf("%s: unknown type %q", resourceID, item.Type)
	}
	mode := item.Mode
	if mode == "" {
		mode = ModeDisabled
	}
	switch mode {
	case ModePinned, ModeSchedule, ModeDisabled:
	default:
		return Config{}, fmt.Errorf("%s: unknown mode %q", resourceID, mode)
	}
	if mode == ModePinned && item.Desired != DesiredRunning && item.Desired != DesiredStopped {
		return Config{}, fmt.Errorf("%s: mode=pinned requires desired running|stopped", resourceID)
	}

	return Config{
		ResourceID:   resourceID,
		Type:         item.Type,
		Mode:         mode,
		Desired:      item.Desired,
		StartCron:    item.StartCron,
		StopCron:     item.StopCron,
		Timezone:     item.Timezone,
		RestoreCount: item.RestoreCount,
	}, nil
}
