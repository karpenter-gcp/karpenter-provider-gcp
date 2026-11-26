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
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
)

func TestConfigurationReconcile(t *testing.T) {
	t.Parallel()

	selfHostedCtx := options.ToContext(context.Background(), &options.Options{
		ProvisionMode:   options.ProvisionModeSelfHosted,
		ProjectID:       "my-project",
		ClusterLocation: "us-central1",
	})
	validSelfHosted := func() *v1alpha1.GCENodeClass {
		return &v1alpha1.GCENodeClass{
			Spec: v1alpha1.GCENodeClassSpec{
				NetworkConfig: &v1alpha1.NetworkConfig{Subnetwork: "nodes"},
				KubeletConfiguration: &v1alpha1.KubeletConfiguration{
					MaxPods:        ptr.To(int32(110)),
					PodsPerCore:    ptr.To(int32(10)),
					SystemReserved: map[string]v1alpha1.KubeletQuantity{"cpu": "100m"},
					KubeReserved:   map[string]v1alpha1.KubeletQuantity{"memory": "256Mi"},
					EvictionHard:   map[string]v1alpha1.KubeletQuantity{"memory.available": "100Mi"},
				},
				Disks: []v1alpha1.Disk{{Boot: true, SizeGiB: 100}},
			},
		}
	}

	t.Run("gke mode marks configuration ready without self-hosted fields", func(t *testing.T) {
		t.Parallel()
		nc := &v1alpha1.GCENodeClass{}
		_, err := (&Configuration{}).Reconcile(context.Background(), nc)
		require.NoError(t, err)
		require.True(t, nc.StatusConditions().IsTrue(v1alpha1.ConditionTypeConfigurationReady))
	})

	t.Run("gke mode rejects startupScript with ConfigurationReady=False", func(t *testing.T) {
		t.Parallel()
		nc := &v1alpha1.GCENodeClass{
			Spec: v1alpha1.GCENodeClassSpec{StartupScript: ptr.To("#!/bin/bash\necho hi\n")},
		}
		_, err := (&Configuration{}).Reconcile(context.Background(), nc)
		require.NoError(t, err)
		cond := nc.StatusConditions().Get(v1alpha1.ConditionTypeConfigurationReady)
		require.True(t, cond.IsFalse())
		require.Equal(t, invalidConfigReason, cond.Reason)
		require.Equal(t, "spec.startupScript is only supported when PROVISION_MODE=self-hosted", cond.Message)
	})

	t.Run("self-hosted valid image-baked nodeClass with binpacking kubelet fields is ready", func(t *testing.T) {
		t.Parallel()
		nc := validSelfHosted()
		_, err := (&Configuration{}).Reconcile(selfHostedCtx, nc)
		require.NoError(t, err)
		require.True(t, nc.StatusConditions().IsTrue(v1alpha1.ConditionTypeConfigurationReady))
	})

	cases := []struct {
		name        string
		mutate      func(*v1alpha1.GCENodeClass)
		wantMessage string
	}{
		{
			name:        "missing networkConfig",
			mutate:      func(nc *v1alpha1.GCENodeClass) { nc.Spec.NetworkConfig = nil },
			wantMessage: "spec.networkConfig.subnetwork is required in self-hosted mode",
		},
		{
			name:        "malformed subnetwork reference",
			mutate:      func(nc *v1alpha1.GCENodeClass) { nc.Spec.NetworkConfig.Subnetwork = "subnetworks/nodes" },
			wantMessage: `invalid subnetwork reference "subnetworks/nodes"`,
		},
		{
			name: "subnetwork in different region",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.NetworkConfig.Subnetwork = "projects/my-project/regions/europe-west4/subnetworks/nodes"
			},
			wantMessage: `is in region "europe-west4", expected controller region "us-central1"`,
		},
		{
			name: "additional interface subnetwork in different region",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.NetworkConfig.AdditionalNetworkInterfaces = []v1alpha1.AdditionalNetworkInterface{
					{Subnetwork: "regions/europe-west4/subnetworks/extra"},
				}
			},
			wantMessage: `is in region "europe-west4", expected controller region "us-central1"`,
		},
		{
			name:        "missing maxPods",
			mutate:      func(nc *v1alpha1.GCENodeClass) { nc.Spec.KubeletConfiguration = nil },
			wantMessage: "spec.kubeletConfiguration.maxPods must be set and >= 1 in self-hosted mode",
		},
		{
			name:        "zero maxPods",
			mutate:      func(nc *v1alpha1.GCENodeClass) { nc.Spec.KubeletConfiguration.MaxPods = ptr.To(int32(0)) },
			wantMessage: "spec.kubeletConfiguration.maxPods must be set and >= 1 in self-hosted mode",
		},
		{
			name:        "missing boot disk",
			mutate:      func(nc *v1alpha1.GCENodeClass) { nc.Spec.Disks = []v1alpha1.Disk{{SizeGiB: 100}} },
			wantMessage: "spec.disks must contain a boot disk in self-hosted mode",
		},
		{
			name:        "autoGPUTaint rejected",
			mutate:      func(nc *v1alpha1.GCENodeClass) { nc.Spec.AutoGPUTaint = true },
			wantMessage: "spec.autoGPUTaint is not supported in self-hosted mode",
		},
		{
			name: "linuxNodeConfig rejected",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.LinuxNodeConfig = &v1alpha1.LinuxNodeConfig{
					SwapConfig: &v1alpha1.SwapConfig{Enabled: true},
				}
			},
			wantMessage: "spec.linuxNodeConfig is not supported in self-hosted mode",
		},
		{
			name: "gpuSharingConfig rejected",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.GPUSharingConfig = &v1alpha1.GPUSharingConfig{
					Strategy: v1alpha1.GPUSharingStrategyTimeSharing, MaxSharedClientsPerGPU: 2,
				}
			},
			wantMessage: "spec.gpuSharingConfig is not supported in self-hosted mode",
		},
		{
			name: "secondaryBootImage rejected",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.Disks = append(nc.Spec.Disks, v1alpha1.Disk{SizeGiB: 50, SecondaryBootImage: "global/images/cache"})
			},
			wantMessage: "spec.disks[].secondaryBootImage and secondaryBootMode are not supported in self-hosted mode",
		},
		{
			name: "secondaryBootMode rejected",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.Disks = append(nc.Spec.Disks, v1alpha1.Disk{SizeGiB: 50, SecondaryBootMode: "CONTAINER_IMAGE_CACHE"})
			},
			wantMessage: "spec.disks[].secondaryBootImage and secondaryBootMode are not supported in self-hosted mode",
		},
		{
			name: "node-delivery kubeletConfiguration field rejected",
			mutate: func(nc *v1alpha1.GCENodeClass) {
				nc.Spec.KubeletConfiguration.CPUManagerPolicy = "static"
			},
			wantMessage: "spec.kubeletConfiguration only supports binpacking fields",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nc := validSelfHosted()
			tc.mutate(nc)

			_, err := (&Configuration{}).Reconcile(selfHostedCtx, nc)
			require.NoError(t, err)
			cond := nc.StatusConditions().Get(v1alpha1.ConditionTypeConfigurationReady)
			require.True(t, cond.IsFalse())
			require.Equal(t, invalidConfigReason, cond.Reason)
			require.Contains(t, cond.Message, tc.wantMessage)
		})
	}
}

