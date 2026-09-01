package state

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/core/model"
)

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
	PK                 string `dynamodbav:"pk"`
	SK                 string `dynamodbav:"sk"`
	ObservedState      string `dynamodbav:"observed_state,omitempty"`
	LastAction         string `dynamodbav:"last_action,omitempty"`
	LastDesired        string `dynamodbav:"last_desired,omitempty"`
	LastActionAt       string `dynamodbav:"last_action_at,omitempty"`
	LastError          string `dynamodbav:"last_error,omitempty"`
	LastErrorAt        string `dynamodbav:"last_error_at,omitempty"`
	TransitioningSince string `dynamodbav:"transitioning_since,omitempty"`
	PendingOperationID string `dynamodbav:"pending_operation_id,omitempty"`
	PendingGroup       string `dynamodbav:"pending_group,omitempty"`
	PendingConfigHash  string `dynamodbav:"pending_config_hash,omitempty"`
	PendingAction      string `dynamodbav:"pending_action,omitempty"`
	PendingDesired     string `dynamodbav:"pending_desired,omitempty"`
	PendingObserved    string `dynamodbav:"pending_observed,omitempty"`
	PendingStartedAt   string `dynamodbav:"pending_started_at,omitempty"`
}

func (i statusItem) status() model.Status {
	return model.Status{
		ObservedState:      model.ObservedState(i.ObservedState),
		LastAction:         model.Action(i.LastAction),
		LastDesired:        model.DesiredState(i.LastDesired),
		LastActionAt:       i.LastActionAt,
		LastError:          i.LastError,
		LastErrorAt:        i.LastErrorAt,
		TransitioningSince: i.TransitioningSince,
		PendingOperationID: i.PendingOperationID,
		PendingGroup:       i.PendingGroup,
		PendingConfigHash:  i.PendingConfigHash,
		PendingAction:      model.Action(i.PendingAction),
		PendingDesired:     model.DesiredState(i.PendingDesired),
		PendingObserved:    model.ObservedState(i.PendingObserved),
		PendingStartedAt:   i.PendingStartedAt,
	}
}

// Status の各属性を独立して復号し、読めた属性と読めなかった属性を同じ結果に保持する。
// 1 属性の型が壊れていても、pending operation の有無や通知の重複排除に使う他の属性を失わない。
func decodeStatusRecord(resourceID string, raw map[string]types.AttributeValue) StatusRecord {
	var item statusItem
	fields := []struct {
		name string
		set  func(string)
	}{
		{"observed_state", func(v string) { item.ObservedState = v }},
		{"last_action", func(v string) { item.LastAction = v }},
		{"last_desired", func(v string) { item.LastDesired = v }},
		{"last_action_at", func(v string) { item.LastActionAt = v }},
		{"last_error", func(v string) { item.LastError = v }},
		{"last_error_at", func(v string) { item.LastErrorAt = v }},
		{"transitioning_since", func(v string) { item.TransitioningSince = v }},
		{"pending_operation_id", func(v string) { item.PendingOperationID = v }},
		{"pending_group", func(v string) { item.PendingGroup = v }},
		{"pending_config_hash", func(v string) { item.PendingConfigHash = v }},
		{"pending_action", func(v string) { item.PendingAction = v }},
		{"pending_desired", func(v string) { item.PendingDesired = v }},
		{"pending_observed", func(v string) { item.PendingObserved = v }},
		{"pending_started_at", func(v string) { item.PendingStartedAt = v }},
	}
	record := StatusRecord{}
	for _, field := range fields {
		value, exists := raw[field.name]
		if !exists {
			continue
		}
		text, ok := value.(*types.AttributeValueMemberS)
		if !ok {
			record.CorruptAttributes = append(record.CorruptAttributes, field.name)
			continue
		}
		field.set(text.Value)
	}
	record.Status = item.status()
	if len(record.CorruptAttributes) > 0 {
		record.Err = fmt.Errorf("unmarshal status %s: attributes must be strings: %s", resourceID, strings.Join(record.CorruptAttributes, ", "))
	}
	return record
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
	ObservedState      *model.ObservedState
	LastAction         *model.Action
	LastDesired        *model.DesiredState
	LastActionAt       *string
	LastError          *string
	LastErrorAt        *string
	TransitioningSince *string
	PendingOperationID *string
	PendingGroup       *string
	PendingConfigHash  *string
	PendingAction      *model.Action
	PendingDesired     *model.DesiredState
	PendingObserved    *model.ObservedState
	PendingStartedAt   *string
}

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
	if p.PendingGroup != nil {
		out = append(out, statusAttr{"pending_group", *p.PendingGroup})
	}
	if p.PendingConfigHash != nil {
		out = append(out, statusAttr{"pending_config_hash", *p.PendingConfigHash})
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
	return out
}
