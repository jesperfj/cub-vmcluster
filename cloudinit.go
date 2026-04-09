package main

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed cloudinit/userdata.sh.tmpl
var cloudinitFS embed.FS

type CloudInitParams struct {
	ClusterName    string
	K3sVersion     string
	ConfigHubURL   string
	IngressEnabled bool
	IngressDomain  string
	TLSEmail       string
	WorkerManifest string

	// VMCluster worker self-install
	InstallVMClusterWorker   bool
	VMClusterWorkerID        string
	VMClusterWorkerSecret    string
	VMClusterWorkerImage     string
	VMClusterSubnetID        string
	VMClusterHostedZoneID    string
	VMClusterInstanceProfile string
	AWSRegion                string
}

// WorkerManifestParams holds the values needed to generate a cub-worker Kubernetes manifest.
type WorkerManifestParams struct {
	WorkerImage  string
	ConfigHubURL string
	WorkerID     string
	WorkerSecret string
}

// generateWorkerManifest produces the Kubernetes YAML manifest for deploying cub-worker
// into a k3s cluster. This is the same manifest used both for cloud-init bootstrap and
// as the initial content of the worker config unit.
func generateWorkerManifest(p WorkerManifestParams) string {
	if p.WorkerImage == "" {
		p.WorkerImage = "ghcr.io/confighubai/confighub-worker:latest"
	}

	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: confighub-system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: cub-worker
  namespace: confighub-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cub-worker-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: cub-worker
  namespace: confighub-system
---
apiVersion: v1
kind: Secret
metadata:
  name: cub-worker-secret
  namespace: confighub-system
type: Opaque
stringData:
  CONFIGHUB_WORKER_ID: "%s"
  CONFIGHUB_WORKER_SECRET: "%s"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cub-worker
  namespace: confighub-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cub-worker
  template:
    metadata:
      labels:
        app: cub-worker
    spec:
      serviceAccountName: cub-worker
      containers:
      - name: cub-worker
        image: %s
        env:
        - name: CONFIGHUB_URL
          value: "%s"
        - name: CONFIGHUB_DISABLE_AUTO_TARGET_CREATION
          value: "1"
        - name: NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        envFrom:
        - secretRef:
            name: cub-worker-secret
        volumeMounts:
        - name: tmp
          mountPath: /tmp
      volumes:
      - name: tmp
        emptyDir: {}`, p.WorkerID, p.WorkerSecret, p.WorkerImage, p.ConfigHubURL)
}

func renderUserData(cluster *VMCluster, workerManifest string, bridge *VMClusterBridge) (string, error) {
	tmplData, err := cloudinitFS.ReadFile("cloudinit/userdata.sh.tmpl")
	if err != nil {
		return "", fmt.Errorf("failed to read userdata template: %w", err)
	}

	tmpl, err := template.New("userdata").Parse(string(tmplData))
	if err != nil {
		return "", fmt.Errorf("failed to parse userdata template: %w", err)
	}

	vmclusterImage := cluster.Spec.VMClusterWorkerImage
	if vmclusterImage == "" {
		vmclusterImage = "ghcr.io/jesperfj/cub-vmcluster:latest"
	}

	params := CloudInitParams{
		ClusterName:    cluster.Metadata.Name,
		K3sVersion:     cluster.Spec.K3sVersion,
		ConfigHubURL:   bridge.confighubURL,
		IngressEnabled: cluster.Spec.Ingress.Enabled,
		IngressDomain:  cluster.Spec.Ingress.Domain,
		TLSEmail:       cluster.Spec.Ingress.TLS.Email,
		WorkerManifest: workerManifest,

		InstallVMClusterWorker:   cluster.Spec.InstallVMClusterWorker,
		VMClusterWorkerID:        bridge.confighubID,
		VMClusterWorkerSecret:    bridge.confighubSecret,
		VMClusterWorkerImage:     vmclusterImage,
		VMClusterSubnetID:        bridge.subnetID,
		VMClusterHostedZoneID:    bridge.hostedZoneID,
		VMClusterInstanceProfile: bridge.instanceProfileID,
		AWSRegion:                cluster.Spec.Region,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("failed to render userdata template: %w", err)
	}

	return buf.String(), nil
}
