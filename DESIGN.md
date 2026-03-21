# cub-vmcluster

A ConfigHub worker that provisions single-node k3s clusters on EC2 instances for demo environments. One KRM resource in, one fully functional cluster out — with a ConfigHub worker pre-installed and connected.

## Overview

`cub-vmcluster` implements a custom ConfigHub bridge (`VMCluster` provider) that:

1. Accepts a `VMCluster` KRM resource via a ConfigHub unit
2. Provisions an EC2 instance in a demo AWS account (cross-account IAM)
3. Bootstraps k3s + ingress + TLS on the instance via cloud-init
4. Passes pre-created worker credentials (from the VMCluster spec) to the instance via user-data
5. The on-instance `cub-worker` connects back to ConfigHub using those credentials
6. Reports granular progress back to ConfigHub throughout the process

> **Note:** Worker credentials are currently stored directly in the VMCluster spec. This is a known shortcut for initial development. Future iterations should auto-register workers or deliver credentials out-of-band (e.g., SSM Parameter Store).

The result is a cheap, ephemeral Kubernetes cluster with public ingress, reachable via kubectl and HTTP, managed entirely through ConfigHub.

## Config Format

Units use `Kubernetes/YAML` toolchain with `VMCluster` provider. The resource format:

```yaml
apiVersion: demo.confighub.com/v1alpha1
kind: VMCluster
metadata:
  name: acme-demo
  labels:
    customer: acme
    purpose: demo
spec:
  # EC2 provisioning
  instanceType: t3.medium
  region: us-east-1
  diskSizeGB: 30

  # K3s config
  k3sVersion: v1.35.2+k3s1

  # Networking
  ingress:
    enabled: true
    domain: acme.demo.confighub.com
    tls:
      enabled: true
      email: ops@confighub.com

  # ConfigHub worker bootstrap
  # NOTE: Storing credentials in config is a temporary shortcut.
  # The worker must be pre-created in ConfigHub before applying.
  # Future: auto-register workers or deliver credentials via SSM.
  worker:
    confighubURL: https://app.confighub.com
    workerID: wkr_acme-demo
    workerSecret: ch_xxx
    providerTypes:
      - Kubernetes

```

## Architecture

```
┌─────────────────────────────────────────┐
│  Main AWS Account (ConfigHub)           │
│                                         │
│  ┌─────────────────────────────┐        │
│  │  EKS Cluster                │        │
│  │  ┌───────────────────────┐  │        │
│  │  │ cub-vmcluster pod     │  │        │
│  │  │ (VMCluster bridge)    │──┼────┐   │
│  │  └───────────────────────┘  │    │   │
│  └─────────────────────────────┘    │   │
│                                     │   │
│  IAM Role: vmcluster-provisioner    │   │
│  (AssumeRole to demo account)       │   │
└─────────────────────────────────────┼───┘
                                      │
              STS AssumeRole          │
                                      │
┌─────────────────────────────────────┼───┐
│  Demo AWS Account                   │   │
│                                     ▼   │
│  ┌──────────────────────────────────┐   │
│  │  EC2: acme-demo                  │   │
│  │  ┌────────────────────────────┐  │   │
│  │  │  k3s (single node)        │  │   │
│  │  │  ┌──────────────────────┐ │  │   │
│  │  │  │ cub-worker           │─┼──┼───┼──→ ConfigHub API
│  │  │  │ (kubernetes bridge)  │ │  │   │
│  │  │  └──────────────────────┘ │  │   │
│  │  │  ┌──────────────────────┐ │  │   │
│  │  │  │ traefik ingress      │ │  │   │
│  │  │  └──────────────────────┘ │  │   │
│  │  └────────────────────────────┘  │   │
│  └──────────────────────────────────┘   │
│                                         │
│  Route53: *.demo.confighub.com          │
│  Security Group: 443, 6443          │
└─────────────────────────────────────────┘
```

### Two Workers in Play

| Worker | Runs on | Bridge | Purpose |
|--------|---------|--------|---------|
| `cub-vmcluster` | Main EKS cluster | VMCluster (custom) | Provisions/manages EC2 instances |
| `cub-worker` (per cluster) | Each EC2 instance | Kubernetes (standard) | Deploys workloads to the k3s cluster |

## Bridge Implementation

### Provider Registration

```go
const ProviderVMCluster api.ProviderType = "VMCluster"
```

Toolchain: `Kubernetes/YAML` — reuses existing ConfigHub UI, diffing, revision tracking, and link resolution.

### Operations

| Operation | Behavior |
|-----------|----------|
| **Apply** | Parse VMCluster YAML. First apply: create security group, launch EC2 with cloud-init user-data, configure DNS. Update: handle mutable fields (instanceType → stop/change/start). |
| **Refresh** | DescribeInstances → build LiveData reflecting actual state (running, IP, worker connected). Detect drift (instance terminated externally, SG changed). Store kubeconfig in LiveState. |
| **Destroy** | Terminate instance, clean up security group, remove DNS record. |

### Apply Flow

1. Parse VMCluster YAML from `payload.Data`
2. Check LiveState for existing instance ID
   - If exists and spec unchanged: no-op, report Synced
   - If exists and mutable field changed: update in-place
   - If not exists: provision new
3. Provision:
   a. Create/verify Security Group (6443, 80, 443). No SSH — use SSM Session Manager for shell access.
   b. Read worker credentials from spec (workerID + workerSecret)
   c. Render cloud-init user-data script with cluster-specific values (credentials passed via user-data)
   d. Launch EC2 instance with user-data and tags
   e. Wait for instance running
   f. Create/update Route53 record: `*.{name}.demo.confighub.com` → public IP
   g. Store instance metadata in LiveState
