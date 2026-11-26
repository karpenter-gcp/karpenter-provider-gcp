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
	"maps"

	"google.golang.org/api/compute/v1"
)

const (
	// ClusterNameKey is the provider-owned metadata key carrying the exact cluster name.
	// Distributions that authorize joining nodes through instance metadata (e.g. kOps'
	// TPM node authorizer) match on this value.
	ClusterNameKey = "cluster-name"
	// StartupScriptKey is the metadata key executed natively by the GCE guest agent.
	StartupScriptKey = "startup-script"
)

// SelfHostedComputeMetadata renders self-hosted instance metadata: the user's
// spec.metadata keys, the provider-owned cluster-name key, and startup-script when
// non-empty — never the GKE bootstrap keys the InstanceMetadata model always renders.
func SelfHostedComputeMetadata(clusterName, startupScript string, custom CustomMetadata) *compute.Metadata {
	values := metadataValues{}
	maps.Copy(values, custom)
	delete(values, "")
	values[ClusterNameKey] = clusterName
	if startupScript != "" {
		values[StartupScriptKey] = startupScript
	}
	return toComputeMetadata(values)
}
