// Package target implements the per-resource-type describe/stop/start operations behind a common interface.
package target

import (
	"context"

	"cheapskate/internal/model"
)

//go:generate go tool mockgen -destination ../mocks/target.go -package mocks cheapskate/internal/target Target,RdsAPI,EcsAPI,AutoScalingAPI

// Target abstracts one resource type.
//
// Stop is split in two so the caller can persist restore state (write-ahead) before anything in AWS actually changes: PrepareStop is read-only and returns the state to save, then Stop performs the mutation. This bounds the damage of a crash between the two — the worst case is a stale-but-safe saved value, never a lost one.
type Target interface {
	Type() string
	Describe(ctx context.Context, ref string) (model.Observation, error)
	PrepareStop(ctx context.Context, ref string, cfg model.Config, status model.Status) (*model.SavedState, error)
	Stop(ctx context.Context, ref string, cfg model.Config, status model.Status) error
	Start(ctx context.Context, ref string, cfg model.Config, status model.Status) (*model.SavedState, error)
}
