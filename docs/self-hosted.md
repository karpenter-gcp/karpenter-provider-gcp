# Self-hosted clusters

Karpenter GCP can provision nodes for Kubernetes clusters that run on Compute Engine without GKE, such as clusters built with [kOps](https://kops.sigs.k8s.io/). In self-hosted mode, Karpenter launches correctly shaped GCE instances, and your distribution's own bootstrap process joins them to the cluster.

Self-hosted mode applies to the whole Karpenter deployment. One deployment can't mix GKE and self-hosted NodeClasses.

## How self-hosted mode differs from GKE mode

In self-hosted mode, the controller makes no Container API calls, so its service account needs no `container.googleapis.com` permissions. Karpenter reads everything it needs from the `GCENodeClass` and the Compute Engine API:

| Capability           | GKE mode (default)                                    | Self-hosted mode                                                                                       |
|----------------------|-------------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| Node bootstrap       | Cloned from a [bootstrap pool](bootstrap-pool.md)     | `spec.startupScript`, or baked into the image                                                          |
| Image selection      | `family`, `channel`, `version`, `alias`, or `id`      | `id` only                                                                                              |
| Network              | Taken from the GKE cluster                            | Derived from `spec.networkConfig.subnetwork`                                                           |
| External IPs         | Follows the cluster's private-node setting            | Nodes are private unless `networkConfig.enablePrivateNodes` is `false`                                 |
| Network tags         | GKE firewall tags plus `spec.networkTags`             | `spec.networkTags` only                                                                                |
| Kubelet settings     | Applied by Karpenter from `spec.kubeletConfiguration` | Applied by your startup script. Karpenter uses `kubeletConfiguration` only to calculate node capacity. |
| GPU drivers          | Installed by GKE (`spec.gpuDriverVersion`)            | Installed by your startup script or image. `gpuDriverVersion` is ignored.                              |
| Kubelet CSR approval | Karpenter's [CSR approver](security/csr-approval.md)  | Your distribution's node authorization. Karpenter's CSR approver doesn't run.                          |

Consolidation, drift, pricing, Spot capacity, and interruption handling work the same in both modes.

## Enable self-hosted mode

Set `controller.settings.provisionMode` to `self-hosted` when you install the chart. `projectID`, `clusterLocation`, and `clusterName` stay required:

```sh
helm upgrade karpenter "${CHARTS_REPOSITORY}/karpenter" --install --version "$CHART_VERSION" \
  --namespace karpenter-system --create-namespace \
  --set "controller.settings.provisionMode=self-hosted" \
  --set "controller.settings.projectID=${PROJECT_ID}" \
  --set "controller.settings.clusterLocation=${REGION}" \
  --set "controller.settings.clusterName=${CLUSTER_NAME}" \
  --set "controller.priorityClassName=system-cluster-critical" \
  --set "credentials.enabled=false" \
  --wait
```

- **`clusterName`**: Use your distribution's exact cluster name, for example `mycluster.example.com`. Karpenter writes it verbatim to each instance's `cluster-name` metadata key, and some distributions, including kOps, reject nodes whose value doesn't match. Karpenter only manages and garbage-collects instances whose `cluster-name` metadata matches exactly.
- **`clusterLocation`**: A zone is accepted and treated as its region. Choose the zones nodes use with NodePool requirements.
- **`controller.priorityClassName`**: The chart default, `gmp-critical`, exists only on GKE. Set a priority class that exists in your cluster.
- **`credentials.enabled=false`**: Use this when the controller authenticates through the metadata server of the VM it runs on. Otherwise, supply a service account key as described in [Alternative: service account keys](getting-started/installation.md#alternative-service-account-keys).

The controller fails to start in self-hosted mode if `defaultNodePoolTemplateName` is set, because there are no GKE node pools to pin. See [Settings](settings.md) for the `PROVISION_MODE` environment variable and `--provision-mode` flag.

## Configure a GCENodeClass

A self-hosted `GCENodeClass` must set:

- `imageSelectorTerms` that use only `id`. Any Compute Engine image works, including public images from `ubuntu-os-cloud` or `cos-cloud`, as long as it includes the Google guest environment and declares an architecture.
- `disks` with a boot disk.
- `networkConfig.subnetwork`, in the controller's region. Karpenter derives the VPC network from it.
- `kubeletConfiguration.maxPods`, matching the value your startup script gives the kubelet.

```yaml
apiVersion: karpenter.k8s.gcp/v1alpha1
kind: GCENodeClass
metadata:
  name: self-hosted
spec:
  imageSelectorTerms:
    - id: projects/ubuntu-os-cloud/global/images/ubuntu-2404-noble-amd64-v20260601
  disks:
    - category: pd-balanced
      sizeGiB: 60
      boot: true
  networkConfig:
    subnetwork: projects/my-project/regions/us-central1/subnetworks/nodes
  subnetRangeNames:
    - pods                     # optional: secondary range for pod alias IPs
  kubeletConfiguration:
    maxPods: 110               # must match the kubelet your script configures
  networkTags:
    - mycluster-example-com-node
  startupScript: |
    #!/bin/bash
    # Your distribution's bootstrap script. It installs and configures
    # the container runtime, CNI, and kubelet, and joins the node.
    ...
```

Karpenter writes `spec.startupScript` verbatim to the instance's `startup-script` metadata key, and the GCE guest agent runs it. Leave `startupScript` unset when the image already contains your bootstrap. The script can be up to 131,072 characters, and all instance metadata together must stay within the GCE size limits. Karpenter fails the launch with an error naming the oversized key otherwise.

Changing `startupScript` counts as drift, so Karpenter replaces every node provisioned from that `GCENodeClass`.

### Fields that self-hosted mode doesn't support

These fields depend on GKE bootstrap, so self-hosted NodeClasses reject them:

- `autoGPUTaint`, `linuxNodeConfig`, and `gpuSharingConfig`
- `disks[].secondaryBootImage` and `disks[].secondaryBootMode`
- `kubeletConfiguration` fields other than `maxPods`, `podsPerCore`, `systemReserved`, `kubeReserved`, and `evictionHard`

When `startupScript` is set, the API server rejects these fields on admission. For a NodeClass without `startupScript`, Karpenter sets the `ConfigurationReady` condition to `False` with reason `InvalidConfig` and a message naming the field. In GKE mode, a NodeClass that sets `startupScript` reports `ConfigurationReady=False`, and Karpenter doesn't provision from it.

## Write a startup script for Karpenter nodes

Your startup script or image must do the following, or nodes fail to register or Karpenter misjudges their capacity:

- Register the kubelet with the `karpenter.sh/unregistered:NoExecute` taint. Without it, pods can schedule before Karpenter applies the NodePool's labels and taints.
- Set the node's provider ID to `gce://<project>/<zone>/<instance-name>`, the default for the GCP cloud controller manager.
- Configure the kubelet, container runtime, CNI, and any GPU drivers. Set `maxPods`, `systemReserved`, `kubeReserved`, and `evictionHard` in the `GCENodeClass` to match the kubelet, because Karpenter uses them to calculate allocatable capacity.
- For Spot nodes, enable kubelet graceful node shutdown with a non-zero `shutdownGracePeriod`. Karpenter detects Spot preemption from the node condition the kubelet sets during graceful shutdown.
- Run safely more than once. The guest agent runs the startup script on every boot.
- Keep secrets out of the script. Any process on the node can read instance metadata, so fetch credentials at boot instead.

## Use self-hosted mode with kOps

kOps is the reference distribution for self-hosted mode. For a kOps cluster:

- Set `clusterName` to the kOps cluster name.
- Enable vTPM with `spec.shieldedInstanceConfig.enableVtpm: true`, which kOps TPM node authorization requires.
- Add the `kops-k8s-io-instance-group-name` key to `spec.metadata`.
- Use the kOps nodeup script as `startupScript`, and add the kOps node role tag to `networkTags`.

kOps can generate these `GCENodeClass` and NodePool resources for Karpenter-managed instance groups. See the [kOps Karpenter documentation](https://kops.sigs.k8s.io/operations/karpenter/). GCE support in kOps is tracked in [kubernetes/kops#18628](https://github.com/kubernetes/kops/pull/18628).

## Limitations

- Image selection supports only `id`, so Karpenter doesn't pick up new images automatically. Update the image ID to roll nodes.
- IPv6-only and dual-stack nodes aren't supported.
- Windows nodes aren't supported.
