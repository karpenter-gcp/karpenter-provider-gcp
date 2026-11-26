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

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/controllers/csr"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/controllers/nodepooltemplate"
	"github.com/karpenter-gcp/karpenter-provider-gcp/pkg/operator/options"
)

func TestGKEControllers_OmittedInSelfHostedMode(t *testing.T) {
	t.Parallel()

	gkeCtx := options.ToContext(context.Background(), &options.Options{
		ProvisionMode: options.ProvisionModeGKE,
	})
	gkeSet := gkeControllers(gkeCtx, nil, nil)
	require.Len(t, gkeSet, 2)
	require.IsType(t, &nodepooltemplate.Controller{}, gkeSet[0])
	require.IsType(t, &csr.Controller{}, gkeSet[1])

	selfHostedCtx := options.ToContext(context.Background(), &options.Options{
		ProvisionMode: options.ProvisionModeSelfHosted,
	})
	require.Empty(t, gkeControllers(selfHostedCtx, nil, nil))
}
