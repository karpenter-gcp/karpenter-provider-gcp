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

package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	containerv1 "google.golang.org/api/container/v1"
	"google.golang.org/api/googleapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/metadata"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/offerings/unavailableofferings"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/utils"
)

const selfHostedStartupScript = "#!/bin/bash\nset -euo pipefail\necho 'nodeup: bootstrap'\n"

func selfHostedContext() context.Context {
	return options.ToContext(context.Background(), &options.Options{
		ProvisionMode: options.ProvisionModeSelfHosted,
	})
}

func selfHostedNodeClass() *v1alpha1.GCENodeClass {
	return &v1alpha1.GCENodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "self-hosted"},
		Spec: v1alpha1.GCENodeClassSpec{
			StartupScript: ptr.To(selfHostedStartupScript),
			Disks:         []v1alpha1.Disk{{SizeGiB: 100, Category: "pd-balanced", Boot: true}},
			KubeletConfiguration: &v1alpha1.KubeletConfiguration{
				MaxPods: ptr.To(int32(110)),
			},
			Metadata: map[string]string{
				"kops-k8s-io-instance-group-name": "nodes-us-central1-a",
			},
			NetworkConfig: &v1alpha1.NetworkConfig{
				Subnetwork: "projects/test-project/regions/us-central1/subnetworks/nodes",
			},
			NetworkTags: []v1alpha1.NetworkTag{"poc0006-k8s-local-node"},
		},
		Status: v1alpha1.GCENodeClassStatus{
			Images: []v1alpha1.Image{{
				SourceImage: "projects/test-project/global/images/kops-node",
				Requirements: []corev1.NodeSelectorRequirement{{
					Key:      corev1.LabelArchStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"amd64"},
				}},
			}},
		},
	}
}

func selfHostedLaunchImage() launchImage {
	return launchImage{SourceImage: "projects/test-project/global/images/kops-node"}
}

// selfHostedFakeProvider fails the test on any API call — building a self-hosted
// instance must not make any Compute or Container API calls.
func selfHostedFakeProvider(t *testing.T) *DefaultProvider {
	t.Helper()
	p := newFakeComputeProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected API call in self-hosted instance build: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	p.clusterName = "poc0006.k8s.local"
	p.clusterLocation = "us-central1"
	p.computeDefaultSA = "123-compute@developer.gserviceaccount.com"
	return p
}

