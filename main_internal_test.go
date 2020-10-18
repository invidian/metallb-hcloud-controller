package main

import (
	"os"
	"testing"
)

func TestNewHcloudClientNoTokenSet(t *testing.T) {
	if err := os.Setenv(hcloudTokenEnv, ""); err != nil {
		t.Fatalf("Emptying %q environment variable: %v", hcloudTokenEnv, err)
	}

	c, err := newHcloudClient()
	if err == nil {
		t.Fatalf("Creating client with empty token should fail")
	}

	if c != nil {
		t.Fatalf("Creating bad client should not return client object")
	}
}

func TestNewHcloudClientBadToken(t *testing.T) {
	if err := os.Setenv(hcloudTokenEnv, "FOO"); err != nil {
		t.Fatalf("Setting %q environment variable: %v", hcloudTokenEnv, err)
	}

	if _, err := newHcloudClient(); err == nil {
		t.Fatalf("Creating client with bad token should fail")
	}
}

func TestNewFloatingIPSyncerBadHcloudClient(t *testing.T) {
	if err := os.Setenv(hcloudTokenEnv, ""); err != nil {
		t.Fatalf("Emptying %q environment variable: %v", hcloudTokenEnv, err)
	}

	if _, err := newFloatingIPSyncer(); err == nil {
		t.Fatalf("Creating syncer should fail with bad Hetzner Cloud client: %v", err)
	}
}

func TestKubeconfigPathFromEnv(t *testing.T) {
	expectedPath := "/test"
	if err := os.Setenv(kubeconfigEnv, expectedPath); err != nil {
		t.Fatalf("Setting %q environment variable: %v", kubeconfigEnv, err)
	}

	path, err := kubeconfigPath()
	if err != nil {
		t.Fatalf("Getting kubeconfig path with %q environment variable set should work, got: %v", kubeconfigEnv, err)
	}

	if path != expectedPath {
		t.Fatalf("Unexpected path, wanted %q, got %q", expectedPath, path)
	}
}

func TestKubeconfigPathFromHome(t *testing.T) {
	if err := os.Setenv(kubeconfigEnv, ""); err != nil {
		t.Fatalf("Setting %q environment variable: %v", kubeconfigEnv, err)
	}

	path, err := kubeconfigPath()
	if err != nil {
		t.Fatalf("Getting kubeconfig path without %q environment variable set should work, got: %v", kubeconfigEnv, err)
	}

	if path == "" {
		t.Fatalf("Returned path should not be empty")
	}
}

func TestKubeconfigNonExistent(t *testing.T) {
	if err := os.Setenv(kubeconfigEnv, "/nonexistent"); err != nil {
		t.Fatalf("Setting %q environment variable: %v", kubeconfigEnv, err)
	}

	if _, err := kubeconfig(); err == nil {
		t.Fatalf("Creating Kubernetes client from non existent kubeconfig should fail")
	}
}

func TestKubeconfigBadInCluster(t *testing.T) {
	envVars := []string{"KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT"}
	for _, envVar := range envVars {
		if err := os.Setenv(envVar, "nonempty"); err != nil {
			t.Fatalf("Setting %q environment variable: %v", envVar, err)
		}
	}

	if _, err := kubeconfig(); err == nil {
		t.Fatalf("Creating Kubernetes client in broken in-cluster config should fail")
	}
}
