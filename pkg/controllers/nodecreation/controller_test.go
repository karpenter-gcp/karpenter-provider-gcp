/*
Copyright 2025 The CloudPilot AI Authors.

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

package nodecreation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	containerv1 "google.golang.org/api/container/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/gke"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/instance"
)

const (
	testProject    = "my-project"
	testInstance   = "karpenter-default-abc"
	testProviderID = "gce://" + testProject + "/us-central1-a/" + testInstance
)

var testNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

type recorderStub struct{ published []events.Event }

func (r *recorderStub) Publish(evts ...events.Event) { r.published = append(r.published, evts...) }

func (r *recorderStub) reasons() []string {
	var reasons []string
	for _, e := range r.published {
		reasons = append(reasons, e.Reason)
	}
	return reasons
}

type instanceStub struct {
	instance *instance.Instance
	err      error
}

func (s instanceStub) Get(context.Context, string) (*instance.Instance, error) {
	return s.instance, s.err
}

// gkeStub reports mode until InvalidateClusterConfig is called, then modeAfterInvalidate
// when that is set: GKE keeps reporting the old mode until a mode change finishes.
type gkeStub struct {
	gke.Provider
	mode                gke.NodeCreationMode
	modeAfterInvalidate gke.NodeCreationMode
	invalidations       int
	err                 error
}

func (s *gkeStub) GetClusterConfig(context.Context) (*containerv1.Cluster, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &containerv1.Cluster{NodeCreationConfig: &containerv1.NodeCreationConfig{NodeCreationMode: string(s.mode)}}, nil
}

func (s *gkeStub) InvalidateClusterConfig() {
	s.invalidations++
	if s.modeAfterInvalidate != "" {
		s.mode = s.modeAfterInvalidate
	}
}

func enrolledClaim(enrolledAgo time.Duration) *karpv1.NodeClaim {
	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default-abc",
			UID:  "claim-uid",
			Annotations: map[string]string{
				v1alpha1.AnnotationKubeletEnrolledAt: testNow.Add(-enrolledAgo).Format(time.RFC3339),
			},
		},
		Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Group: apis.Group, Kind: "GCENodeClass", Name: "default"}},
	}
	nc.Status.ProviderID = testProviderID
	nc.StatusConditions().SetTrue(karpv1.ConditionTypeLaunched)
	return nc
}

func launchedInstance() *instance.Instance {
	return &instance.Instance{Name: testInstance, KubeLabels: map[string]string{
		karpv1.NodePoolLabelKey:       "default",
		karpv1.NodeRegisteredLabelKey: "true",
	}}
}

// memClient is the slice of the Kubernetes API the controller uses, in memory.
type memClient struct {
	client.Client
	nodes     map[string]*corev1.Node
	claims    map[string]*karpv1.NodeClaim
	nodeClass *v1alpha1.GCENodeClass
	// hideNodes makes List return no Nodes, as a cache that has not seen them yet.
	hideNodes bool
}

func (m *memClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	switch out := obj.(type) {
	case *v1alpha1.GCENodeClass:
		if m.nodeClass == nil || m.nodeClass.Name != key.Name {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "gcenodeclasses"}, key.Name)
		}
		*out = *m.nodeClass
	case *karpv1.NodeClaim:
		claim, ok := m.claims[key.Name]
		if !ok {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "nodeclaims"}, key.Name)
		}
		*out = *claim.DeepCopy()
	case *corev1.Node:
		node, ok := m.nodes[key.Name]
		if !ok {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, key.Name)
		}
		*out = *node.DeepCopy()
	default:
		return errors.New("unexpected Get type")
	}
	return nil
}

func (m *memClient) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	nodes, ok := list.(*corev1.NodeList)
	if !ok {
		return errors.New("unexpected List type")
	}
	listOpts := &client.ListOptions{}
	listOpts.ApplyOptions(opts)
	providerID, _ := listOpts.FieldSelector.RequiresExactMatch("spec.providerID")
	nodes.Items = nil
	if m.hideNodes {
		return nil
	}
	for _, node := range m.nodes {
		if node.Spec.ProviderID == providerID {
			nodes.Items = append(nodes.Items, *node.DeepCopy())
		}
	}
	return nil
}

func (m *memClient) Create(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
	node := obj.(*corev1.Node)
	if _, exists := m.nodes[node.Name]; exists {
		return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "nodes"}, node.Name)
	}
	m.nodes[node.Name] = node.DeepCopy()
	return nil
}

type harness struct {
	controller *Controller
	client     *memClient
	recorder   *recorderStub
	gke        *gkeStub
}

func newHarness(t *testing.T, nodeCreation string, mode gke.NodeCreationMode, nodes ...*corev1.Node) *harness {
	t.Helper()
	kube := &memClient{
		nodes:     map[string]*corev1.Node{},
		claims:    map[string]*karpv1.NodeClaim{},
		nodeClass: &v1alpha1.GCENodeClass{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
	}
	for _, node := range nodes {
		kube.nodes[node.Name] = node
	}
	h := &harness{client: kube, recorder: &recorderStub{}, gke: &gkeStub{mode: mode}}
	h.controller = NewController(kube, instanceStub{instance: launchedInstance()}, h.gke, h.recorder, testProject, nodeCreation)
	h.controller.now = func() time.Time { return testNow }
	return h
}

// reconcile stores claim as the API server's copy unless the test already did,
// then reconciles it.
func (h *harness) reconcile(claim *karpv1.NodeClaim) (reconcile.Result, error) {
	if _, ok := h.client.claims[claim.Name]; !ok {
		h.client.claims[claim.Name] = claim.DeepCopy()
	}
	return h.controller.Reconcile(context.Background(), claim)
}

func (h *harness) nodes(t *testing.T) []*corev1.Node {
	t.Helper()
	var out []*corev1.Node
	for _, node := range h.client.nodes {
		out = append(out, node)
	}
	return out
}

func TestReconcileIgnoresClaimsThatAreNotEnrolledProviderClaims(t *testing.T) {
	for name, mutate := range map[string]func(*karpv1.NodeClaim){
		"another provider's claim": func(nc *karpv1.NodeClaim) { nc.Spec.NodeClassRef.Group = "other.example" },
		"no node class reference":  func(nc *karpv1.NodeClaim) { nc.Spec.NodeClassRef = nil },
		"not launched":             func(nc *karpv1.NodeClaim) { nc.Status.Conditions = nil },
		"deleting": func(nc *karpv1.NodeClaim) {
			now := metav1.Now()
			nc.DeletionTimestamp = &now
		},
		"registered":            func(nc *karpv1.NodeClaim) { nc.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered) },
		"no enrollment record":  func(nc *karpv1.NodeClaim) { delete(nc.Annotations, v1alpha1.AnnotationKubeletEnrolledAt) },
		"unparsable enrollment": func(nc *karpv1.NodeClaim) { nc.Annotations[v1alpha1.AnnotationKubeletEnrolledAt] = "yesterday" },
		"instance of another project": func(nc *karpv1.NodeClaim) {
			nc.Status.ProviderID = "gce://other-project/us-central1-a/" + testInstance
		},
		"no provider ID": func(nc *karpv1.NodeClaim) { nc.Status.ProviderID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			nc := enrolledClaim(time.Hour)
			mutate(nc)
			h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)

			result, err := h.reconcile(nc)
			require.NoError(t, err)
			require.Zero(t, result)
			require.Empty(t, h.nodes(t))
			require.Empty(t, h.recorder.published)
		})
	}
}

func TestReconcileModeGate(t *testing.T) {
	for _, tt := range []struct {
		name         string
		nodeCreation string
		mode         gke.NodeCreationMode
		afterRefresh gke.NodeCreationMode
		enrolledAgo  time.Duration

		wantNode        bool
		wantRequeue     time.Duration
		wantRefreshes   int
		wantEventReason []string
	}{
		{
			name: "control plane mode creates at once", nodeCreation: options.NodeCreationAuto, mode: gke.NodeCreationModeControlPlane,
			enrolledAgo: time.Second, wantNode: true, wantEventReason: []string{reasonNodeCreated},
		},
		{
			name: "kubelet mode waits out the grace period", nodeCreation: options.NodeCreationAuto, mode: gke.NodeCreationModeKubelet,
			enrolledAgo: 10 * time.Second, wantRequeue: 35 * time.Second,
		},
		{
			name: "kubelet mode creates after the grace period and warns", nodeCreation: options.NodeCreationAuto, mode: gke.NodeCreationModeKubelet,
			enrolledAgo: 46 * time.Second, wantNode: true, wantRefreshes: 1, wantEventReason: []string{reasonModeMismatch, reasonNodeCreated},
		},
		{
			name: "mode change seen on forced re-read creates without a warning", nodeCreation: options.NodeCreationAuto, mode: gke.NodeCreationModeKubelet,
			afterRefresh: gke.NodeCreationModeControlPlane, enrolledAgo: 46 * time.Second, wantNode: true, wantRefreshes: 1, wantEventReason: []string{reasonNodeCreated},
		},
		{
			name: "kubelet setting never creates in kubelet mode", nodeCreation: options.NodeCreationKubelet, mode: gke.NodeCreationModeKubelet,
			enrolledAgo: time.Hour,
		},
		{
			name: "kubelet setting on control plane waits, then explains", nodeCreation: options.NodeCreationKubelet, mode: gke.NodeCreationModeControlPlane,
			enrolledAgo: 10 * time.Second, wantRequeue: 35 * time.Second,
		},
		{
			name: "kubelet setting on control plane reports the stuck claim", nodeCreation: options.NodeCreationKubelet, mode: gke.NodeCreationModeControlPlane,
			enrolledAgo: time.Minute, wantEventReason: []string{reasonSkipped},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.nodeCreation, tt.mode)
			h.gke.modeAfterInvalidate = tt.afterRefresh

			result, err := h.reconcile(enrolledClaim(tt.enrolledAgo))
			require.NoError(t, err)
			require.Equal(t, tt.wantRequeue, result.RequeueAfter)
			require.Equal(t, tt.wantRefreshes, h.gke.invalidations)
			require.Equal(t, tt.wantEventReason, h.recorder.reasons())
			if tt.wantNode {
				require.Len(t, h.nodes(t), 1)
			} else {
				require.Empty(t, h.nodes(t))
			}
		})
	}
}

func TestReconcileCreatesNodeKubeletCanAdopt(t *testing.T) {
	h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)

	_, err := h.reconcile(enrolledClaim(time.Second))
	require.NoError(t, err)

	nodes := h.nodes(t)
	require.Len(t, nodes, 1)
	require.Equal(t, testInstance, nodes[0].Name, "must match the kubelet's system:node:<name> identity")
	require.Equal(t, testProviderID, nodes[0].Spec.ProviderID)
	require.Equal(t, map[string]string{karpv1.NodePoolLabelKey: "default"}, nodes[0].Labels, "registered must be left for core to add")
	require.Equal(t, v1alpha1.NodeCreatedByController, nodes[0].Annotations[v1alpha1.AnnotationNodeCreatedBy])
	require.Empty(t, nodes[0].Finalizers)
	require.Empty(t, nodes[0].OwnerReferences)
}

func TestReconcileLeavesExistingNodesAlone(t *testing.T) {
	t.Run("node with the expected name and provider ID", func(t *testing.T) {
		existing := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testInstance, Labels: map[string]string{"kubelet": "made-me"}}, Spec: corev1.NodeSpec{ProviderID: testProviderID}}
		h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane, existing)

		_, err := h.reconcile(enrolledClaim(time.Minute))
		require.NoError(t, err)
		nodes := h.nodes(t)
		require.Len(t, nodes, 1)
		require.Equal(t, map[string]string{"kubelet": "made-me"}, nodes[0].Labels, "an existing Node is never updated")
		require.Empty(t, h.recorder.published)
	})

	t.Run("differently named node with the same provider ID", func(t *testing.T) {
		stale := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "recycled-name"}, Spec: corev1.NodeSpec{ProviderID: testProviderID}}
		h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane, stale)

		for range 2 {
			_, err := h.reconcile(enrolledClaim(time.Minute))
			require.NoError(t, err)
		}
		require.Len(t, h.nodes(t), 1, "a second Node would make core fail registration with MultipleNodesFound")
		require.Equal(t, []string{reasonNodeNameConflict}, h.recorder.reasons(), "reported once, not on every reconcile")
	})

	t.Run("node of another instance owns the name", func(t *testing.T) {
		squatter := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: testInstance}, Spec: corev1.NodeSpec{ProviderID: "gce://my-project/us-central1-a/other"}}
		h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane, squatter)

		_, err := h.reconcile(enrolledClaim(time.Minute))
		require.Error(t, err, "a name collision is not ours to repair, and must back off")
		nodes := h.nodes(t)
		require.Len(t, nodes, 1)
		require.Equal(t, "gce://my-project/us-central1-a/other", nodes[0].Spec.ProviderID)
		require.Equal(t, []string{reasonNodeNameConflict}, h.recorder.reasons())
	})
}

func TestReconcileIsIdempotentWhenTheCacheLags(t *testing.T) {
	// Our own earlier create is not visible in the cached list yet, so the second
	// reconcile tries to create again and must treat AlreadyExists as success.
	h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)
	h.client.hideNodes = true
	for range 2 {
		_, err := h.reconcile(enrolledClaim(time.Minute))
		require.NoError(t, err)
	}
	require.Len(t, h.nodes(t), 1)
}

func TestReconcileDoesNotCreateForAClaimDeletedSinceTheCacheRead(t *testing.T) {
	deleting := enrolledClaim(time.Minute)
	deleting.Finalizers = []string{"karpenter.sh/termination"}
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)
	h.client.claims["default-abc"] = deleting

	// The reconciled copy is the stale, not yet deleting one.
	_, err := h.reconcile(enrolledClaim(time.Minute))
	require.NoError(t, err)
	require.Empty(t, h.nodes(t))
}

func TestReconcileInstanceProblems(t *testing.T) {
	t.Run("instance already gone", func(t *testing.T) {
		h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)
		h.controller.instances = instanceStub{err: cloudprovider.NewNodeClaimNotFoundError(errors.New("gone"))}
		h.client.claims["default-abc"] = enrolledClaim(time.Minute)

		_, err := h.reconcile(enrolledClaim(time.Minute))
		require.NoError(t, err, "garbage collection and the lifecycle controller own a vanished VM")
		require.Empty(t, h.nodes(t))
	})

	t.Run("malformed kube-labels", func(t *testing.T) {
		h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)
		h.controller.instances = instanceStub{instance: &instance.Instance{Name: testInstance, KubeLabelsErr: errors.New("bad label")}}
		h.client.claims["default-abc"] = enrolledClaim(time.Minute)

		_, err := h.reconcile(enrolledClaim(time.Minute))
		require.Error(t, err)
		require.Empty(t, h.nodes(t), "a Node without its labels would be unschedulable for GKE's agents")
		require.Equal(t, []string{reasonSkipped}, h.recorder.reasons())
	})

	t.Run("cluster read fails", func(t *testing.T) {
		h := newHarness(t, options.NodeCreationAuto, gke.NodeCreationModeControlPlane)
		h.gke.err = errors.New("container API unavailable")

		_, err := h.reconcile(enrolledClaim(time.Minute))
		require.Error(t, err)
		require.Empty(t, h.nodes(t), "an unknown mode must not be guessed")
	})
}