func TestBuildInstance_SelfHostedRendersOnlyOwnedMetadataAndTags(t *testing.T) {
	t.Parallel()

	p := selfHostedFakeProvider(t)
	nodeClaim := onDemandNodeClaim()
	nodeClaim.Labels = map[string]string{karpv1.NodePoolLabelKey: "my-pool"}

	instance, err := p.buildInstance(selfHostedContext(), nodeClaim, selfHostedNodeClass(), amd64InstanceType(),
		nil, nil, "us-central1-a", "karpenter-test", karpv1.CapacityTypeOnDemand, nil, 2, "", selfHostedLaunchImage())
	require.NoError(t, err)

	// Metadata: exactly startup-script (verbatim), cluster-name (exact option value),
	// and spec.metadata — no GKE bootstrap keys.
	require.ElementsMatch(t, []string{"cluster-name", "startup-script", "kops-k8s-io-instance-group-name"}, metadataKeys(instance))
	require.Equal(t, selfHostedStartupScript, metadataValue(instance.Metadata, "startup-script"))
	require.Equal(t, "poc0006.k8s.local", metadataValue(instance.Metadata, "cluster-name"))
	require.Equal(t, "nodes-us-central1-a", metadataValue(instance.Metadata, "kops-k8s-io-instance-group-name"))

	// Tags: only NodeClass network tags, no gke-* firewall tags.
	require.Equal(t, []string{"poc0006-k8s-local-node"}, instance.Tags.Items)

	// Labels: karpenter identity + sanitized cluster GC labels + local SSD count, no goog-gke-*.
	for key := range instance.Labels {
		require.False(t, strings.HasPrefix(key, "goog-gke-"), "GKE-owned label %q must not be set in self-hosted mode", key)
	}
	require.Equal(t, "my-pool", instance.Labels[utils.SanitizeGCELabelValue(utils.LabelNodePoolKey)])
	require.Equal(t, "self-hosted", instance.Labels[utils.SanitizeGCELabelValue(utils.LabelGCENodeClassKey)])
	require.Equal(t, "poc0006-k8s-local", instance.Labels[utils.SanitizeGCELabelValue(utils.LabelClusterNameKey)])
	require.Equal(t, "us-central1", instance.Labels[utils.SanitizeGCELabelValue(utils.LabelClusterLocationKey)])
	require.Equal(t, "2", instance.Labels[utils.SanitizeGCELabelValue(v1alpha1.LabelInstanceLocalSsdCount)])

	// Persistent disks inherit the same sanitized cluster identity label.
	require.Len(t, instance.Disks, 1)
	require.Equal(t, "poc0006-k8s-local", instance.Disks[0].InitializeParams.Labels[utils.LabelClusterNameKey])

	// Network: omitted so GCE infers the VPC from the canonical subnetwork; private by
	// default with no alias ranges when podRange is empty.
	require.Len(t, instance.NetworkInterfaces, 1)
	iface := instance.NetworkInterfaces[0]
	require.Empty(t, iface.Network)
	require.Equal(t, "projects/test-project/regions/us-central1/subnetworks/nodes", iface.Subnetwork)
	require.Empty(t, iface.AccessConfigs, "nodes must be private by default in self-hosted mode")
	require.Contains(t, iface.ForceSendFields, "AccessConfigs")
	require.Empty(t, iface.AliasIpRanges)
}

func TestBuildInstance_SelfHostedSpotKeepsSchedulingWithoutGKELabel(t *testing.T) {
	t.Parallel()

	p := selfHostedFakeProvider(t)
	nodeClaim := spotOrOnDemandNodeClaim()
	nodeClaim.Labels = map[string]string{karpv1.NodePoolLabelKey: "my-pool"}

	instance, err := p.buildInstance(selfHostedContext(), nodeClaim, selfHostedNodeClass(), amd64InstanceType(),
		nil, nil, "us-central1-a", "karpenter-test", karpv1.CapacityTypeSpot, nil, 0, "", selfHostedLaunchImage())
	require.NoError(t, err)

	require.Equal(t, "SPOT", instance.Scheduling.ProvisioningModel)
	require.True(t, instance.Scheduling.Preemptible)
	require.NotContains(t, instance.Labels, "goog-gke-node-pool-provisioning-model")
}

func TestBuildInstance_SelfHostedWithoutStartupScriptOmitsKey(t *testing.T) {
	t.Parallel()

	// startupScript is optional: images with baked-in bootstrap need none. The
	// startup-script key must be absent, while cluster-name still carries the exact
	// CLUSTER_NAME option value.
	p := selfHostedFakeProvider(t)
	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.StartupScript = nil

	instance, err := p.buildInstance(selfHostedContext(), onDemandNodeClaim(), nodeClass, amd64InstanceType(),
		nil, nil, "us-central1-a", "karpenter-test", karpv1.CapacityTypeOnDemand, nil, 0, "", selfHostedLaunchImage())
	require.NoError(t, err)

	require.NotContains(t, metadataKeys(instance), "startup-script")
	require.Equal(t, "poc0006.k8s.local", metadataValue(instance.Metadata, "cluster-name"))
}

