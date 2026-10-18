package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/state"
	"cheapskate/internal/wire"
)

type serviceOptions struct {
	table    string
	profile  string
	region   string
	location *time.Location
}

type serviceFactory func(context.Context, serviceOptions) (*groups.Service, error)

// 接続設定とタイムゾーンからグループ操作のサービスを生成する。AWS 設定を読み込めない場合はエラーを返す。
func newGroupService(ctx context.Context, options serviceOptions) (*groups.Service, error) {
	var awsOptions []func(*config.LoadOptions) error
	if options.profile != "" {
		awsOptions = append(awsOptions, config.WithSharedConfigProfile(options.profile))
	}
	if options.region != "" {
		awsOptions = append(awsOptions, config.WithRegion(options.region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, awsOptions...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return groups.New(
		state.New(dynamodb.NewFromConfig(cfg), options.table),
		wire.Discoverer(cfg, nil),
		wire.Describers(cfg),
		options.location,
	), nil
}
