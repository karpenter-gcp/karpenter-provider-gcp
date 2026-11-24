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

package metadata

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

func TestKubeLabelsFromComputeRoundTripsRenderedLabels(t *testing.T) {
	model := NewInstanceMetadata()
	model.SetKubeLabels(Labels{"karpenter.sh/registered": "true", "karpenter.sh/nodepool": "default"})
	model.SetKubeLabel("cloud.google.com.node-restriction.kubernetes.io/gke-secondary-boot-disk-img", "CONTAINER_IMAGE_CACHE.project")
	api, err := model.ToComputeMetadata()
	require.NoError(t, err)

	labels, found, err := KubeLabelsFromCompute(api)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, Labels{
		"karpenter.sh/registered": "true",
		"karpenter.sh/nodepool":   "default",
		"cloud.google.com.node-restriction.kubernetes.io/gke-secondary-boot-disk-img": "CONTAINER_IMAGE_CACHE.project",
	}, labels)
}

func TestKubeLabelsFromComputeReadsOnlyTheKubeLabelsItem(t *testing.T) {
	api := &compute.Metadata{Items: []*compute.MetadataItems{
		{Key: KubeEnvKey, Value: lo.ToPtr("KUBELET_ARGS: --node-labels=from-kube-env=true\nSECRET: s3cret\n")},
		{Key: KubeLabelsKey, Value: lo.ToPtr("a=b")},
	}}
	labels, found, err := KubeLabelsFromCompute(api)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, Labels{"a": "b"}, labels)
}

func TestKubeLabelsFromComputeAbsentAndEmpty(t *testing.T) {
	_, found, err := KubeLabelsFromCompute(nil)
	require.NoError(t, err)
	require.False(t, found)

	_, found, err = KubeLabelsFromCompute(&compute.Metadata{Items: []*compute.MetadataItems{{Key: KubeEnvKey, Value: lo.ToPtr("x: y")}}})
	require.NoError(t, err)
	require.False(t, found, "an instance without kube-labels is not the same as one with an empty set")

	labels, found, err := KubeLabelsFromCompute(&compute.Metadata{Items: []*compute.MetadataItems{{Key: KubeLabelsKey, Value: lo.ToPtr("")}}})
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, labels)
}

func TestKubeLabelsFromComputeRejectsMalformedLabels(t *testing.T) {
	for name, raw := range map[string]string{
		"missing equals": "a=b,novalue",
		"invalid key":    "bad key=v",
		"invalid value":  "a=has space",
	} {
		t.Run(name, func(t *testing.T) {
			_, found, err := KubeLabelsFromCompute(&compute.Metadata{Items: []*compute.MetadataItems{{Key: KubeLabelsKey, Value: lo.ToPtr(raw)}}})
			require.Error(t, err)
			require.False(t, found)
		})
	}
}
