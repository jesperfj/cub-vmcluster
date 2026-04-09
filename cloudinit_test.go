package main

import (
	"strings"
	"testing"
)

func TestRenderUserDataIngressEnabled(t *testing.T) {
	cluster := &VMCluster{
		APIVersion: "demo.confighub.com/v1alpha1",
		Kind:       "VMCluster",
		Metadata: VMClusterMeta{
			Name: "test-cluster",
		},
		Spec: VMClusterSpec{
			InstanceType: "t3.medium",
			Region:       "us-east-1",
			DiskSizeGB:   30,
			K3sVersion:   "v1.35.2+k3s1",
			Ingress: IngressSpec{
				Enabled: true,
				Domain:  "test.demo.example.com",
				TLS: TLSSpec{
					Enabled: true,
					Email:   "ops@example.com",
				},
			},
			Worker: WorkerSpec{
				Name: "demo/test-worker",
			},
		},
	}

	manifest := generateWorkerManifest(WorkerManifestParams{
		ConfigHubURL: "https://app.confighub.com",
		WorkerID:     "wkr_test",
		WorkerSecret: "ch_testsecret",
	})

	userData, err := renderUserData(cluster, manifest, &VMClusterBridge{})
	if err != nil {
		t.Fatalf("renderUserData failed: %v", err)
	}

	mustContain := []struct {
		name string
		want string
	}{
		{"shebang", "#!/bin/bash"},
		{"k3s version", "v1.35.2+k3s1"},
		{"tls-san domain", "--tls-san test.demo.example.com"},
		{"disable metrics-server", "--disable metrics-server"},
		{"disable servicelb", "--disable servicelb"},
		{"worker image", "ghcr.io/confighubai/confighub-worker:latest"},
		{"confighub url", "https://app.confighub.com"},
		{"worker id", "wkr_test"},
		{"worker secret", "ch_testsecret"},
		{"cert-manager", "cert-manager.yaml"},
		{"letsencrypt email", "ops@example.com"},
		{"ready tag", `tag_status "ready"`},
	}
	for _, c := range mustContain {
		if !strings.Contains(userData, c.want) {
			t.Errorf("%s: expected %q in user-data, not found", c.name, c.want)
		}
	}

	mustNotContain := []struct {
		name string
		bad  string
	}{
		{"traefik not disabled", "--disable traefik"},
	}
	for _, c := range mustNotContain {
		if strings.Contains(userData, c.bad) {
			t.Errorf("%s: did not expect %q in user-data", c.name, c.bad)
		}
	}
}

func TestRenderUserDataNoIngress(t *testing.T) {
	cluster := &VMCluster{
		APIVersion: "demo.confighub.com/v1alpha1",
		Kind:       "VMCluster",
		Metadata:   VMClusterMeta{Name: "no-ingress"},
		Spec: VMClusterSpec{
			K3sVersion: "v1.35.2+k3s1",
			Ingress: IngressSpec{
				Enabled: false,
			},
			Worker: WorkerSpec{
				Name: "demo/no-ingress-worker",
			},
		},
	}

	manifest := generateWorkerManifest(WorkerManifestParams{
		ConfigHubURL: "https://app.confighub.com",
		WorkerID:     "wkr_noing",
		WorkerSecret: "ch_secret",
	})

	userData, err := renderUserData(cluster, manifest, &VMClusterBridge{})
	if err != nil {
		t.Fatalf("renderUserData failed: %v", err)
	}

	mustContain := []struct {
		name string
		want string
	}{
		{"disable traefik", "--disable traefik"},
		{"disable metrics-server", "--disable metrics-server"},
		{"disable servicelb", "--disable servicelb"},
	}
	for _, c := range mustContain {
		if !strings.Contains(userData, c.want) {
			t.Errorf("%s: expected %q in user-data, not found", c.name, c.want)
		}
	}

	mustNotContain := []struct {
		name string
		bad  string
	}{
		{"no cert-manager", "cert-manager"},
	}
	for _, c := range mustNotContain {
		if strings.Contains(userData, c.bad) {
			t.Errorf("%s: did not expect %q in user-data", c.name, c.bad)
		}
	}
}

func TestGenerateWorkerManifest(t *testing.T) {
	manifest := generateWorkerManifest(WorkerManifestParams{
		ConfigHubURL: "https://hub.confighub.com",
		WorkerID:     "wkr_abc123",
		WorkerSecret: "ch_secret456",
	})

	mustContain := []struct {
		name string
		want string
	}{
		{"namespace", "kind: Namespace"},
		{"service account", "kind: ServiceAccount"},
		{"cluster role binding", "kind: ClusterRoleBinding"},
		{"secret", "kind: Secret"},
		{"deployment", "kind: Deployment"},
		{"default image", "ghcr.io/confighubai/confighub-worker:latest"},
		{"confighub url", "https://hub.confighub.com"},
		{"worker id in secret", "wkr_abc123"},
		{"worker secret in secret", "ch_secret456"},
		{"envFrom secretRef", "secretRef"},
	}
	for _, c := range mustContain {
		if !strings.Contains(manifest, c.want) {
			t.Errorf("%s: expected %q in manifest, not found", c.name, c.want)
		}
	}
}

func TestGenerateWorkerManifestCustomImage(t *testing.T) {
	manifest := generateWorkerManifest(WorkerManifestParams{
		ConfigHubURL: "https://hub.confighub.com",
		WorkerID:     "wkr_test",
		WorkerSecret: "ch_test",
		WorkerImage:  "my-registry.com/worker:v2",
	})

	if !strings.Contains(manifest, "my-registry.com/worker:v2") {
		t.Error("expected custom image in manifest")
	}
	if strings.Contains(manifest, "ghcr.io/confighubai/confighub-worker:latest") {
		t.Error("did not expect default image when custom image is set")
	}
}

func TestParseSlashNotation(t *testing.T) {
	tests := []struct {
		input     string
		wantSpace string
		wantName  string
		wantErr   bool
	}{
		{"demo/test-worker", "demo", "test-worker", false},
		{"my-space/my-unit-config", "my-space", "my-unit-config", false},
		{"noslash", "", "", true},
		{"/no-space", "", "", true},
		{"no-name/", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range tests {
		space, name, err := parseSlashNotation(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseSlashNotation(%q): expected error, got space=%q name=%q", tc.input, space, name)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSlashNotation(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if space != tc.wantSpace || name != tc.wantName {
			t.Errorf("parseSlashNotation(%q): got space=%q name=%q, want space=%q name=%q",
				tc.input, space, name, tc.wantSpace, tc.wantName)
		}
	}
}
