package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPerformInitialSync_ConfigMap(t *testing.T) {
	testData := "test-config-data"
	namespace := "test-namespace"
	resourceName := "test-configmap"
	resourceKey := "config"

	// Create temporary file for test
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-config")

	// Create fake Kubernetes client
	fakeClient := fake.NewClientset()

	// Create ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: namespace,
		},
		Data: map[string]string{
			resourceKey: testData,
		},
	}

	_, err := fakeClient.CoreV1().ConfigMaps(namespace).Create(
		context.Background(),
		configMap,
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test ConfigMap: %v", err)
	}

	// Test performInitialSync
	ctx := context.Background()

	err = performInitialSync(ctx, fakeClient, namespace, string(ResourceTypeConfigMap), resourceName, resourceKey, testFilePath)
	if err != nil {
		t.Fatalf("performInitialSync failed: %v", err)
	}

	// Verify file was written correctly
	content, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(content) != testData {
		t.Errorf("Expected file content %q, got %q", testData, string(content))
	}
}

func TestPerformInitialSync_Secret(t *testing.T) {
	testData := "secret-test-data"
	namespace := "test-namespace"
	resourceName := "test-secret"
	resourceKey := "secret-data"

	// Create temporary file for test
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-secret")

	// Create fake Kubernetes client
	fakeClient := fake.NewClientset()

	// Create Secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			resourceKey: []byte(testData),
		},
	}

	_, err := fakeClient.CoreV1().Secrets(namespace).Create(
		context.Background(),
		secret,
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test Secret: %v", err)
	}

	// Test performInitialSync
	ctx := context.Background()

	err = performInitialSync(ctx, fakeClient, namespace, string(ResourceTypeSecret), resourceName, resourceKey, testFilePath)
	if err != nil {
		t.Fatalf("performInitialSync failed: %v", err)
	}

	// Verify file was written correctly
	content, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(content) != testData {
		t.Errorf("Expected file content %q, got %q", testData, string(content))
	}
}
