# cub-vmcluster

A [ConfigHub](https://confighub.com) bridge worker that provisions single-node k3s clusters on AWS EC2. Define a cluster as a KRM resource in ConfigHub, apply it, and get a fully functional Kubernetes cluster with a ConfigHub worker already connected.

No baked AMIs — clusters boot from stock Ubuntu and install everything on the fly in about 2 minutes.

## Quick start

You need:
- [cub CLI](https://docs.confighub.com/cli) authenticated to ConfigHub
- AWS CLI authenticated to an AWS account
- Docker

### 1. Set up AWS infrastructure

The vmcluster worker needs a VPC with a public subnet and an IAM instance profile. If you already have these, skip to step 2.

```bash
# Edit infra/env.sh with your account details
cp infra/env.sh.example infra/env.sh
vi infra/env.sh

# Create VPC, subnet, IAM roles, and optionally a Route53 zone
./infra/ops setup
```

### 2. Bootstrap

The bootstrap script discovers your AWS environment, creates a ConfigHub worker, and generates a `.env` file:

```bash
./bootstrap.sh
```

It will walk you through selecting a VPC, subnet, and Route53 zone, then create a worker in ConfigHub.

### 3. Run the worker

```bash
docker run --rm --env-file .env ghcr.io/jesperfj/cub-vmcluster:main
```

The worker connects to ConfigHub and is ready to receive VMCluster apply operations.

> **Note:** The Docker container needs AWS credentials. If using SSO or profiles, you may need to mount your credentials:
> ```bash
> docker run --rm --env-file .env \
>   -v ~/.aws:/root/.aws:ro \
>   -e AWS_PROFILE=your-profile \
>   ghcr.io/jesperfj/cub-vmcluster:main
> ```

### 4. Create a cluster

Create a unit in ConfigHub with a VMCluster spec:

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
| `spec.installVMClusterWorker` | Deploy vmcluster worker in cluster | `false` |

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

## Infrastructure management

```bash
./infra/ops aws-login              # Authenticate via AWS SSO
./infra/ops setup                  # Create VPC, subnet, IAM (idempotent)
./infra/ops status                 # Show current state
./infra/ops teardown               # Remove infrastructure (warns about instances)
./infra/ops teardown --force       # Terminate all instances and remove everything
```

## Building from source

```bash
go build -o cub-vmcluster .
go test ./...
```

## License

Apache 2.0
