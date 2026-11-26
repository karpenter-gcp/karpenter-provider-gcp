/*
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

package options

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

func parseOptions(t *testing.T, args ...string) (*Options, error) {
	t.Helper()
	fs := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
	opts := &Options{}
	opts.AddFlags(fs)
	err := opts.Parse(fs, args...)
	return opts, err
}

func requiredArgs() []string {
	return []string{"--project-id=my-project", "--cluster-name=my-cluster", "--cluster-location=us-central1"}
}

// Env vars are read when flags are registered, so these tests use t.Setenv and
// cannot run in parallel.
func TestQuotaExceededTTL(t *testing.T) {
	requiredArgs := []string{"--project-id=p", "--cluster-name=c", "--cluster-location=us-central1"}

	tests := []struct {
		name    string
		env     *string
		args    []string
		want    time.Duration
		wantErr string
	}{
		{name: "unset uses default", want: DefaultQuotaExceededTTL},
		{name: "env custom duration", env: ptr.To("1h"), want: time.Hour},
		{name: "flag custom duration", args: []string{"--quota-exceeded-ttl=45m"}, want: 45 * time.Minute},
		{name: "env 0s disables backoff", env: ptr.To("0s"), want: 0},
		{name: "flag 0s disables backoff", args: []string{"--quota-exceeded-ttl=0s"}, want: 0},
		{name: "env negative is rejected", env: ptr.To("-5m"), wantErr: "quota-exceeded-ttl must not be negative"},
		{name: "flag negative is rejected", args: []string{"--quota-exceeded-ttl=-5m"}, wantErr: "quota-exceeded-ttl must not be negative"},
		{name: "flag takes precedence over env", env: ptr.To("1h"), args: []string{"--quota-exceeded-ttl=2h"}, want: 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProvisionModeEnv(t)
			t.Setenv(quotaExceededTTLEnvVarName, "")
			if tt.env != nil {
				t.Setenv(quotaExceededTTLEnvVarName, *tt.env)
			} else {
				require.NoError(t, os.Unsetenv(quotaExceededTTLEnvVarName))
			}

			o := &Options{}
			fs := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
			o.AddFlags(fs)
			err := o.Parse(fs, append(requiredArgs, tt.args...)...)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, o.QuotaExceededTTL)
		})
	}
}

func TestTelemetry(t *testing.T) {
	requiredArgs := []string{"--project-id=p", "--cluster-name=c", "--cluster-location=us-central1"}

	tests := []struct {
		name string
		env  *string
		args []string
		want bool
	}{
		{name: "unset is off", want: false},
		{name: "env true turns it on", env: ptr.To("true"), want: true},
		{name: "env false keeps it off", env: ptr.To("false"), want: false},
		{name: "flag turns it on", args: []string{"--telemetry"}, want: true},
		{name: "flag takes precedence over env", env: ptr.To("true"), args: []string{"--telemetry=false"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProvisionModeEnv(t)
			t.Setenv(telemetryEnvVarName, "")
			if tt.env != nil {
				t.Setenv(telemetryEnvVarName, *tt.env)
			} else {
				require.NoError(t, os.Unsetenv(telemetryEnvVarName))
			}

			o := &Options{}
			fs := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
			o.AddFlags(fs)
			require.NoError(t, o.Parse(fs, append(requiredArgs, tt.args...)...))
			require.Equal(t, tt.want, o.Telemetry)
		})
	}
}

func TestCapacityBackoffDurations(t *testing.T) {
	requiredArgs := []string{"--project-id=p", "--cluster-name=c", "--cluster-location=us-central1"}

	settings := []struct {
		flag  string
		env   string
		def   time.Duration
		value func(*Options) time.Duration
	}{
		{stockoutTTLFlagName, stockoutTTLEnvVarName, DefaultStockoutTTL, func(o *Options) time.Duration { return o.StockoutTTL }},
		{ipSpaceExhaustedTTLFlagName, ipSpaceExhaustedTTLEnvVarName, DefaultIPSpaceExhaustedTTL, func(o *Options) time.Duration { return o.IPSpaceExhaustedTTL }},
		{unsupportedConfigurationTTLFlagName, unsupportedConfigurationTTLEnvVarName, DefaultUnsupportedConfigurationTTL, func(o *Options) time.Duration { return o.UnsupportedConfigurationTTL }},
		{launchTimeoutFlagName, launchTimeoutEnvVarName, DefaultLaunchTimeout, func(o *Options) time.Duration { return o.LaunchTimeout }},
	}
	for _, s := range settings {
		tests := []struct {
			name    string
			env     *string
			args    []string
			want    time.Duration
			wantErr string
		}{
			{name: "unset uses default", want: s.def},
			{name: "env custom duration", env: ptr.To("7m"), want: 7 * time.Minute},
			{name: "flag custom duration", args: []string{"--" + s.flag + "=45s"}, want: 45 * time.Second},
			{name: "flag takes precedence over env", env: ptr.To("1h"), args: []string{"--" + s.flag + "=2h"}, want: 2 * time.Hour},
			{name: "zero is rejected", args: []string{"--" + s.flag + "=0s"}, wantErr: s.flag + " must be positive"},
			{name: "negative is rejected", env: ptr.To("-5m"), wantErr: s.flag + " must be positive"},
		}
		for _, tt := range tests {
			t.Run(s.flag+"/"+tt.name, func(t *testing.T) {
				clearProvisionModeEnv(t)
				t.Setenv(s.env, "")
				if tt.env != nil {
					t.Setenv(s.env, *tt.env)
				} else {
					require.NoError(t, os.Unsetenv(s.env))
				}

				o := &Options{}
				fs := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
				o.AddFlags(fs)
				err := o.Parse(fs, append(requiredArgs, tt.args...)...)

				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tt.want, s.value(o))
			})
		}
	}
}

func clearProvisionModeEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		provisionModeEnvVarName,
		defaultNodePoolTemplateNameEnvVarName,
		nodeLocationEnvVarName,
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}

func TestParse_ProvisionModeSelection(t *testing.T) {
	var nilOpts *Options
	require.False(t, nilOpts.IsSelfHosted())

	tests := []struct {
		name           string
		env            *string
		args           []string
		wantMode       string
		wantSelfHosted bool
	}{
		{name: "unset defaults to gke", wantMode: ProvisionModeGKE, wantSelfHosted: false},
		{name: "env selects self-hosted", env: ptr.To(ProvisionModeSelfHosted), wantMode: ProvisionModeSelfHosted, wantSelfHosted: true},
		{name: "flag selects self-hosted", args: []string{"--provision-mode=self-hosted"}, wantMode: ProvisionModeSelfHosted, wantSelfHosted: true},
		{name: "flag takes precedence over env", env: ptr.To(ProvisionModeSelfHosted), args: []string{"--provision-mode=gke"}, wantMode: ProvisionModeGKE, wantSelfHosted: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProvisionModeEnv(t)
			if tt.env != nil {
				t.Setenv(provisionModeEnvVarName, *tt.env)
			}

			opts, err := parseOptions(t, append(requiredArgs(), tt.args...)...)
			require.NoError(t, err)
			require.Equal(t, tt.wantMode, opts.ProvisionMode)
			require.Equal(t, tt.wantSelfHosted, opts.IsSelfHosted())
		})
	}
}

func TestParse_Validation(t *testing.T) {
	testCases := []struct {
		name            string
		args            []string
		wantErrContains string
	}{
		{
			name:            "unknown provision mode rejected",
			args:            append(requiredArgs(), "--provision-mode=aks"),
			wantErrContains: `invalid value "aks"`,
		},
		{
			name: "self-hosted rejects default-nodepool-template-name",
			args: append(requiredArgs(),
				"--provision-mode=self-hosted", "--default-nodepool-template-name=default-pool"),
			wantErrContains: "default-nodepool-template-name",
		},
		{
			name: "gke mode allows default-nodepool-template-name",
			args: append(requiredArgs(), "--default-nodepool-template-name=default-pool"),
		},
		{
			name: "self-hosted accepts FQDN cluster name",
			args: []string{"--project-id=my-project", "--cluster-name=poc0006.k8s.local",
				"--cluster-location=us-central1", "--provision-mode=self-hosted"},
		},
		{
			name: "self-hosted rejects cluster name sanitizing to empty",
			args: []string{"--project-id=my-project", "--cluster-name=---",
				"--cluster-location=us-central1", "--provision-mode=self-hosted"},
			wantErrContains: "sanitizes to an empty GCE label value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clearProvisionModeEnv(t)

			_, err := parseOptions(t, tc.args...)
			if tc.wantErrContains == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErrContains)
		})
	}
}

func TestParse_ClusterLocationNormalization(t *testing.T) {
	testCases := []struct {
		name             string
		provisionMode    string
		clusterLocation  string
		expectedLocation string
	}{
		{
			name:             "self-hosted zonal location becomes region",
			provisionMode:    ProvisionModeSelfHosted,
			clusterLocation:  "us-central1-a",
			expectedLocation: "us-central1",
		},
		{
			name:             "self-hosted two-digit region zone",
			provisionMode:    ProvisionModeSelfHosted,
			clusterLocation:  "europe-west10-a",
			expectedLocation: "europe-west10",
		},
		{
			name:             "self-hosted regional location unchanged",
			provisionMode:    ProvisionModeSelfHosted,
			clusterLocation:  "us-central1",
			expectedLocation: "us-central1",
		},
		{
			name:             "gke mode keeps zonal location",
			provisionMode:    ProvisionModeGKE,
			clusterLocation:  "us-central1-a",
			expectedLocation: "us-central1-a",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clearProvisionModeEnv(t)

			opts, err := parseOptions(t,
				"--project-id=my-project", "--cluster-name=my-cluster",
				"--cluster-location="+tc.clusterLocation, "--provision-mode="+tc.provisionMode)
			require.NoError(t, err)
			require.Equal(t, tc.expectedLocation, opts.ClusterLocation)
			require.Equal(t, tc.expectedLocation, opts.NodeLocation)
		})
	}
}
