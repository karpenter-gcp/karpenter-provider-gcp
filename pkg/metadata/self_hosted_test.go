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

package metadata

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

func metadataItemsToMap(md *compute.Metadata) map[string]string {
	values := map[string]string{}
	for _, item := range md.Items {
		values[item.Key] = lo.FromPtr(item.Value)
	}
	return values
}

func TestSelfHostedComputeMetadata_RendersOnlyOwnedAndCustomKeys(t *testing.T) {
	t.Parallel()

	// Multi-line script with trailing newline; must be written byte-for-byte.
	startupScript := "#!/bin/bash\nset -euo pipefail\necho 'nodeup: bootstrap'\n"
	custom := CustomMetadata{
		"kops-k8s-io-instance-group-name": "nodes-us-central1-a",
		"":                                "dropped-empty-key",
	}

	result := SelfHostedComputeMetadata("poc0006.k8s.local", startupScript, custom)

	require.Equal(t, map[string]string{
		ClusterNameKey:                    "poc0006.k8s.local",
		StartupScriptKey:                  startupScript,
		"kops-k8s-io-instance-group-name": "nodes-us-central1-a",
	}, metadataItemsToMap(result))
}

func TestSelfHostedComputeMetadata_OmitsStartupScriptWhenEmpty(t *testing.T) {
	t.Parallel()

	// Images with baked-in bootstrap need no startup script; the key must be absent
	// entirely, not rendered with an empty value.
	result := SelfHostedComputeMetadata("poc0006.k8s.local", "", CustomMetadata{
		"kops-k8s-io-instance-group-name": "nodes-us-central1-a",
	})

	require.Equal(t, map[string]string{
		ClusterNameKey:                    "poc0006.k8s.local",
		"kops-k8s-io-instance-group-name": "nodes-us-central1-a",
	}, metadataItemsToMap(result))
}

func TestSelfHostedComputeMetadata_ProviderOwnedKeysWinOverCustom(t *testing.T) {
	t.Parallel()

	// spec.metadata cannot contain reserved keys (CRD CEL), but the builder must still
	// be safe if that invariant is ever bypassed.
	result := SelfHostedComputeMetadata("exact-name", "#!/bin/bash\n", CustomMetadata{
		ClusterNameKey:   "spoofed",
		StartupScriptKey: "spoofed",
	})

	require.Equal(t, map[string]string{
		ClusterNameKey:   "exact-name",
		StartupScriptKey: "#!/bin/bash\n",
	}, metadataItemsToMap(result))
}
