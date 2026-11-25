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
	"fmt"
	"strings"

	"github.com/samber/lo"
	"google.golang.org/api/compute/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// KubeLabelsFromCompute reads the kube-labels item of Compute metadata and
// nothing else. The instance's metadata also carries secret-bearing items such
// as kube-env, so callers outside this package get the one item they may keep
// and never the whole response. found is false when the item is absent.
//
// Every key and value must be a valid Kubernetes label, so a Node built from the
// result cannot be rejected for a malformed label that was silently skipped.
func KubeLabelsFromCompute(api *compute.Metadata) (labels Labels, found bool, err error) {
	if api == nil {
		return nil, false, nil
	}
	for _, item := range api.Items {
		if item != nil && item.Key == KubeLabelsKey {
			labels, err = ParseLabels(lo.FromPtr(item.Value))
			return labels, err == nil, err
		}
	}
	return nil, false, nil
}

// ParseLabels parses a comma-separated key=value list, the format of the
// kube-labels metadata item and of the node-labels NodeClaim annotation. Every key
// and value must be a valid Kubernetes label.
func ParseLabels(raw string) (Labels, error) {
	labels := Labels{}
	if strings.TrimSpace(raw) == "" {
		return labels, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			return nil, fmt.Errorf("label entry %q is not key=value", pair)
		}
		if errs := k8svalidation.IsQualifiedName(key); len(errs) > 0 {
			return nil, fmt.Errorf("label key %q is invalid: %s", key, strings.Join(errs, "; "))
		}
		if errs := k8svalidation.IsValidLabelValue(value); len(errs) > 0 {
			return nil, fmt.Errorf("label value of %q is invalid: %s", key, strings.Join(errs, "; "))
		}
		labels[key] = value
	}
	return labels, nil
}