func TestBuildInstance_SelfHostedRejectsMetadataOverGCEByteLimit(t *testing.T) {
	t.Parallel()

	// Oversized payloads must fail before Instances.Insert with the offending key named.
	p := selfHostedFakeProvider(t)
	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.StartupScript = ptr.To(strings.Repeat("x", metadata.MaxMetadataValueBytes+1))

	_, err := p.buildInstance(selfHostedContext(), onDemandNodeClaim(), nodeClass, amd64InstanceType(),
		nil, nil, "us-central1-a", "karpenter-test", karpv1.CapacityTypeOnDemand, nil, 0, "", selfHostedLaunchImage())

	require.ErrorContains(t, err, `metadata value for key "startup-script"`)
	require.ErrorContains(t, err, "per-value limit of 262144 bytes")
}

func TestBuildInstance_GKERejectsMetadataOverGCEByteLimit(t *testing.T) {
	t.Parallel()

	p := makeProvider()
	nodeClass := &v1alpha1.GCENodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "gke-oversized"},
		Spec: v1alpha1.GCENodeClassSpec{
			Metadata: map[string]string{
				"custom-payload": strings.Repeat("y", metadata.MaxMetadataValueBytes+1),
			},
		},
	}

	_, err := p.buildInstance(context.Background(), onDemandNodeClaim(), nodeClass, makeNonGPUIT(),
		makeSourceMetadata("max-pods-per-node=110"),
		makeCluster("projects/p/global/networks/my-vpc", "regions/us-central1/subnetworks/my-subnet", "pods", false),
		"us-central1-a", "karpenter-test", karpv1.CapacityTypeOnDemand, nil, 0, "pods", testLaunchImage(nodeClass))

	require.ErrorContains(t, err, `metadata value for key "custom-payload"`)
	require.ErrorContains(t, err, "per-value limit of 262144 bytes")
}

func TestSetupSelfHostedNetworkInterfaces_RequiresSubnetwork(t *testing.T) {
	t.Parallel()

	p := &DefaultProvider{}
	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.NetworkConfig = nil

	_, err := p.setupSelfHostedNetworkInterfaces(nodeClass, "")

	require.ErrorContains(t, err, "spec.networkConfig.subnetwork is required in self-hosted mode")
}

func TestSetupSelfHostedNetworkInterfaces_ExplicitPublicAndAliasRange(t *testing.T) {
	t.Parallel()

	p := selfHostedFakeProvider(t)
	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.NetworkConfig.EnablePrivateNodes = ptr.To(false)
	nodeClass.Spec.GVNIC = &v1alpha1.VirtualNIC{Enabled: true}

	ifaces, err := p.setupSelfHostedNetworkInterfaces(nodeClass, "pods")
	require.NoError(t, err)

	require.Len(t, ifaces, 1)
	require.Equal(t, "GVNIC", ifaces[0].NicType)
	require.Equal(t, []*compute.AccessConfig{{Type: "ONE_TO_ONE_NAT", Name: "External NAT"}}, ifaces[0].AccessConfigs)
	require.Equal(t, []*compute.AliasIpRange{{IpCidrRange: "/24", SubnetworkRangeName: "pods"}}, ifaces[0].AliasIpRanges)
}

func TestSetupSelfHostedNetworkInterfaces_CanonicalizesPrimaryAndAdditionalNICs(t *testing.T) {
	t.Parallel()

	p := selfHostedFakeProvider(t)
	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.NetworkConfig.Subnetwork = "nodes"
	nodeClass.Spec.NetworkConfig.AdditionalNetworkInterfaces = []v1alpha1.AdditionalNetworkInterface{
		{Subnetwork: "secondary-local"},
		{Network: "projects/host-project/global/networks/shared-vpc", Subnetwork: "secondary-shared"},
	}

	ifaces, err := p.setupSelfHostedNetworkInterfaces(nodeClass, "")
	require.NoError(t, err)
	require.Len(t, ifaces, 3)

	// Primary bare name is canonicalized into the operator project; Network is omitted so GCE infers it.
	require.Empty(t, ifaces[0].Network)
	require.Equal(t, "projects/test-project/regions/us-central1/subnetworks/nodes", ifaces[0].Subnetwork)

	// Additional NIC without explicit Network omits Network and canonicalizes against operator defaults.
	require.Empty(t, ifaces[1].Network)
	require.Equal(t, "projects/test-project/regions/us-central1/subnetworks/secondary-local", ifaces[1].Subnetwork)

	// Additional NIC with Shared VPC Network preserves Network and resolves bare subnetwork in the host project.
	require.Equal(t, "projects/host-project/global/networks/shared-vpc", ifaces[2].Network)
	require.Equal(t, "projects/host-project/regions/us-central1/subnetworks/secondary-shared", ifaces[2].Subnetwork)
}

