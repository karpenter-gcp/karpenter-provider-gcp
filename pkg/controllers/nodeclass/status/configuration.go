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
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/subnet"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/utils"
)

const invalidConfigReason = "InvalidConfig"

type Configuration struct{}

// Reconcile validates mode-dependent GCENodeClass configuration invariants that
// CRD CEL rules cannot enforce (for example, spec.startupScript in GKE mode or
// self-hosted NodeClasses without spec.startupScript that rely on image-baked bootstrap).
func (c *Configuration) Reconcile(ctx context.Context, nodeClass *v1alpha1.GCENodeClass) (reconcile.Result, error) {
	opts := options.FromContext(ctx)
	if !opts.IsSelfHosted() {
		if nodeClass.Spec.StartupScript != nil {
			nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionTypeConfigurationReady, invalidConfigReason,
				"spec.startupScript is only supported when PROVISION_MODE=self-hosted")
			return reconcile.Result{}, nil
		}
		nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionTypeConfigurationReady)
		return reconcile.Result{}, nil
	}

	region := utils.RegionFromLocation(opts.ClusterLocation)
	if msg := validateSelfHostedNodeClass(nodeClass, opts.ProjectID, region); msg != "" {
		nodeClass.StatusConditions().SetFalse(v1alpha1.ConditionTypeConfigurationReady, invalidConfigReason, msg)
		return reconcile.Result{}, nil
	}
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionTypeConfigurationReady)
	return reconcile.Result{}, nil
}

func validateSelfHostedNodeClass(nodeClass *v1alpha1.GCENodeClass, projectID, region string) string {
	if msg := validateSelfHostedNetworkConfig(nodeClass.Spec.NetworkConfig, projectID, region); msg != "" {
		return msg
	}
	switch {
	case !hasValidMaxPods(nodeClass.Spec.KubeletConfiguration):
		return "spec.kubeletConfiguration.maxPods must be set and >= 1 in self-hosted mode"
	case !hasBootDisk(nodeClass.Spec.Disks):
		return "spec.disks must contain a boot disk in self-hosted mode"
	case nodeClass.Spec.AutoGPUTaint:
		return "spec.autoGPUTaint is not supported in self-hosted mode"
	case nodeClass.Spec.LinuxNodeConfig != nil:
		return "spec.linuxNodeConfig is not supported in self-hosted mode"
	case nodeClass.Spec.GPUSharingConfig != nil:
		return "spec.gpuSharingConfig is not supported in self-hosted mode"
	case hasSecondaryBootDisk(nodeClass.Spec.Disks):
		return "spec.disks[].secondaryBootImage and secondaryBootMode are not supported in self-hosted mode"
	case hasUnsupportedSelfHostedKubeletConfig(nodeClass.Spec.KubeletConfiguration):
		return "spec.kubeletConfiguration only supports binpacking fields (maxPods, podsPerCore, systemReserved, kubeReserved, evictionHard) in self-hosted mode"
	default:
		return ""
	}
}

func validateSelfHostedNetworkConfig(cfg *v1alpha1.NetworkConfig, projectID, region string) string {
	if cfg == nil || cfg.Subnetwork == "" {
		return "spec.networkConfig.subnetwork is required in self-hosted mode"
	}
	if msg := validateSubnetworkInRegion("", cfg.Subnetwork, projectID, region); msg != "" {
		return msg
	}
	for _, extra := range cfg.AdditionalNetworkInterfaces {
		if msg := validateSubnetworkInRegion(extra.Network, extra.Subnetwork, projectID, region); msg != "" {
			return msg
		}
	}
	return ""
}

func validateSubnetworkInRegion(network, subnetwork, projectID, region string) string {
	ref, err := subnet.CanonicalSubnetworkRef(network, subnetwork, projectID, region)
	if err != nil {
		return err.Error()
	}
	parts := strings.Split(ref, "/")
	if region != "" && len(parts) == 6 && parts[3] != region {
		return fmt.Sprintf("subnetwork %q is in region %q, expected controller region %q", subnetwork, parts[3], region)
	}
	return ""
}

func hasValidMaxPods(kc *v1alpha1.KubeletConfiguration) bool {
	return kc != nil && kc.MaxPods != nil && *kc.MaxPods >= 1
}

func hasBootDisk(disks []v1alpha1.Disk) bool {
	return slices.ContainsFunc(disks, func(d v1alpha1.Disk) bool { return d.Boot })
}

func hasSecondaryBootDisk(disks []v1alpha1.Disk) bool {
	return slices.ContainsFunc(disks, func(d v1alpha1.Disk) bool {
		return d.SecondaryBootImage != "" || d.SecondaryBootMode != ""
	})
}

// hasUnsupportedSelfHostedKubeletConfig mirrors the startupScript CEL rule for
// image-baked self-hosted GCENodeClasses: any kubeletConfiguration field outside
// the binpacking allowlist is delivered only via GKE kubelet-config metadata.
func hasUnsupportedSelfHostedKubeletConfig(kc *v1alpha1.KubeletConfiguration) bool {
	if kc == nil {
		return false
	}
	return slices.Contains([]bool{
		len(kc.ClusterDNS) > 0,
		len(kc.EvictionSoft) > 0,
		len(kc.EvictionSoftGracePeriod) > 0,
		kc.EvictionMaxPodGracePeriod != nil,
		kc.ImageGCHighThresholdPercent != nil,
		kc.ImageGCLowThresholdPercent != nil,
		kc.CPUCFSQuota != nil,
		kc.CPUManagerPolicy != "",
		kc.SerializeImagePulls != nil,
		kc.MaxParallelImagePulls != nil,
		kc.FailSwapOn != nil,
		kc.MemorySwap != nil,
		kc.ShutdownGracePeriod != nil,
		kc.ShutdownGracePeriodCriticalPods != nil,
	}, true)
}
