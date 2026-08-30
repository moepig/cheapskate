package state

import "cheapskate/internal/core/model"

const (
	configPK         = "CONFIG"
	lockPK           = "LOCK"
	currentSK        = "CURRENT"
	groupSKPrefix    = "GROUP#"
	overrideSKPrefix = "OVERRIDE#"
	statusPKPrefix   = "STATUS#"
)

type itemKey struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
}

func groupKey(name string) itemKey    { return itemKey{PK: configPK, SK: groupSKPrefix + name} }
func overrideKey(name string) itemKey { return itemKey{PK: configPK, SK: overrideSKPrefix + name} }
func statusKey(resourceID string) itemKey {
	return itemKey{PK: statusPKPrefix + resourceID, SK: currentSK}
}
func groupStatusKey(name string) itemKey { return statusKey(model.GroupStatusID(name)) }
func reconcileLockKey() itemKey          { return itemKey{PK: lockPK, SK: "RECONCILE"} }

type groupItem struct {
	PK        string   `dynamodbav:"pk"`
	SK        string   `dynamodbav:"sk"`
	Mode      string   `dynamodbav:"mode,omitempty"`
	Desired   string   `dynamodbav:"desired,omitempty"`
	StartCron string   `dynamodbav:"start_cron,omitempty"`
	StopCron  string   `dynamodbav:"stop_cron,omitempty"`
	Timezone  string   `dynamodbav:"timezone,omitempty"`
	TagKey    string   `dynamodbav:"tag_key,omitempty"`
	TagValue  string   `dynamodbav:"tag_value,omitempty"`
	Types     []string `dynamodbav:"types,stringset,omitempty"`
}

func newGroupItem(spec model.GroupSpec) groupItem {
	k := groupKey(spec.Name)
	return groupItem{
		PK:        k.PK,
		SK:        k.SK,
		Mode:      string(spec.Mode),
		Desired:   string(spec.Desired),
		StartCron: spec.StartCron,
		StopCron:  spec.StopCron,
		Timezone:  spec.Timezone,
		TagKey:    spec.TagKey,
		TagValue:  spec.TagValue,
		Types:     model.TypeNames(spec.Types),
	}
}

func (i groupItem) spec(name string) model.GroupSpec {
	return model.GroupSpec{
		Name:      name,
		Mode:      model.Mode(i.Mode),
		Desired:   model.DesiredState(i.Desired),
		StartCron: i.StartCron,
		StopCron:  i.StopCron,
		Timezone:  i.Timezone,
		TagKey:    i.TagKey,
		TagValue:  i.TagValue,
		Types:     model.ResourceTypes(i.Types),
	}
}

type overrideItem struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Desired   string `dynamodbav:"desired"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

func (i overrideItem) override() model.Override {
	return model.Override{Desired: model.DesiredState(i.Desired), ExpiresAt: i.ExpiresAt}
}

type statusItem struct {
	PK                  string `dynamodbav:"pk"`
	SK                  string `dynamodbav:"sk"`
	ObservedState       string `dynamodbav:"observed_state,omitempty"`
	LastAction          string `dynamodbav:"last_action,omitempty"`
	LastDesired         string `dynamodbav:"last_desired,omitempty"`
	LastActionAt        string `dynamodbav:"last_action_at,omitempty"`
	LastError           string `dynamodbav:"last_error,omitempty"`
	LastErrorAt         string `dynamodbav:"last_error_at,omitempty"`
	TransitioningSince  string `dynamodbav:"transitioning_since,omitempty"`
	PendingOperationID  string `dynamodbav:"pending_operation_id,omitempty"`
	PendingAction       string `dynamodbav:"pending_action,omitempty"`
	PendingDesired      string `dynamodbav:"pending_desired,omitempty"`
	PendingObserved     string `dynamodbav:"pending_observed,omitempty"`
	PendingStartedAt    string `dynamodbav:"pending_started_at,omitempty"`
	NotificationPending string `dynamodbav:"notification_pending,omitempty"`
}

func (i statusItem) status() model.Status {
	return model.Status{
		ObservedState:       model.ObservedState(i.ObservedState),
		LastAction:          model.Action(i.LastAction),
		LastDesired:         model.DesiredState(i.LastDesired),
		LastActionAt:        i.LastActionAt,
		LastError:           i.LastError,
		LastErrorAt:         i.LastErrorAt,
		TransitioningSince:  i.TransitioningSince,
		PendingOperationID:  i.PendingOperationID,
		PendingAction:       model.Action(i.PendingAction),
		PendingDesired:      model.DesiredState(i.PendingDesired),
		PendingObserved:     model.ObservedState(i.PendingObserved),
		PendingStartedAt:    i.PendingStartedAt,
		NotificationPending: i.NotificationPending,
	}
}

// GroupPatchはグループ設定の属性単位の変更を表す。
// nilは変更しないことを表し、stringとsliceの空値は属性の削除を表す。
type GroupPatch struct {
	Mode      *model.Mode
	Desired   *model.DesiredState
	StartCron *string
	StopCron  *string
	Timezone  *string
	TagKey    *string
	TagValue  *string
	Types     *[]model.ResourceType
}

// StatusPatchはstatusアイテムの属性単位の変更を表す。
// nilは変更しないことを表し、空文字列は空値への更新を表す。
type StatusPatch struct {
	ObservedState       *model.ObservedState
	LastAction          *model.Action
	LastDesired         *model.DesiredState
	LastActionAt        *string
	LastError           *string
	LastErrorAt         *string
	TransitioningSince  *string
	PendingOperationID  *string
	PendingAction       *model.Action
	PendingDesired      *model.DesiredState
	PendingObserved     *model.ObservedState
	PendingStartedAt    *string
	NotificationPending *string
}

// Setは変更対象の値を指すポインタを返す。
func Set[T any](v T) *T { return &v }

type statusAttr struct {
	name  string
	value string
}

func (p StatusPatch) attributes() []statusAttr {
	var out []statusAttr
	if p.ObservedState != nil {
		out = append(out, statusAttr{"observed_state", string(*p.ObservedState)})
	}
	if p.LastAction != nil {
		out = append(out, statusAttr{"last_action", string(*p.LastAction)})
	}
	if p.LastDesired != nil {
		out = append(out, statusAttr{"last_desired", string(*p.LastDesired)})
	}
	if p.LastActionAt != nil {
		out = append(out, statusAttr{"last_action_at", *p.LastActionAt})
	}
	if p.LastError != nil {
		out = append(out, statusAttr{"last_error", *p.LastError})
	}
	if p.LastErrorAt != nil {
		out = append(out, statusAttr{"last_error_at", *p.LastErrorAt})
	}
	if p.TransitioningSince != nil {
		out = append(out, statusAttr{"transitioning_since", *p.TransitioningSince})
	}
	if p.PendingOperationID != nil {
		out = append(out, statusAttr{"pending_operation_id", *p.PendingOperationID})
	}
	if p.PendingAction != nil {
		out = append(out, statusAttr{"pending_action", string(*p.PendingAction)})
	}
	if p.PendingDesired != nil {
		out = append(out, statusAttr{"pending_desired", string(*p.PendingDesired)})
	}
	if p.PendingObserved != nil {
		out = append(out, statusAttr{"pending_observed", string(*p.PendingObserved)})
	}
	if p.PendingStartedAt != nil {
		out = append(out, statusAttr{"pending_started_at", *p.PendingStartedAt})
	}
	if p.NotificationPending != nil {
		out = append(out, statusAttr{"notification_pending", *p.NotificationPending})
	}
	return out
}
