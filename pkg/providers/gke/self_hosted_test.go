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

package gke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	computev1 "google.golang.org/api/compute/v1"
	googleapioption "google.golang.org/api/option"
)

func newSelfHostedTestProvider(t *testing.T, endpoint string) Provider {
	t.Helper()
	computeService, err := computev1.NewService(context.Background(),
		googleapioption.WithoutAuthentication(),
		googleapioption.WithEndpoint(endpoint+"/"),
	)
	require.NoError(t, err)

	return NewSelfHostedProvider(computeService, "p", "us-central1")
}

func TestSelfHostedResolveClusterZones_FiltersByRegionAndStatus(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&computev1.ZoneList{Items: []*computev1.Zone{
			{Name: "us-central1-c", Status: "UP"},
			{Name: "us-central1-a", Status: "UP"},
			{Name: "us-central1-b", Status: "DOWN"},
			{Name: "europe-west4-a", Status: "UP"},
		}})
	}))
	defer srv.Close()

	p := newSelfHostedTestProvider(t, srv.URL)

	zones, err := p.ResolveClusterZones(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"us-central1-a", "us-central1-c"}, zones)
	require.Equal(t, int32(1), calls.Load())

	zones, err = p.ResolveClusterZones(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"us-central1-a", "us-central1-c"}, zones)
	require.Equal(t, int32(1), calls.Load(), "second call must hit cache")
}

func TestSelfHostedResolveClusterZones_ErrorsWhenRegionHasNoZones(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&computev1.ZoneList{Items: []*computev1.Zone{
			{Name: "europe-west4-a", Status: "UP"},
		}})
	}))
	defer srv.Close()

	p := newSelfHostedTestProvider(t, srv.URL)

	_, err := p.ResolveClusterZones(context.Background())
	require.ErrorContains(t, err, "no zones found for region: us-central1")
}
