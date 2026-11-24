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
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/apis/v1alpha1"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/providers/instance"
)

// kubeletLabelKeys are the well-known labels in kubernetes.io namespaces that a
// kubelet may set on its own Node (the NodeRestriction admission plugin allows
// them), including the legacy beta and failure-domain spellings.
var kubeletLabelKeys = map[string]struct{}{
	"kubernetes.io/hostname":                   {},
	"kubernetes.io/arch":                       {},
	"kubernetes.io/os":                         {},
	"topology.kubernetes.io/region":            {},
	"topology.kubernetes.io/zone":              {},
	"failure-domain.beta.kubernetes.io/region": {},
	"failure-domain.beta.kubernetes.io/zone":   {},
	"beta.kubernetes.io/arch":                  {},
	"beta.kubernetes.io/os":                    {},
	"beta.kubernetes.io/instance-type":         {},
	"node.kubernetes.io/instance-type":         {},
}

// kubeletLabelDomains are namespaces under which a kubelet may set any label.
var kubeletLabelDomains = []string{"kubelet.kubernetes.io", "node.kubernetes.io"}

// renderNode builds the Node a kubelet would have registered for a launched
// instance. It carries the labels the provider wrote into the instance's
// kube-labels, the launch taints plus the cloud initialization taint, and the
// provider ID the core registration controller matches on. It deliberately has
// no owner reference or finalizer: core adds them when registration completes.
// dropped lists the label keys the policy removed.
func renderNode(name, providerID string, nodeClass *v1alpha1.GCENodeClass, inst *instance.Instance) (node *corev1.Node, dropped []string) {
	labels, dropped := filterNodeLabels(inst.KubeLabels)
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: map[string]string{v1alpha1.AnnotationNodeCreatedBy: v1alpha1.NodeCreatedByController},
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
			Taints:     instance.NodeRegistrationTaints(nodeClass, inst.KubeLabels),
		},
	}, dropped
}

// filterNodeLabels applies the label policy to the instance's kube-labels.
//
// The controller is a trusted control plane component, so unlike a kubelet it may
// set node-restriction labels (for example secondary boot disk labels). It must
// not be more permissive than the data it reads, which anyone with
// compute.instances.setMetadata can write: labels in the kubernetes.io and k8s.io
// namespaces other than those a kubelet may self-assign (node-role among them)
// are dropped, as are the registration labels that only core may add.
func filterNodeLabels(in map[string]string) (kept map[string]string, dropped []string) {
	kept = make(map[string]string, len(in))
	for key, value := range in {
		if labelAllowed(key) {
			kept[key] = value
		} else {
			dropped = append(dropped, key)
		}
	}
	sort.Strings(dropped)
	return kept, dropped
}

func labelAllowed(key string) bool {
	// Core adds these when registration and initialization complete. Their absence
	// before then is what registration's race protection relies on.
	if key == karpv1.NodeRegisteredLabelKey || key == karpv1.NodeInitializedLabelKey {
		return false
	}
	domain, _, hasPrefix := strings.Cut(key, "/")
	if !hasPrefix || !isReservedDomain(domain) {
		return true
	}
	if domain == "node-restriction.kubernetes.io" || strings.HasSuffix(domain, ".node-restriction.kubernetes.io") {
		return true
	}
	if _, ok := kubeletLabelKeys[key]; ok {
		return true
	}
	for _, allowed := range kubeletLabelDomains {
		if domain == allowed || strings.HasSuffix(domain, "."+allowed) {
			return true
		}
	}
	return false
}

func isReservedDomain(domain string) bool {
	for _, reserved := range []string{"kubernetes.io", "k8s.io"} {
		if domain == reserved || strings.HasSuffix(domain, "."+reserved) {
			return true
		}
	}
	return false
}
