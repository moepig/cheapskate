package state

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/core/model"
)

const (
	configPK      = "CONFIG"
	groupSKPrefix = "GROUP#"
)

var positiveDecimal = regexp.MustCompile(`^[1-9][0-9]*$`)

type itemKey struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
}

func groupKey(name string) itemKey {
	return itemKey{PK: configPK, SK: groupSKPrefix + name}
}

func decodeGroup(raw map[string]types.AttributeValue) (model.GroupSpec, error) {
	allowed := map[string]struct{}{
		"pk": {}, "sk": {}, "start_cron": {}, "stop_cron": {}, "override": {}, "override_expires_at": {},
	}
	var unknown []string
	for name := range raw {
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return model.GroupSpec{}, fmt.Errorf("unknown attributes: %s", strings.Join(unknown, ", "))
	}
	pk, err := stringAttribute(raw, "pk", true)
	if err != nil {
		return model.GroupSpec{}, err
	}
	sk, err := stringAttribute(raw, "sk", true)
	if err != nil {
		return model.GroupSpec{}, err
	}
	if pk != configPK {
		return model.GroupSpec{}, fmt.Errorf("pk must be %q, got %q", configPK, pk)
	}
	name, ok := strings.CutPrefix(sk, groupSKPrefix)
	if !ok {
		return model.GroupSpec{}, fmt.Errorf("sk must start with %q, got %q", groupSKPrefix, sk)
	}
	g := model.GroupSpec{Name: name}
	if g.StartCron, err = stringAttribute(raw, "start_cron", false); err != nil {
		return model.GroupSpec{}, err
	}
	if g.StopCron, err = stringAttribute(raw, "stop_cron", false); err != nil {
		return model.GroupSpec{}, err
	}
	override, err := stringAttribute(raw, "override", false)
	if err != nil {
		return model.GroupSpec{}, err
	}
	g.Override = model.Override(override)
	if value, ok := raw["override_expires_at"]; ok {
		n, ok := value.(*types.AttributeValueMemberN)
		if !ok {
			return model.GroupSpec{}, fmt.Errorf("attribute override_expires_at must be a Number")
		}
		if !positiveDecimal.MatchString(n.Value) {
			return model.GroupSpec{}, fmt.Errorf("attribute override_expires_at must be a positive decimal integer")
		}
		g.OverrideExpiresAt, err = strconv.ParseInt(n.Value, 10, 64)
		if err != nil {
			return model.GroupSpec{}, fmt.Errorf("attribute override_expires_at is out of range: %w", err)
		}
	}
	return g, nil
}

func stringAttribute(raw map[string]types.AttributeValue, name string, required bool) (string, error) {
	value, ok := raw[name]
	if !ok {
		if required {
			return "", fmt.Errorf("attribute %s is required", name)
		}
		return "", nil
	}
	text, ok := value.(*types.AttributeValueMemberS)
	if !ok {
		return "", fmt.Errorf("attribute %s must be a String", name)
	}
	return text.Value, nil
}

func encodeGroup(g model.GroupSpec) map[string]types.AttributeValue {
	k := groupKey(g.Name)
	item := marshalKey(k)
	if g.StartCron != "" {
		item["start_cron"] = &types.AttributeValueMemberS{Value: g.StartCron}
	}
	if g.StopCron != "" {
		item["stop_cron"] = &types.AttributeValueMemberS{Value: g.StopCron}
	}
	if g.Override != "" {
		item["override"] = &types.AttributeValueMemberS{Value: string(g.Override)}
	}
	if g.OverrideExpiresAt != 0 {
		item["override_expires_at"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(g.OverrideExpiresAt, 10)}
	}
	return item
}

func marshalKey(key itemKey) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: key.PK},
		"sk": &types.AttributeValueMemberS{Value: key.SK},
	}
}
