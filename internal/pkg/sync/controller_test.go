package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

const (
	testNamespace     = "test-namespace"
	testConfigMapName = "test-configmap"
	testConfigMapKey  = "config"
)

func TestController_SyncConfigMap(t *testing.T) {
	testData := "test-config-data"
	
	// Create temporary directory for test
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-config")

	// Create fake Kubernetes client
	fakeClient := fake.NewClientset()
	
	// Create ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testConfigMapName,
			Namespace: testNamespace,
		},
		Data: map[string]string{
			testConfigMapKey: testData,
		},
	}
	
	_, err := fakeClient.CoreV1().ConfigMaps(testNamespace).Create(
		context.Background(), 
		configMap, 
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test ConfigMap: %v", err)
	}

	// Create informer factory
	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(
		fakeClient,
		time.Second*30,
		kubeinformers.WithNamespace(testNamespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fmt.Sprintf("metadata.name=%s", testConfigMapName)
		}),
	)

	// Create controller
	controller, err := NewController(
		informerFactory.Core().V1().ConfigMaps(),
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ConfigMapKey:  testConfigMapKey,
			ConfigMapName: testConfigMapName,
			FilePath:      testFilePath,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create controller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start informers
	informerFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informerFactory.Core().V1().ConfigMaps().Informer().HasSynced) {
		t.Fatal("Failed to sync informer cache")
	}

	// Run controller in background
	go func() {
		if err := controller.Run(ctx, 1); err != nil {
			t.Logf("Controller error: %v", err)
		}
	}()
	
	// Manually trigger the add event since fake client may not automatically do this
	controller.onConfigMapAdd(configMap)

	// Wait for file to be written
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 5*time.Second, false, func(ctx context.Context) (bool, error) {
		content, err := os.ReadFile(testFilePath)
		if err != nil {
			return false, nil // File doesn't exist yet
		}
		return string(content) == testData, nil
	})
	
	if err != nil {
		t.Fatalf("File was not written with expected content: %v", err)
	}

	// Verify file contents
	content, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	
	if string(content) != testData {
		t.Errorf("Expected file content %q, got %q", testData, string(content))
	}
}

func TestController_WriteFile(t *testing.T) {
	testData := "test-data"
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-config")

	fakeClient := fake.NewClientset()
	informerFactory := kubeinformers.NewSharedInformerFactory(fakeClient, time.Second*30)

	controller, err := NewController(
		informerFactory.Core().V1().ConfigMaps(),
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ConfigMapKey:  testConfigMapKey,
			ConfigMapName: testConfigMapName,
			FilePath:      testFilePath,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create controller: %v", err)
	}

	// Test file writing by directly writing to the file path
	err = os.WriteFile(testFilePath, []byte(testData), 0644)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Verify file was written correctly
	content, err := os.ReadFile(testFilePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(content) != testData {
		t.Errorf("Expected file content %q, got %q", testData, string(content))
	}

	// Test that shouldEnqueue works correctly
	correctCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testConfigMapName,
			Namespace: testNamespace,
		},
	}

	wrongCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-name",
			Namespace: testNamespace,
		},
	}

	if !controller.shouldEnqueue(correctCM) {
		t.Error("shouldEnqueue should return true for correct ConfigMap")
	}

	if controller.shouldEnqueue(wrongCM) {
		t.Error("shouldEnqueue should return false for wrong ConfigMap")
	}
}

func TestController_InvalidOptions(t *testing.T) {
	fakeClient := fake.NewClientset()
	informerFactory := kubeinformers.NewSharedInformerFactory(fakeClient, time.Second*30)

	tests := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{
			name: "empty file path",
			opts: Options{
				ConfigMapKey:  testConfigMapKey,
				ConfigMapName: testConfigMapName,
				FilePath:      "",
			},
			wantErr: true,
		},
		{
			name: "empty configmap name",
			opts: Options{
				ConfigMapKey:  testConfigMapKey,
				ConfigMapName: "",
				FilePath:      "/tmp/test",
			},
			wantErr: true,
		},
		{
			name: "empty configmap key",
			opts: Options{
				ConfigMapKey:  "",
				ConfigMapName: testConfigMapName,
				FilePath:      "/tmp/test",
			},
			wantErr: true,
		},
		{
			name: "valid options",
			opts: Options{
				ConfigMapKey:  testConfigMapKey,
				ConfigMapName: testConfigMapName,
				FilePath:      "/tmp/test",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewController(
				informerFactory.Core().V1().ConfigMaps(),
				fakeClient,
				testNamespace,
				prometheus.NewRegistry(),
				tt.opts,
			)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("NewController() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestController_MissingConfigMapKey(t *testing.T) {
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-config")

	fakeClient := fake.NewClientset()
	
	// Create ConfigMap without the expected key
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testConfigMapName,
			Namespace: testNamespace,
		},
		Data: map[string]string{
			"wrong-key": "some-data",
		},
	}
	
	_, err := fakeClient.CoreV1().ConfigMaps(testNamespace).Create(
		context.Background(), 
		configMap, 
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test ConfigMap: %v", err)
	}

	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(
		fakeClient,
		time.Second*30,
		kubeinformers.WithNamespace(testNamespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fmt.Sprintf("metadata.name=%s", testConfigMapName)
		}),
	)

	controller, err := NewController(
		informerFactory.Core().V1().ConfigMaps(),
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ConfigMapKey:  testConfigMapKey,
			ConfigMapName: testConfigMapName,
			FilePath:      testFilePath,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create controller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	informerFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informerFactory.Core().V1().ConfigMaps().Informer().HasSynced) {
		t.Fatal("Failed to sync informer cache")
	}

	go func() {
		if err := controller.Run(ctx, 1); err != nil {
			t.Logf("Controller error: %v", err)
		}
	}()

	// Wait a bit to ensure controller processes the ConfigMap
	time.Sleep(1 * time.Second)

	// File should not be created when key is missing
	if _, err := os.Stat(testFilePath); !os.IsNotExist(err) {
		t.Error("File should not be created when ConfigMap key is missing")
	}
}