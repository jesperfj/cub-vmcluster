# cub-vmcluster

A [ConfigHub](https://confighub.com) bridge worker that provisions single-node k3s clusters on AWS EC2. Define a cluster as a KRM resource in ConfigHub, apply it, and get a fully functional Kubernetes cluster with a ConfigHub worker already connected.

No baked AMIs — clusters boot from stock Ubuntu and install everything on the fly in about 2 minutes.

## Quick start

You need:
- [cub CLI](https://docs.confighub.com/cli) authenticated to ConfigHub
- AWS CLI configured with at least one profile
- Docker

### 1. Initialize

```bash
./vmctl init
```

This interactive command:
- Selects your AWS profile and authenticates (SSO login if needed)
- Creates AWS infrastructure (VPC, subnet, IAM roles) if it doesn't exist
- Discovers your VPCs, subnets, and Route53 zones
- Creates a ConfigHub worker
- Creates the first VMCluster unit
- Writes a `.env` file with all configuration

If you already have a VPC and instance profile, skip infrastructure creation:

```bash
./vmctl init --skip-infra
```

### 2. Run the worker

The init output includes the exact Docker command. It looks like:

```bash
docker run --rm --env-file .env -v $HOME/.aws:/root/.aws:ro ghcr.io/jesperfj/cub-vmcluster:latest
```

### 3. Apply the cluster

Open ConfigHub, assign the VMCluster unit to the vmcluster target (shown in the init output), and hit Apply. Your cluster will be ready in about 2 minutes.

### 4. Create more clusters

Create additional VMCluster units in ConfigHub:

```yaml
apiVersion: demo.confighub.com/v1alpha1
kind: VMCluster
metadata:
  name: my-cluster
spec:
  instanceType: t4g.small
  region: us-east-2
  diskSizeGB: 30
  k3sVersion: v1.35.2+k3s1
  ingress:
    enabled: true
    domain: my-cluster.example.com
    tls:
      enabled: true
      email: ops@example.com
  worker:
    confighubURL: https://hub.confighub.com
    slug: my-cluster-worker
    spaceSlug: my-space
    providerTypes:
      - Kubernetes
```

Assign it to the vmcluster worker's target and apply. Within a couple minutes you'll have a running k3s cluster with its own ConfigHub worker connected and ready for workloads.

### Without DNS

DNS is optional. If you don't have a Route53 hosted zone, omit the ingress section:

```yaml
apiVersion: demo.confighub.com/v1alpha1
kind: VMCluster
metadata:
  name: my-cluster
spec:
  instanceType: t4g.small
  region: us-east-2
  diskSizeGB: 30
  k3sVersion: v1.35.2+k3s1
  worker:
    confighubURL: https://hub.confighub.com
    slug: my-cluster-worker
    spaceSlug: my-space
    providerTypes:
      - Kubernetes
```

The cluster will still be fully functional — you can deploy workloads and access them by IP. Ingress with hostnames and TLS requires a Route53 hosted zone.

### DNS setup

If you want hostname-based ingress with TLS, you need a domain with DNS hosted in Route53. The bootstrap script will discover available zones. For each cluster, the bridge creates:

- `<cluster-name>.<domain>` → instance public IP
- `*.<cluster-name>.<domain>` → instance public IP (wildcard)

This means any service deployed with an ingress hostname like `myapp.cluster1.example.com` will route to the cluster automatically. TLS certificates are provisioned via cert-manager and Let's Encrypt.

## Self-hosting

The vmcluster worker can move into one of its own clusters so you don't need to keep running it locally. Add `installVMClusterWorker: true` to the first cluster you provision:

```yaml
spec:
  installVMClusterWorker: true
```

This deploys the vmcluster worker as a pod in the cluster using the same credentials. Once it connects to ConfigHub:

1. Stop the local Docker container
2. The in-cluster worker takes over and can provision more clusters

## VMCluster spec reference

| Field | Description | Default |
|-------|-------------|---------|
| `spec.instanceType` | EC2 instance type (ARM and x86 supported) | `t3.medium` |
| `spec.region` | AWS region | `us-east-1` |
| `spec.diskSizeGB` | Root EBS volume size in GB | `30` |
| `spec.k3sVersion` | k3s release version (e.g. `v1.35.2+k3s1`) | required |
| `spec.ingress.enabled` | Enable Traefik ingress | `false` |
| `spec.ingress.domain` | Domain for the cluster (A + wildcard records) | — |
| `spec.ingress.tls.enabled` | Enable TLS via cert-manager + Let's Encrypt | `false` |
| `spec.ingress.tls.email` | Let's Encrypt registration email | — |
| `spec.worker.confighubURL` | ConfigHub server URL | required |
| `spec.worker.slug` | Worker name (created automatically) | required |
| `spec.worker.spaceSlug` | ConfigHub space for the worker | required |
| `spec.worker.providerTypes` | Bridge provider types | `["Kubernetes"]` |
| `spec.worker.image` | Container image for the cub-worker | `ghcr.io/confighubai/confighub-worker:latest` |
| `spec.installVMClusterWorker` | Deploy vmcluster worker in cluster | `false` |
| `spec.vmclusterWorkerImage` | Container image for self-hosted worker | `ghcr.io/jesperfj/cub-vmcluster:latest` |

## How it works

```
  Local (bootstrap)                 EC2 Instance
  ┌─────────────────────┐          ┌──────────────────────────┐
  │ cub-vmcluster       │          │ k3s                      │
  │ (Docker container)  │──EC2──>  │ ├── cub-worker ──────────┼──> ConfigHub
  └─────────────────────┘          │ ├── traefik (ingress)    │
           │                       │ ├── cert-manager (TLS)   │
           │                       │ └── cub-vmcluster (opt)  │
           ▼                       └──────────────────────────┘
      ConfigHub API
```

The bridge:
1. Parses the VMCluster KRM resource
2. Creates a ConfigHub worker for the cluster (via the API)
3. Provisions an EC2 instance with cloud-init that installs k3s
4. Deploys the ConfigHub worker as a k3s pod (auto-deploys via `/var/lib/rancher/k3s/server/manifests/`)
5. Creates Route53 DNS records (A + wildcard) if configured
6. Reports progress back to ConfigHub throughout the process

## Cluster access

```bash
./vmctl list                              # List running clusters
./vmctl resize cluster1 t4g.medium        # Resize instance (stop/change/start)
./vmctl kubeconfig cluster1               # Get kubeconfig (server rewritten to public IP)
./vmctl shell cluster1                    # Open an interactive SSM shell
./vmctl exec cluster1 -- kubectl get pods -A  # Run a command remotely
```

To use the kubeconfig with kubectl:

```bash
./vmctl kubeconfig cluster1 > /tmp/cluster1.yaml
KUBECONFIG=/tmp/cluster1.yaml kubectl get nodes
```

No SSH keys or open ports required — access is via AWS Systems Manager.

## Infrastructure management

```bash
./vmctl status                     # Show infrastructure and running clusters
./vmctl teardown                   # Remove AWS infrastructure (warns about instances)
./vmctl teardown --force           # Terminate all instances and remove everything
```

## Limitations and future work

### Re-apply behavior

Changing a VMCluster spec and re-applying has limited support:

- **If the instance is running**, re-apply reports "already running" and makes no changes. To apply a new configuration, destroy first and then re-apply. This means changes to instance type, disk size, k3s version, or ingress settings require a full reprovision.
- **If the instance is terminated or gone**, re-apply provisions a fresh cluster.

Future: detect spec changes and handle mutable updates (e.g., DNS changes) without full reprovision.

### Refresh

Refresh checks if the EC2 instance is still running and reports drift if it's stopped or terminated. It does not currently:

- Verify k3s is healthy
- Check if the ConfigHub worker is actually connected
- Pull the kubeconfig into LiveState

### Destroy

Destroy deletes the worker deployment (clean disconnect), terminates the instance, removes the security group, and removes DNS records. It does not currently:

- Delete the worker entity from ConfigHub (intentional — preserves audit trail and allows re-apply with the same worker)
- Clean up orphaned EBS volumes (shouldn't happen with instance-store termination, but not verified)

### Other limitations

- **Single availability zone** — clusters are provisioned in a single AZ (the subnet's AZ). No HA.
- **No persistent storage** — EBS root volume is deleted on termination. These are ephemeral demo clusters.
- **No Import** — the Import bridge operation is not implemented. Existing EC2 instances cannot be adopted as VMClusters.
- **TTL / auto-sleep** — no automatic expiry or instance scheduling. Clusters run until explicitly destroyed.
- **Boot time** — clusters take ~2 minutes to boot. Most of the time is k3s startup and cert-manager deployment.

## Building from source

```bash
go build -o cub-vmcluster .
go test ./...
```

## License

MIT
