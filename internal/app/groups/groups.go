package groups

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
)

type Store interface {
	ListGroups(ctx context.Context) ([]state.GroupRow, error)
	GetGroup(ctx context.Context, name string) (*model.GroupSpec, error)
	CreateGroup(ctx context.Context, group model.GroupSpec) error
	SetSchedule(ctx context.Context, name string, schedule model.ScheduleSpec) error
	SetOverride(ctx context.Context, name string, override model.Override, expiresAt int64) error
	ClearOverride(ctx context.Context, name string) error
	DeleteGroup(ctx context.Context, name string) error
}

var ErrInvalidConfig = errors.New("invalid group configuration")

type Service struct {
	store      Store
	discoverer port.Discoverer
	describers map[model.ResourceType]port.Describer
	location   *time.Location
}

func New(store Store, discoverer port.Discoverer, describers map[model.ResourceType]port.Describer, location *time.Location) *Service {
	return &Service{store: store, discoverer: discoverer, describers: describers, location: location}
}

type GroupRow struct {
	Name      string
	Group     model.GroupSpec
	ConfigErr error
}

func (s *Service) List(ctx context.Context, now time.Time) ([]GroupRow, error) {
	stored, err := s.store.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]GroupRow, 0, len(stored))
	for _, row := range stored {
		configErr := row.Err
		if configErr == nil {
			configErr = model.ValidateGroup(row.Group, now, s.location)
		}
		rows = append(rows, GroupRow{Name: row.Name, Group: row.Group, ConfigErr: configErr})
	}
	return rows, nil
}

type ResourceRow struct {
	Resource model.Resource
	Live     *model.Observation
	LiveErr  error
}

type GroupDetail struct {
	Group     model.GroupSpec
	Resources []ResourceRow
}

func (s *Service) Show(ctx context.Context, name string, now time.Time) (GroupDetail, error) {
	group, err := s.validGroup(ctx, name, now)
	if err != nil {
		return GroupDetail{}, err
	}
	resources, err := s.discoverer.Discover(ctx)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("discover resources: %w", err)
	}
	rows := make([]ResourceRow, 0)
	for _, resource := range resources {
		if resource.Tags[model.GroupTagKey] != name {
			continue
		}
		row := ResourceRow{Resource: resource}
		if describer, ok := s.describers[resource.Type]; ok {
			observation, describeErr := describer.Describe(ctx, resource)
			if describeErr != nil {
				row.LiveErr = describeErr
			} else {
				row.Live = &observation
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Resource.ARN < rows[j].Resource.ARN })
	return GroupDetail{Group: *group, Resources: rows}, nil
}

func (s *Service) Schedule(ctx context.Context, name string, schedule model.ScheduleSpec, now time.Time) (model.GroupSpec, error) {
	if err := model.ValidGroupName(name); err != nil {
		return model.GroupSpec{}, err
	}
	if schedule.StartCron == "" || schedule.StopCron == "" {
		return model.GroupSpec{}, fmt.Errorf("schedule start_cron and stop_cron are required")
	}
	existing, err := s.store.GetGroup(ctx, name)
	if err != nil {
		return model.GroupSpec{}, err
	}
	next := model.GroupSpec{Name: name, StartCron: schedule.StartCron, StopCron: schedule.StopCron}
	if existing != nil {
		next.Override = existing.Override
		next.OverrideExpiresAt = existing.OverrideExpiresAt
	}
	if err := model.ValidateGroupForWrite(next, now, s.location); err != nil {
		return model.GroupSpec{}, err
	}
	if existing == nil {
		if err := s.store.CreateGroup(ctx, next); err != nil {
			return model.GroupSpec{}, err
		}
	} else if err := s.store.SetSchedule(ctx, name, schedule); err != nil {
		return model.GroupSpec{}, err
	}
	return next, nil
}

func (s *Service) Override(ctx context.Context, name string, override model.Override, duration time.Duration, now time.Time) (model.GroupSpec, error) {
	if err := model.ValidGroupName(name); err != nil {
		return model.GroupSpec{}, err
	}
	if err := override.Validate(); err != nil {
		return model.GroupSpec{}, err
	}
	if duration < 0 {
		return model.GroupSpec{}, fmt.Errorf("override duration must not be negative")
	}
	existing, err := s.store.GetGroup(ctx, name)
	if err != nil {
		return model.GroupSpec{}, err
	}
	next := model.GroupSpec{Name: name, Override: override}
	if existing != nil {
		next = *existing
		next.Override = override
		next.OverrideExpiresAt = 0
	}
	if duration > 0 {
		if existing == nil {
			return model.GroupSpec{}, fmt.Errorf("%w: %s", state.ErrGroupNotFound, name)
		}
		next.OverrideExpiresAt = now.Add(duration).Unix()
	}
	if err := model.ValidateGroupForWrite(next, now, s.location); err != nil {
		return model.GroupSpec{}, err
	}
	if existing == nil {
		if err := s.store.CreateGroup(ctx, next); err != nil {
			return model.GroupSpec{}, err
		}
	} else if err := s.store.SetOverride(ctx, name, override, next.OverrideExpiresAt); err != nil {
		return model.GroupSpec{}, err
	}
	return next, nil
}

func (s *Service) ClearOverride(ctx context.Context, name string, now time.Time) (model.GroupSpec, error) {
	group, err := s.requireGroup(ctx, name)
	if err != nil {
		return model.GroupSpec{}, err
	}
	next := *group
	next.Override = ""
	next.OverrideExpiresAt = 0
	if err := model.ValidateGroupForWrite(next, now, s.location); err != nil {
		return model.GroupSpec{}, err
	}
	if err := s.store.ClearOverride(ctx, name); err != nil {
		return model.GroupSpec{}, err
	}
	return next, nil
}

func (s *Service) Remove(ctx context.Context, name string) error {
	if _, err := s.requireGroup(ctx, name); err != nil {
		return err
	}
	return s.store.DeleteGroup(ctx, name)
}

func (s *Service) validGroup(ctx context.Context, name string, now time.Time) (*model.GroupSpec, error) {
	group, err := s.requireGroup(ctx, name)
	if err != nil {
		if errors.Is(err, state.ErrInvalidGroup) {
			return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		return nil, err
	}
	if err := model.ValidateGroup(*group, now, s.location); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return group, nil
}

func (s *Service) requireGroup(ctx context.Context, name string) (*model.GroupSpec, error) {
	if err := model.ValidGroupName(name); err != nil {
		return nil, err
	}
	group, err := s.store.GetGroup(ctx, name)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, fmt.Errorf("%w: %s", state.ErrGroupNotFound, name)
	}
	return group, nil
}