type fakeSelfHostedSubnetProvider struct {
	emptySubnetProvider
	counts      map[string]int64
	gotNetwork  string
	gotSubnet   string
	invalidated []string
}

func (f *fakeSelfHostedSubnetProvider) GetFreeIPCounts(_ context.Context, network, subnetwork string) (map[string]int64, error) {
	f.gotNetwork = network
	f.gotSubnet = subnetwork
	return f.counts, nil
}

func (f *fakeSelfHostedSubnetProvider) Invalidate(_, subnetwork string) {
	f.invalidated = append(f.invalidated, subnetwork)
}

func TestPodRangeFreeIPs_SelfHostedSkipsGKEAndQueriesSubnetProvider(t *testing.T) {
	t.Parallel()

	subnetProv := &fakeSelfHostedSubnetProvider{
		counts: map[string]int64{"pods-a": 512, "pods-b": 4096},
	}
	p := &DefaultProvider{
		// gkeProvider is nil: any GetClusterConfig call in self-hosted mode would panic.
		subnetProvider: subnetProv,
	}
	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.SubnetRangeNames = []string{"pods-a", "pods-b"}

	counts := p.podRangeFreeIPs(selfHostedContext(), nodeClass)
	require.Equal(t, map[string]int64{"pods-a": 512, "pods-b": 4096}, counts)
	require.Empty(t, subnetProv.gotNetwork)
	require.Equal(t, "projects/test-project/regions/us-central1/subnetworks/nodes", subnetProv.gotSubnet)
}