// TestHasUnsupportedSelfHostedKubeletConfig_Exhaustive ensures every field on
// KubeletConfiguration is classified as either binpacking-allowed or rejected by
// hasUnsupportedSelfHostedKubeletConfig.
func TestHasUnsupportedSelfHostedKubeletConfig_Exhaustive(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"maxPods": true, "podsPerCore": true,
		"systemReserved": true, "kubeReserved": true, "evictionHard": true,
	}
	typ := reflect.TypeFor[v1alpha1.KubeletConfiguration]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		kc := &v1alpha1.KubeletConfiguration{}
		fv := reflect.ValueOf(kc).Elem().Field(i)
		switch field.Type.Kind() {
		case reflect.String:
			fv.SetString("non-empty")
		case reflect.Slice:
			fv.Set(reflect.ValueOf([]string{"10.0.0.10"}))
		case reflect.Map:
			m := reflect.MakeMap(field.Type)
			if field.Type.Elem() == reflect.TypeFor[metav1.Duration]() {
				m.SetMapIndex(reflect.ValueOf("memory.available"), reflect.ValueOf(metav1.Duration{}))
			} else {
				m.SetMapIndex(reflect.ValueOf("cpu"), reflect.ValueOf(v1alpha1.KubeletQuantity("100m")))
			}
			fv.Set(m)
		case reflect.Pointer:
			fv.Set(reflect.New(field.Type.Elem()))
		default:
			t.Fatalf("unexpected KubeletConfiguration field kind %v for %s", field.Type.Kind(), field.Name)
		}

		got := hasUnsupportedSelfHostedKubeletConfig(kc)
		require.Equal(t, !allowed[jsonName], got,
			"KubeletConfiguration.%s (%q) classification mismatch in hasUnsupportedSelfHostedKubeletConfig", field.Name, jsonName)
	}
}
