# cub-vmcluster

A [ConfigHub](https://confighub.com) bridge worker that provisions single-node k3s clusters on AWS EC2. Define a cluster as a KRM resource in ConfigHub, apply it, and get a fully functional Kubernetes cluster with a ConfigHub worker pre-installed and connected.

## How it works

1. You define a `VMCluster` resource in a ConfigHub unit
2. The bridge provisions an EC2 instance with k3s via cloud-init
3. A ConfigHub worker is deployed inside the cluster and connects back to ConfigHub
4. You can now deploy workloads to the cluster through ConfigHub

No baked AMIs — instances boot from stock Ubuntu and install everything on the fly (~2 minutes).

## Quick start

### Prerequisites

- An AWS account with [AWS CLI](https://aws.amazon.com/cli/) configured
- A [ConfigHub](https://confighub.com) account
- Go 1.25+ (to build from source)

### 1. Set up AWS infrastructure

Edit `infra/env.sh` with your AWS account ID, region, and optionally a Route53 domain:

```bash
cd infra
./ops aws-login
./ops setup
```

This creates a VPC, public subnet, and IAM instance profile. The setup output shows the exact env vars for the next step.

### 2. Create a worker in ConfigHub

Create a bridge worker in ConfigHub. Note the worker ID and secret.

### 3. Run the vmcluster worker

```bash
go build -o cub-vmcluster .

CONFIGHUB_URL=https://hub.confighub.com \
CONFIGHUB_WORKER_ID=<worker-id> \
CONFIGHUB_WORKER_SECRET=<worker-secret> \
AWS_PROFILE=<your-profile> \
SUBNET_ID=<subnet-id> \
INSTANCE_PROFILE_NAME=vmcluster-instance \
ROUTE53_HOSTED_ZONE_ID=<zone-id> \
./cub-vmcluster
```

### 4. Create a VMCluster

Create a unit in ConfigHub with this YAML:

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

Assign the unit to the vmcluster worker's target and apply. A new cluster will be provisioned and its worker will connect to ConfigHub.

## Self-hosting (bootstrap pivot)

The vmcluster worker can run inside one of the clusters it provisions. Add `installVMClusterWorker: true` to the spec:

```yaml
spec:
  installVMClusterWorker: true
  # ... rest of spec
```

This deploys the vmcluster worker itself as a pod in the cluster using the same credentials. Once it connects, stop the local worker. The in-cluster worker takes over and can provision additional clusters.

## VMCluster spec reference

| Field | Description | Default |
|-------|-------------|---------|
| `spec.instanceType` | EC2 instance type | `t3.medium` |
| `spec.region` | AWS region | `us-east-1` |
| `spec.diskSizeGB` | Root EBS volume size | `30` |
| `spec.k3sVersion` | k3s release version | required |
| `spec.ingress.enabled` | Enable ingress | `false` |
| `spec.ingress.domain` | Domain for the cluster | - |
| `spec.ingress.tls.enabled` | Enable TLS via cert-manager | `false` |
| `spec.ingress.tls.email` | Let's Encrypt email | - |
| `spec.worker.confighubURL` | ConfigHub server URL | required |
| `spec.worker.slug` | Worker name (auto-created) | required |
| `spec.worker.spaceSlug` | Space for the worker | required |
| `spec.worker.providerTypes` | Bridge provider types | `["Kubernetes"]` |
| `spec.installVMClusterWorker` | Deploy vmcluster worker in cluster | `false` |

## Infrastructure management

The `infra/ops` script manages AWS infrastructure:

```bash
./infra/ops aws-login              # Authenticate via AWS SSO
./infra/ops setup                  # Create infrastructure (idempotent)
./infra/ops status                 # Show current state
./infra/ops teardown               # Remove infrastructure (prompts for instances)
./infra/ops teardown --force       # Terminate all instances and remove everything
```

## Architecture

```
  Your machine (bootstrap)          EC2 Instance
  ┌─────────────────────┐          ┌──────────────────────────┐
  │ cub-vmcluster       │          │ k3s                      │
  │ (VMCluster bridge)  │──EC2──>  │ ├── cub-worker ──────────┼──> ConfigHub
  └─────────────────────┘          │ ├── traefik (ingress)    │
           │                       │ ├── cert-manager (TLS)   │
           │                       │ └── cub-vmcluster (opt)  │
           ▼                       └──────────────────────────┘
      ConfigHub API
```

## License

Apache 2.0