func TestCreateInstanceWithPodRanges_SelfHostedRanksAndFallsBack(t *testing.T) {
	t.Parallel()

	var attemptedRanges []string
	p := newFakeComputeProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, &compute.Operation{Name: "op-done", Status: "DONE"})
		case http.MethodPost:
			var inst compute.Instance
			if err := json.NewDecoder(r.Body).Decode(&inst); err != nil {
				t.Errorf("decode Compute request: %v", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if len(inst.NetworkInterfaces) != 1 || len(inst.NetworkInterfaces[0].AliasIpRanges) != 1 {
				t.Errorf("expected 1 NIC with 1 alias IP range, got %+v", inst.NetworkInterfaces)
				http.Error(w, "unexpected NIC layout", http.StatusInternalServerError)
				return
			}
			rangeName := inst.NetworkInterfaces[0].AliasIpRanges[0].SubnetworkRangeName
			attemptedRanges = append(attemptedRanges, rangeName)
			if rangeName == "pods-b" {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": map[string]any{"errors": []map[string]string{{"reason": "IP_SPACE_EXHAUSTED"}}}})
				return
			}
			writeJSON(w, &compute.Operation{Name: "op-done", Status: "DONE"})
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	subnetProv := &fakeSelfHostedSubnetProvider{}
	p.subnetProvider = subnetProv
	p.clusterName = "poc0006.k8s.local"
	p.clusterLocation = "us-central1"
	p.computeDefaultSA = "123-compute@developer.gserviceaccount.com"

	nodeClass := selfHostedNodeClass()
	nodeClass.Spec.SubnetRangeNames = []string{"pods-a", "pods-b"}

	inst, _, _, err := p.createInstanceWithPodRanges(selfHostedContext(), onDemandNodeClaim(), nodeClass, amd64InstanceType(),
		nil, nil, "us-central1-a", "karpenter-test", karpv1.CapacityTypeOnDemand, nil, 0,
		map[string]int64{"pods-a": 512, "pods-b": 4096}, selfHostedLaunchImage())
	require.NoError(t, err)
	require.Equal(t, []string{"pods-b", "pods-a"}, attemptedRanges)
	require.Equal(t, "pods-a", inst.NetworkInterfaces[0].AliasIpRanges[0].SubnetworkRangeName)
	require.Equal(t, []string{"projects/test-project/regions/us-central1/subnetworks/nodes"}, subnetProv.invalidated)
}

type selfHostedOnlyGKEProvider struct {
	fakeGKEProvider
	t *testing.T
}

func (f *selfHostedOnlyGKEProvider) GetClusterConfig(context.Context) (*containerv1.Cluster, error) {
	f.t.Error("GetClusterConfig must not be called in self-hosted mode")
	return nil, context.Canceled
}

func (f *selfHostedOnlyGKEProvider) GetServerConfig(context.Context) (*containerv1.ServerConfig, error) {
	f.t.Error("GetServerConfig must not be called in self-hosted mode")
	return nil, context.Canceled
}

// TestCreate_SelfHostedWithNilGKEProviders mirrors operator.NewOperator wiring where
// nodePoolTemplateProvider and bootstrapProvider are nil in self-hosted mode.
func TestCreate_SelfHostedWithNilGKEProviders(t *testing.T) {
	t.Parallel()

	var inserted *compute.Instance
	p := newFakeComputeProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/aggregated/instances"):
			writeJSON(w, &compute.InstanceAggregatedList{})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/instances/"):
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, &googleapi.Error{Code: http.StatusNotFound, Message: "not found"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/instances"):
			var inst compute.Instance
			if err := json.NewDecoder(r.Body).Decode(&inst); err != nil {
				t.Errorf("decode Compute request: %v", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			inserted = &inst
			writeJSON(w, &compute.Operation{Name: "op-create", Status: "DONE"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/"):
			writeJSON(w, &compute.Operation{Name: "op-create", Status: "DONE"})
		default:
			t.Errorf("unexpected Compute API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p.clusterName = "poc0006.k8s.local"
	p.clusterLocation = "us-central1"
	p.computeDefaultSA = "123-compute@developer.gserviceaccount.com"
	p.gkeProvider = &selfHostedOnlyGKEProvider{fakeGKEProvider: fakeGKEProvider{zones: []string{"us-central1-a"}}, t: t}
	p.instanceTypeProvider = fakeInstanceTypeProvider{}
	p.subnetProvider = &fakeSelfHostedSubnetProvider{}
	p.unavailableOfferings = unavailableofferings.NewUnavailableOfferings()
	p.nodePoolTemplateProvider = nil
	p.bootstrapProvider = nil

	nodeClaim := onDemandNodeClaim()
	nodeClaim.Name = "self-hosted-wire"
	nodeClaim.Labels = map[string]string{karpv1.NodePoolLabelKey: "my-pool"}
	it := spotAndOnDemandIT("n2-standard-4", 0.05, 0.2)
	it.Requirements.Add(scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"))

	nodeClass := selfHostedNodeClass()
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionTypeImagesReady)
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionTypeConfigurationReady)

	inst, err := p.Create(selfHostedContext(), nodeClass, nodeClaim, []*cloudprovider.InstanceType{it})
	require.NoError(t, err)
	require.NotNil(t, inserted)
	require.Equal(t, "karpenter-self-hosted-wire", inst.Name)
	require.Equal(t, selfHostedStartupScript, metadataValue(inserted.Metadata, "startup-script"))
	require.Equal(t, "poc0006.k8s.local", metadataValue(inserted.Metadata, "cluster-name"))
	require.Empty(t, inserted.NetworkInterfaces[0].Network)
	require.Equal(t, "projects/test-project/regions/us-central1/subnetworks/nodes", inserted.NetworkInterfaces[0].Subnetwork)
}

func TestAdopt_IgnoresForeignClusterInstance(t *testing.T) {
	t.Parallel()

	clusterNameMeta := func(name string) *compute.Metadata {
		return &compute.Metadata{Items: []*compute.MetadataItems{
			{Key: metadata.ClusterNameKey, Value: ptr.To(name)},
		}}
	}
	foreignName := &compute.Instance{
		Name:     "karpenter-claim-1",
		Zone:     "zones/us-central1-a",
		Metadata: clusterNameMeta("other.k8s.local"),
	}
	foreignLocation := &compute.Instance{
		Name:     "karpenter-claim-1",
		Zone:     "zones/us-central1-c",
		Metadata: clusterNameMeta("poc0006.k8s.local"),
		Labels:   map[string]string{utils.LabelClusterLocationKey: "us-central1-c"},
	}
	owned := &compute.Instance{
		Name:     "karpenter-claim-1",
		Zone:     "zones/us-central1-b",
		Metadata: clusterNameMeta("poc0006.k8s.local"),
		Labels:   map[string]string{utils.LabelClusterLocationKey: "us-central1"},
	}

	p := newFakeComputeProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/aggregated/instances"):
			writeJSON(w, &compute.InstanceAggregatedList{
				Items: map[string]compute.InstancesScopedList{
					"zones/us-central1-a": {Instances: []*compute.Instance{foreignName}},
					"zones/us-central1-c": {Instances: []*compute.Instance{foreignLocation}},
					"zones/us-central1-b": {Instances: []*compute.Instance{owned}},
				},
			})
		case strings.Contains(r.URL.Path, "/zones/us-central1-a/instances/"):
			writeJSON(w, foreignName)
		case strings.Contains(r.URL.Path, "/zones/us-central1-c/instances/"):
			writeJSON(w, foreignLocation)
		case strings.Contains(r.URL.Path, "/zones/us-central1-b/instances/"):
			writeJSON(w, owned)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	p.clusterName = "poc0006.k8s.local"
	p.clusterLocation = "us-central1"

	found, err := p.findInstanceByNodeClaim(context.Background(), &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim-1"}})
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, "zones/us-central1-b", found.Zone)

	_, exists, err := p.isInstanceExists(context.Background(), "us-central1-a", "karpenter-claim-1")
	require.NoError(t, err)
	require.False(t, exists, "foreign cluster-name VM must not be adopted")

	_, exists, err = p.isInstanceExists(context.Background(), "us-central1-c", "karpenter-claim-1")
	require.NoError(t, err)
	require.False(t, exists, "same-name VM from another cluster location must not be adopted")

	inst, exists, err := p.isInstanceExists(context.Background(), "us-central1-b", "karpenter-claim-1")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "karpenter-claim-1", inst.Name)
}

func TestBuildInstance_GKEModeExplicitOptionAndSpotLabel(t *testing.T) {
	t.Parallel()

	p := makeProvider()
	nodeClaim := spotOrOnDemandNodeClaim()
	nodeClaim.Labels = map[string]string{karpv1.NodePoolLabelKey: "my-pool"}
	nodeClass := &v1alpha1.GCENodeClass{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	gkeCtx := options.ToContext(context.Background(), &options.Options{
		ProvisionMode: options.ProvisionModeGKE,
	})

	instance, err := p.buildInstance(gkeCtx, nodeClaim, nodeClass, makeNonGPUIT(),
		makeSourceMetadata("max-pods-per-node=110"),
		makeCluster("projects/p/global/networks/my-vpc", "regions/us-central1/subnetworks/my-subnet", "pods", false),
		"us-central1-a", "karpenter-test", karpv1.CapacityTypeSpot, nil, 0, "pods", testLaunchImage(nodeClass))
	require.NoError(t, err)

	require.Contains(t, instance.Tags.Items, "gke-"+p.clusterName+"-node")
	require.Contains(t, instance.Labels, "goog-gke-node")
	require.Equal(t, "spot", instance.Labels["goog-gke-node-pool-provisioning-model"])
}
