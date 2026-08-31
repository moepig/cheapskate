package state

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/core/model"
)

// PendingOperation は AWS の変更操作を実行する前に永続化する意図を表す。
// CompleteOperation が同じ ID を条件に完了へ進めるため、別の実行による上書きを防ぐ。
type PendingOperation struct {
	ID        string
	Action    model.Action
	Desired   model.DesiredState
	Observed  model.ObservedState
	StartedAt string
}

// 未完了の変更操作がない場合だけ、操作意図を記録する。
func (s *Store) BeginOperation(ctx context.Context, resourceID string, op PendingOperation) error {
	empty := ""
	return s.updateStatus(ctx, resourceID, StatusPatch{
		PendingOperationID: Set(op.ID),
		PendingAction:      Set(op.Action),
		PendingDesired:     Set(op.Desired),
		PendingObserved:    Set(op.Observed),
		PendingStartedAt:   Set(op.StartedAt),
	}, "attribute_not_exists(#pending_operation_id) OR #pending_operation_id = :empty",
		map[string]string{"#pending_operation_id": "pending_operation_id"},
		map[string]types.AttributeValue{":empty": &types.AttributeValueMemberS{Value: empty}})
}

// 同じ操作 ID の意図を監査証跡へ進め、以前のエラーを同じ更新で解除する。
func (s *Store) CompleteOperation(ctx context.Context, resourceID string, op PendingOperation) error {
	empty := ""
	return s.updateStatus(ctx, resourceID, StatusPatch{
		ObservedState:      Set(op.Observed),
		LastAction:         Set(op.Action),
		LastDesired:        Set(op.Desired),
		LastActionAt:       Set(op.StartedAt),
		LastError:          Set(empty),
		LastErrorAt:        Set(empty),
		PendingOperationID: Set(empty),
		PendingAction:      Set(model.ActionNone),
		PendingDesired:     Set(model.DesiredNone),
		PendingObserved:    Set(model.ObservedState("")),
		PendingStartedAt:   Set(empty),
	}, "#pending_operation_id = :operation_id",
		map[string]string{"#pending_operation_id": "pending_operation_id"},
		map[string]types.AttributeValue{
			":operation_id": &types.AttributeValueMemberS{Value: op.ID},
		})
}

// AbandonOperation は実行に至らなかった、または収束を確認できなかった操作意図を消す。
func (s *Store) AbandonOperation(ctx context.Context, resourceID, operationID string) error {
	empty := ""
	return s.updateStatus(ctx, resourceID, StatusPatch{
		PendingOperationID: Set(empty),
		PendingAction:      Set(model.ActionNone),
		PendingDesired:     Set(model.DesiredNone),
		PendingObserved:    Set(model.ObservedState("")),
		PendingStartedAt:   Set(empty),
	}, "#pending_operation_id = :operation_id",
		map[string]string{"#pending_operation_id": "pending_operation_id"},
		map[string]types.AttributeValue{":operation_id": &types.AttributeValueMemberS{Value: operationID}})
}
