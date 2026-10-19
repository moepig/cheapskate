package wire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

// 合成ルートから実際の SDK をローカル HTTP サーバーへ接続し、JSON/XML の変換、署名、resource ID フィルタ、およびページ継続を確認する。
// ARN から名前を解決し、NotFound はエイリアスの照会へ進む。権限エラーは探索全体のエラーとして返る必要がある。
func TestDiscovererThroughAWSSDK(t *testing.T) {
	const prefix = "arn:aws:rds:ap-northeast-1:123456789012:cluster:"
	const resourceID = "cluster-ANPAJ4AE5446DAEXAMPLE"
	for _, tc := range []struct {
		denied   bool
		arnRef   string
		aliasRef string
	}{
		{arnRef: "dev-aurora"}, {denied: true, arnRef: "dev-aurora"}, {arnRef: resourceID},
		{arnRef: "dev-aurora", aliasRef: strings.ToLower(resourceID)},
	} {
		t.Run(fmt.Sprintf("access_denied=%t/arn=%s/alias=%s", tc.denied, tc.arnRef, tc.aliasRef), func(t *testing.T) {
			denied := tc.denied
			aliasRef := tc.aliasRef
			if aliasRef == "" {
				aliasRef = resourceID
			}
			var mu sync.Mutex
			var requests []string
			record := func(request string) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, request)
			}
			writeCluster := func(w http.ResponseWriter) {
				fmt.Fprintf(w, `<DescribeDBClustersResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBClustersResult><DBClusters><DBCluster><DBClusterIdentifier>dev-aurora</DBClusterIdentifier><DBClusterArn>%s%s</DBClusterArn><DbClusterResourceId>%s</DbClusterResourceId><Engine>aurora-postgresql</Engine><TagList><Tag><Key>cheapskate:group</Key><Value>dev</Value></Tag></TagList></DBCluster></DBClusters></DescribeDBClustersResult></DescribeDBClustersResponse>`, prefix, tc.arnRef, resourceID)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Contains(t, r.Header.Get("Authorization"), "Credential=test/")
				if r.Header.Get("X-Amz-Target") == "ResourceGroupsTaggingAPI_20170126.GetResources" {
					record("GetResources")
					var input struct {
						TagFilters []struct{ Key string }
					}
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) || !assert.Len(t, input.TagFilters, 1) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, model.GroupTagKey, input.TagFilters[0].Key)
					w.Header().Set("Content-Type", "application/x-amz-json-1.1")
					fmt.Fprintf(w, `{"ResourceTagMappingList":[{"ResourceARN":%q,"Tags":[{"Key":"cheapskate:group","Value":"stale"}]},{"ResourceARN":%q,"Tags":[{"Key":"cheapskate:group","Value":"stale"}]}]}`, prefix+"dev-aurora", prefix+aliasRef)
					return
				}
				if !assert.NoError(t, r.ParseForm()) || !assert.Equal(t, "DescribeDBClusters", r.Form.Get("Action")) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/xml")
				if ref := r.Form.Get("DBClusterIdentifier"); ref != "" {
					if ref == prefix+aliasRef {
						record("DescribeDBClusters:arn")
					} else {
						record("DescribeDBClusters:name")
					}
					assert.Empty(t, r.Form.Get("Filters.Filter.1.Name"))
					if ref == prefix+tc.arnRef {
						writeCluster(w)
						return
					}
					assert.Contains(t, []string{aliasRef, prefix + aliasRef}, ref)
					w.WriteHeader(http.StatusNotFound)
					io.WriteString(w, `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>DBClusterNotFoundFault</Code><Message>Cluster not found</Message></Error><RequestId>not-found</RequestId></ErrorResponse>`)
					return
				}
				assert.Equal(t, "db-cluster-resource-id", r.Form.Get("Filters.Filter.1.Name"))
				assert.Equal(t, aliasRef, r.Form.Get("Filters.Filter.1.Values.Value.1"))
				marker := r.Form.Get("Marker")
				record("DescribeDBClusters:filter:" + marker)
				if denied {
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>Resource ID query denied</Message></Error><RequestId>denied</RequestId></ErrorResponse>`)
					return
				}
				if marker == "" {
					io.WriteString(w, `<DescribeDBClustersResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><DescribeDBClustersResult><DBClusters/><Marker>next</Marker></DescribeDBClustersResult></DescribeDBClustersResponse>`)
					return
				}
				assert.Equal(t, "next", marker)
				writeCluster(w)
			}))
			defer server.Close()
			cfg := aws.Config{
				Region: "ap-northeast-1", BaseEndpoint: aws.String(server.URL), HTTPClient: server.Client(), RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
				}),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resources, err := Discoverer(cfg, slog.New(slog.DiscardHandler)).Discover(ctx)

			if denied {
				require.Error(t, err)
				assert.Nil(t, resources)
				var apiErr smithy.APIError
				require.True(t, errors.As(err, &apiErr))
				assert.Equal(t, "AccessDenied", apiErr.ErrorCode())
				assert.ErrorContains(t, err, prefix+aliasRef)
			} else {
				require.NoError(t, err)
				assert.Equal(t, map[string]model.Resource{prefix + tc.arnRef: {
					Type: model.TypeRdsCluster, Ref: "dev-aurora", ARN: prefix + tc.arnRef, Tags: map[string]string{model.GroupTagKey: "dev"},
				}}, resources)
			}
			mu.Lock()
			defer mu.Unlock()
			want := []string{"GetResources", "DescribeDBClusters:arn"}
			if tc.arnRef != resourceID {
				want = append(want, "DescribeDBClusters:name", "DescribeDBClusters:filter:")
			}
			if !denied && tc.arnRef != resourceID {
				want = append(want, "DescribeDBClusters:filter:next")
			}
			assert.Equal(t, want, requests)
		})
	}
}