4. Poll instance tag `confighub:status` for boot progress
5. Report granular `ResourceStatuses` throughout:
   - `EC2Instance` → Synced/InProgress
   - `SecurityGroup` → Synced/Ready
   - `DNSRecord` → Synced/Ready
   - `K3sCluster` → Pending/InProgress
   - `ConfigHubWorker` → Pending/Unknown
6. Report `ApplyCompleted` when instance reports ready

### LiveState

```json
{
  "instanceID": "i-0abc123def456",
  "publicIP": "54.123.45.67",
  "privateIP": "10.0.1.42",
  "state": "running",
  "launchTime": "2026-03-20T10:00:00Z",
  "securityGroupID": "sg-0abc123",
  "workerID": "wkr_acme-demo",
  "workerConnected": true,
  "k3sReady": true,
  "dnsRecord": "acme.demo.confighub.com",
  "kubeconfig": "apiVersion: v1\nclusters:\n- cluster:\n    server: https://54.123.45.67:6443\n  ..."
}
```

## No Baked AMI

Clusters boot from stock Ubuntu and install everything via cloud-init:

| Step | Time |
|------|------|
| EC2 launch + cloud-init start | ~30-40s |
| Download + install k3s (~60MB) | ~5-10s |
| k3s first boot (certs, startup) | ~20-30s |
| k3s deploys cub-worker + ingress from auto-deploy manifests | ~30-60s |
| Worker connects to ConfigHub | ~5s |
| **Total** | **~2-3 min** |

The cub-worker runs as a Deployment inside k3s, pulled from `ghcr.io/confighubai/confighub-worker`. Cloud-init drops the manifest into `/var/lib/rancher/k3s/server/manifests/` and k3s auto-deploys it on startup. No binary extraction needed.

Benefits over baked AMIs:
- `spec.k3sVersion` just works — downloads the exact version at boot
- cub-worker image version is always fresh (or pinnable in the VMCluster spec)
- No Packer pipeline, no AMI rebuilds, no cross-region copies
- Only one binary download (k3s), everything else runs as k3s workloads

The cloud-init script signals progress by tagging the EC2 instance with `confighub:status`. The bridge polls this tag during Apply to report progress to ConfigHub.

## Cross-Account AWS Setup

### Main Account (worker pod via IRSA)

```json
{
  "Effect": "Allow",
  "Action": "sts:AssumeRole",
  "Resource": "arn:aws:iam::DEMO_ACCOUNT:role/vmcluster-provisioner"
}
```

### Demo Account (trust policy)

```json
{
  "Effect": "Allow",
  "Principal": {"AWS": "arn:aws:iam::MAIN_ACCOUNT:role/vmcluster-worker"},
  "Action": "sts:AssumeRole"
}
```

### Demo Account (permissions)

- `ec2:*` scoped to tagged resources (`confighub:managed-by = cub-vmcluster`)
- `route53:ChangeResourceRecordSets` scoped to hosted zone
- `ssm:*` for SSM Session Manager (shell access to instances)

EC2 instances get an instance profile with `AmazonSSMManagedInstanceCore` for SSM agent connectivity. Kubeconfig is stored in the unit's LiveState — no Secrets Manager needed.

## ConfigHub Modeling

```
Space: demo-clusters
├── Unit: acme-demo          (VMCluster)          → Target: vmcluster-bridge
├── Unit: bigcorp-demo       (VMCluster)          → Target: vmcluster-bridge
└── Unit: internal-test      (VMCluster)          → Target: vmcluster-bridge

Space: acme-demo-workloads                        (created after cluster boots)
├── Unit: app-deployment     (Kubernetes/YAML)    → Target: acme-demo-k3s
├── Unit: app-service        (Kubernetes/YAML)    → Target: acme-demo-k3s
└── Unit: ingress            (Kubernetes/YAML)    → Target: acme-demo-k3s
```

Links can wire cluster outputs (IP, domain) into workload units for ingress configuration.

## V1 Scope

V1 implements Apply, Refresh, and Destroy. The following are deferred to future iterations:

- **TTL / auto-sleep**: Automatic expiry and instance scheduling
- **Import**: Discovering existing EC2-based clusters
- **Auto worker registration**: Creating workers programmatically instead of pre-creating and pasting credentials

## Project Structure

```
cub-vmcluster/
├── main.go                    # Worker entrypoint
├── bridge.go                  # BridgeWorker interface implementation
├── apply.go                   # Apply operation
├── refresh.go                 # Refresh operation
├── destroy.go                 # Destroy operation
├── types.go                   # VMCluster resource types
├── cloudinit.go               # Cloud-init template rendering
├── aws.go                     # AWS client helpers (EC2, Route53, STS)
├── cloudinit/
│   └── userdata.sh.tmpl       # Cloud-init script template
├── Dockerfile
├── .github/
│   └── workflows/
│       └── build.yaml         # Build + push container image
├── go.mod
├── go.sum
├── DESIGN.md                  # This file
├── LICENSE                    # Apache 2.0
└── README.md
```

## Build & Deploy

```bash
# Build
go build -o bin/cub-vmcluster .

# Container
docker build -t ghcr.io/jesperfj/cub-vmcluster:latest .

# Run (locally or in k8s)
CONFIGHUB_URL=https://app.confighub.com \
CONFIGHUB_WORKER_ID=wkr_vmcluster \
CONFIGHUB_WORKER_SECRET=ch_xxx \
AWS_ROLE_ARN=arn:aws:iam::DEMO_ACCOUNT:role/vmcluster-provisioner \
ROUTE53_HOSTED_ZONE_ID=Z1234567890 \
./bin/cub-vmcluster
```
