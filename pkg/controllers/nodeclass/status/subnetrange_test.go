/*
Copyright 2026 The Karpenter GCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package status

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	containerv1 "google.golang.org/api/container/v1"
	"k8s.io/utils/ptr"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/subnet"
)

type failingGKEProvider struct {
	t *testing.T
}

func (f *failingGKEProvider) ResolveClusterZones(_ context.Context) ([]string, error) {
	f.t.Error("ResolveClusterZones must not be called")
	return nil, nil
}

func (f *failingGKEProvider) GetClusterConfig(_ context.Context) (*containerv1.Cluster, error) {
	f.t.Error("GetClusterConfig must not be called in self-hosted mode")
	return nil, nil
}

func (f *failingGKEProvider) InvalidateClusterConfig() {}

func (f *failingGKEProvider) GetServerConfig(_ context.Context) (*containerv1.ServerConfig, error) {
	f.t.Error("GetServerConfig must not be called")
	return nil, nil
}

type fakeSubnetProvider struct {
	getFreeIPCounts func(ctx context.Context, network, subnetwork string) (map[string]int64, error)
}

func (f *fakeSubnetProvider) GetFreeIPCounts(ctx context.Context, network, subnetwork string) (map[string]int64, error) {
	return f.getFreeIPCounts(ctx, network, subnetwork)
}

func (f *fakeSubnetProvider) GetNetworkConfig(_ context.Context, _, _ string) (subnet.NetworkConfig, error) {
	return subnet.NetworkConfig{}, nil
}

func (f *fakeSubnetProvider) Invalidate(_, _ string) {}

func TestSubnetRangeReconcile_SelfHosted(t *testing.T) {
	t.Parallel()

	selfHostedCtx := options.ToContext(context.Background(), &options.Options{
		ProvisionMode: options.ProvisionModeSelfHosted,
	})

	t.Run("publishes free IPs for spec.subnetRangeNames without GetClusterConfig", func(t *testing.T) {
		t.Parallel()

		var gotNetwork, gotSubnetwork string
		reconciler := &SubnetRange{
			gkeProvider: &failingGKEProvider{t: t},
			subnetProvider: &fakeSubnetProvider{
				getFreeIPCounts: func(_ context.Context, network, subnetwork string) (map[string]int64, error) {
					gotNetwork, gotSubnetwork = network, subnetwork
					return map[string]int64{"pods-a": 4096, "pods-b": 1024}, nil
				},
			},
		}
		nodeClass := &v1alpha1.GCENodeClass{
			Spec: v1alpha1.GCENodeClassSpec{
				SubnetRangeNames: []string{"pods-a", "pods-b"},
				NetworkConfig:    &v1alpha1.NetworkConfig{Subnetwork: "nodes"},
			},
		}

		res, err := reconciler.Reconcile(selfHostedCtx, nodeClass)
		require.NoError(t, err)
		require.Equal(t, subnetRangeStatusRequeue, res.RequeueAfter)
		require.Empty(t, gotNetwork)
		require.Equal(t, "nodes", gotSubnetwork)
		require.Equal(t, []v1alpha1.SubnetRangeStatus{
			{Name: "pods-a", TotalFreeIP: ptr.To(int64(4096))},
			{Name: "pods-b", TotalFreeIP: ptr.To(int64(1024))},
		}, nodeClass.Status.SubnetRanges)
	})

	t.Run("skips subnet lookup when no pod ranges are configured", func(t *testing.T) {
		t.Parallel()

		reconciler := &SubnetRange{
			gkeProvider: &failingGKEProvider{t: t},
			subnetProvider: &fakeSubnetProvider{
				getFreeIPCounts: func(_ context.Context, _, _ string) (map[string]int64, error) {
					t.Error("GetFreeIPCounts must not be called when no ranges are configured")
					return nil, nil
				},
			},
		}
		nodeClass := &v1alpha1.GCENodeClass{
			Spec: v1alpha1.GCENodeClassSpec{
				NetworkConfig: &v1alpha1.NetworkConfig{Subnetwork: "nodes"},
			},
		}

		res, err := reconciler.Reconcile(selfHostedCtx, nodeClass)
		require.NoError(t, err)
		require.Equal(t, subnetRangeStatusRequeue, res.RequeueAfter)
		require.Empty(t, nodeClass.Status.SubnetRanges)
	})
}
