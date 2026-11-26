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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/imagefamily"
)

type fakeStatusWriter struct {
	client.SubResourceWriter
	patchErr error
	called   bool
}

func (f *fakeStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	f.called = true
	return f.patchErr
}

type fakePatchClient struct {
	client.Client
	patchErr     error
	patchCalled  bool
	statusWriter *fakeStatusWriter
}

func (f *fakePatchClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	f.patchCalled = true
	return f.patchErr
}

func (f *fakePatchClient) Status() client.SubResourceWriter {
	return f.statusWriter
}

func TestControllerReconcilePatchConflicts(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: apis.Group, Resource: "gcenodeclasses"}
	conflictErr := apierrors.NewConflict(gr, "default", errors.New("object has been modified"))
	notFoundErr := apierrors.NewNotFound(gr, "default")
	internalErr := errors.New("apiserver unavailable")

	selfHostedCtx := options.ToContext(context.Background(), &options.Options{
		ProvisionMode:   options.ProvisionModeSelfHosted,
		ProjectID:       "my-project",
		ClusterLocation: "us-central1",
	})
	newController := func(kubeClient client.Client) *Controller {
		return NewController(
			kubeClient,
			fakeImageProvider{
				images: imagefamily.Images{
					{
						SourceImage:  "projects/test/global/images/img-1",
						Requirements: scheduling.NewRequirements(scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64")),
					},
				},
			},
			&failingGKEProvider{t: t},
			&fakeSubnetProvider{},
		)
	}
	baseNodeClass := func(withFinalizer bool) *v1alpha1.GCENodeClass {
		nc := &v1alpha1.GCENodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Spec: v1alpha1.GCENodeClassSpec{
				NetworkConfig: &v1alpha1.NetworkConfig{Subnetwork: "nodes"},
				Disks:         []v1alpha1.Disk{{Boot: true, SizeGiB: 100}},
			},
		}
		if withFinalizer {
			nc.Finalizers = []string{v1alpha1.TerminationFinalizer}
		}
		return nc
	}

	t.Run("finalizer patch conflict requeues without error", func(t *testing.T) {
		t.Parallel()
		sw := &fakeStatusWriter{}
		kc := &fakePatchClient{patchErr: conflictErr, statusWriter: sw}

		res, err := newController(kc).Reconcile(selfHostedCtx, baseNodeClass(false))
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{Requeue: true}, res) //nolint:staticcheck // Verifying rate-limited conflict requeue.
		require.True(t, kc.patchCalled)
		require.False(t, sw.called)
	})

	t.Run("finalizer patch not found is ignored", func(t *testing.T) {
		t.Parallel()
		sw := &fakeStatusWriter{}
		kc := &fakePatchClient{patchErr: notFoundErr, statusWriter: sw}

		res, err := newController(kc).Reconcile(selfHostedCtx, baseNodeClass(false))
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{}, res)
		require.True(t, kc.patchCalled)
		require.False(t, sw.called)
	})

	t.Run("finalizer patch non-conflict error is returned", func(t *testing.T) {
		t.Parallel()
		sw := &fakeStatusWriter{}
		kc := &fakePatchClient{patchErr: internalErr, statusWriter: sw}

		_, err := newController(kc).Reconcile(selfHostedCtx, baseNodeClass(false))
		require.ErrorIs(t, err, internalErr)
		require.False(t, sw.called)
	})

	t.Run("status patch conflict requeues without error", func(t *testing.T) {
		t.Parallel()
		sw := &fakeStatusWriter{patchErr: conflictErr}
		kc := &fakePatchClient{statusWriter: sw}

		res, err := newController(kc).Reconcile(selfHostedCtx, baseNodeClass(true))
		require.NoError(t, err)
		require.Equal(t, reconcile.Result{Requeue: true}, res) //nolint:staticcheck // Verifying rate-limited conflict requeue.
		require.False(t, kc.patchCalled)
		require.True(t, sw.called)
	})

	t.Run("status patch non-conflict error is returned", func(t *testing.T) {
		t.Parallel()
		sw := &fakeStatusWriter{patchErr: internalErr}
		kc := &fakePatchClient{statusWriter: sw}

		_, err := newController(kc).Reconcile(selfHostedCtx, baseNodeClass(true))
		require.ErrorIs(t, err, internalErr)
		require.True(t, sw.called)
	})
}
