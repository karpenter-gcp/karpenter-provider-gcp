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
	"fmt"

	"google.golang.org/api/compute/v1"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/subnet"
)

// This file contains the self-hosted launch path. Node bootstrap is owned by the
// NodeClass startup script or baked into the image, so the instance carries no GKE
// bootstrap metadata, tags, or labels.

// setupSelfHostedNetworkInterfaces builds NICs from spec.networkConfig. The primary
// subnetwork is required; Network is omitted unless explicitly set on an additional
// interface so GCE infers the VPC from the subnetwork. Nodes are private by default;
// alias IP ranges use podRange (resolved from spec.subnetRangeNames or deprecated
// spec.subnetRangeName) since there is no GKE IP allocation policy.
func (p *DefaultProvider) setupSelfHostedNetworkInterfaces(nodeClass *v1alpha1.GCENodeClass, podRange string) ([]*compute.NetworkInterface, error) {
	cfg := nodeClass.Spec.NetworkConfig
	if cfg == nil || cfg.Subnetwork == "" {
		return nil, fmt.Errorf("GCENodeClass %s: spec.networkConfig.subnetwork is required in self-hosted mode", nodeClass.Name)
	}
	primarySubnetwork, err := subnet.CanonicalSubnetworkRef("", cfg.Subnetwork, p.projectID, p.region)
	if err != nil {
		return nil, err
	}

	disableExternal := cfg.EnablePrivateNodes == nil || *cfg.EnablePrivateNodes
	nic := networkInterfaceType(nodeClass)

	primary := &compute.NetworkInterface{
		Subnetwork: primarySubnetwork,
		NicType:    nic,
	}
	if podRange != "" {
		primary.AliasIpRanges = []*compute.AliasIpRange{{
			IpCidrRange:         fmt.Sprintf("/%d", podCIDRRange(nodeClass.GetMaxPods())),
			SubnetworkRangeName: podRange,
		}}
	}
	applyAccessConfig(primary, disableExternal)

	ifaces := []*compute.NetworkInterface{primary}
	for _, override := range cfg.AdditionalNetworkInterfaces {
		subnetworkRef, err := subnet.CanonicalSubnetworkRef(override.Network, override.Subnetwork, p.projectID, p.region)
		if err != nil {
			return nil, err
		}
		iface := &compute.NetworkInterface{
			Network:    override.Network,
			Subnetwork: subnetworkRef,
			NicType:    nic,
		}
		applyAccessConfig(iface, disableExternal)
		ifaces = append(ifaces, iface)
	}
	return ifaces, nil
}
