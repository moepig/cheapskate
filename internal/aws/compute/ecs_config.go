package compute

import (
	"fmt"
	"strconv"

	"cheapskate/internal/core/model"
)

type ecsBoundsMode string

const (
	ecsBoundsModeTags      ecsBoundsMode = "tags"
	ecsBoundsModeScheduled ecsBoundsMode = "scheduled"
)

type ecsConfig struct {
	desired    int32
	minimum    int32
	maximum    int32
	boundsMode ecsBoundsMode
}

// リソースタグを解釈し、起動台数とスケーリング上限をグループの登録上限で検証する。不正な設定はエラーを返す。
func ecsConfigForStart(res model.Resource) (ecsConfig, error) {
	config, err := ecsConfigFromTags(res.Tags)
	if err != nil {
		return ecsConfig{}, err
	}
	if res.EcsMaxCount <= 0 {
		return ecsConfig{}, fmt.Errorf("ecs service %s: group ecs_max_count must be registered before start", res.Ref)
	}
	if config.desired > res.EcsMaxCount {
		return ecsConfig{}, fmt.Errorf("ecs service %s: %s=%d exceeds group ecs_max_count=%d", res.Ref, model.EcsDesiredCountTagKey, config.desired, res.EcsMaxCount)
	}
	if config.maximum > res.EcsMaxCount {
		return ecsConfig{}, fmt.Errorf("ecs service %s: %s=%d exceeds group ecs_max_count=%d", res.Ref, model.EcsScalingMaxTagKey, config.maximum, res.EcsMaxCount)
	}
	return config, nil
}

// リソースタグから設定を取得する。未設定のタグには既定値を適用し、不正な値はエラーを返す。
func ecsConfigFromTags(tags map[string]string) (ecsConfig, error) {
	desired, err := desiredCountFromTags(tags)
	if err != nil {
		return ecsConfig{}, err
	}
	minimum, maximum, err := scalingBoundsFromTags(tags, desired)
	if err != nil {
		return ecsConfig{}, err
	}
	mode := ecsBoundsModeScheduled
	if modeValue, present := tags[model.EcsScalingBoundsModeTagKey]; present {
		mode = ecsBoundsMode(modeValue)
		if mode != ecsBoundsModeTags && mode != ecsBoundsModeScheduled {
			return ecsConfig{}, fmt.Errorf("tag %s=%q must be tags or scheduled", model.EcsScalingBoundsModeTagKey, modeValue)
		}
	}
	return ecsConfig{desired: desired, minimum: minimum, maximum: maximum, boundsMode: mode}, nil
}

// model.EcsDesiredCountTagKey を正整数として返す。未設定の場合は 1、不正な値の場合はエラーを返す。
func desiredCountFromTags(tags map[string]string) (int32, error) {
	n, ok, err := tagInt32(tags, model.EcsDesiredCountTagKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 1, nil
	}
	if n <= 0 {
		return 0, fmt.Errorf("tag %s=%d must be positive", model.EcsDesiredCountTagKey, n)
	}
	return n, nil
}

// スケーリング上下限を返す。未設定のタグにはそれぞれ desiredCount を適用し、minimum <= desiredCount <= maximum を満たさない場合はエラーを返す。
func scalingBoundsFromTags(tags map[string]string, desiredCount int32) (minimum, maximum int32, err error) {
	minimum = desiredCount
	if n, ok, err := tagInt32(tags, model.EcsScalingMinTagKey); err != nil {
		return 0, 0, err
	} else if ok {
		minimum = n
	}
	maximum = desiredCount
	if n, ok, err := tagInt32(tags, model.EcsScalingMaxTagKey); err != nil {
		return 0, 0, err
	} else if ok {
		maximum = n
	}
	if desiredCount < minimum || desiredCount > maximum {
		return 0, 0, fmt.Errorf("tags must satisfy %s <= %s <= %s, got %d <= %d <= %d",
			model.EcsScalingMinTagKey, model.EcsDesiredCountTagKey, model.EcsScalingMaxTagKey,
			minimum, desiredCount, maximum)
	}
	return minimum, maximum, nil
}

// tags[key] を非負の int32 として返す。タグがなければ ok=false、空文字または不正な整数ならエラーを返す。
func tagInt32(tags map[string]string, key string) (n int32, ok bool, err error) {
	v, present := tags[key]
	if !present {
		return 0, false, nil
	}
	if v == "" {
		return 0, false, fmt.Errorf("tag %s must not be empty", key)
	}
	parsed, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, false, fmt.Errorf("tag %s=%q is not an integer", key, v)
	}
	if parsed < 0 {
		return 0, false, fmt.Errorf("tag %s=%q must not be negative", key, v)
	}
	return int32(parsed), true, nil
}
