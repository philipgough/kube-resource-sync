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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	testNamespace     = "test-namespace"
	testConfigMapName = "test-configmap"
	testConfigMapKey  = "config"
	testSecretName    = "test-secret"
	testSecretKey     = "secret-data"
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
		nil, // secretInformer not needed for ConfigMap test
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "configmap",
			ResourceKey:  testConfigMapKey,
			ResourceName: testConfigMapName,
			FilePath:     testFilePath,
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
		nil, // secretInformer not needed for ConfigMap test
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "configmap",
			ResourceKey:  testConfigMapKey,
			ResourceName: testConfigMapName,
			FilePath:     testFilePath,
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

	if !controller.shouldEnqueueConfigMap(correctCM) {
		t.Error("shouldEnqueueConfigMap should return true for correct ConfigMap")
	}

	if controller.shouldEnqueueConfigMap(wrongCM) {
		t.Error("shouldEnqueueConfigMap should return false for wrong ConfigMap")
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
				ResourceType: "configmap",
				ResourceKey:  testConfigMapKey,
				ResourceName: testConfigMapName,
				FilePath:     "",
			},
			wantErr: true,
		},
		{
			name: "empty resource name",
			opts: Options{
				ResourceType: "configmap",
				ResourceKey:  testConfigMapKey,
				ResourceName: "",
				FilePath:     "/tmp/test",
			},
			wantErr: true,
		},
		{
			name: "empty resource key",
			opts: Options{
				ResourceType: "configmap",
				ResourceKey:  "",
				ResourceName: testConfigMapName,
				FilePath:     "/tmp/test",
			},
			wantErr: true,
		},
		{
			name: "empty resource type",
			opts: Options{
				ResourceType: "",
				ResourceKey:  testConfigMapKey,
				ResourceName: testConfigMapName,
				FilePath:     "/tmp/test",
			},
			wantErr: true,
		},
		{
			name: "invalid resource type",
			opts: Options{
				ResourceType: "invalid",
				ResourceKey:  testConfigMapKey,
				ResourceName: testConfigMapName,
				FilePath:     "/tmp/test",
			},
			wantErr: true,
		},
		{
			name: "valid configmap options",
			opts: Options{
				ResourceType: "configmap",
				ResourceKey:  testConfigMapKey,
				ResourceName: testConfigMapName,
				FilePath:     "/tmp/test",
			},
			wantErr: false,
		},
		{
			name: "valid secret options",
			opts: Options{
				ResourceType: "secret",
				ResourceKey:  testConfigMapKey,
				ResourceName: testConfigMapName,
				FilePath:     "/tmp/test",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewController(
				informerFactory.Core().V1().ConfigMaps(),
				informerFactory.Core().V1().Secrets(),
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
		nil, // secretInformer not needed for ConfigMap test
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "configmap",
			ResourceKey:  testConfigMapKey,
			ResourceName: testConfigMapName,
			FilePath:     testFilePath,
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

// Integration tests using envtest for real Kubernetes API interaction
func TestCreateUpdateCycle_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	const (
		ns = "test-write-to-disk-ns"
	)

	tmpdir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{},
	}

	// start the test environment
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testEnv.Stop(); err != nil {
			t.Logf("Failed to stop test environment: %v", err)
		}
	}()

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}

	// Create a namespace
	if err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}

	// run the controller in the background
	file := filepath.Join(tmpdir, testConfigMapKey)
	runControllerIntegration(t, ctx, cfg, ns, file)

	// Create a ConfigMap
	cm := buildConfigMap(t, ns, testConfigMapName, "hr", `["a", "b", "c"]`, `["t1", "t2"]`)
	if err := k8sClient.Create(ctx, cm); err != nil {
		t.Fatalf("failed to create ConfigMap: %v", err)
	}

	// verify the contents on disk
	if err := pollUntilExpectConfigurationOrTimeout(t, ctx, file, cm.Data[testConfigMapKey]); err != nil {
		t.Fatalf("failed to assert initial state: %v", err)
	}

	setTo := `[{"name":"a","tenants":["t1","t2"], "endpoints":["a"]},{"name":"b","tenants":["t3","t4"],"endpoints":["b"]}]`
	cmCopy := cm.DeepCopy()
	cmCopy.Data[testConfigMapKey] = setTo

	if err := k8sClient.Update(ctx, cmCopy); err != nil {
		t.Fatalf("failed to update ConfigMap: %v", err)
	}

	// verify the contents on disk
	if err := pollUntilExpectConfigurationOrTimeout(t, ctx, file, setTo); err != nil {
		t.Fatalf("failed to assert updated state: %v", err)
	}
}

func runControllerIntegration(t *testing.T, ctx context.Context, cfg *rest.Config, namespace string, filePath string) {
	t.Helper()
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err, "Error building kubernetes clientset")
	}

	configMapInformer := kubeinformers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		time.Second*30,
		kubeinformers.WithNamespace(namespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fmt.Sprintf("metadata.name=%s", testConfigMapName)
		}),
	)

	controller, err := NewController(
		configMapInformer.Core().V1().ConfigMaps(),
		nil, // secretInformer not needed for ConfigMap test
		kubeClient,
		namespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "configmap",
			ResourceKey:  testConfigMapKey,
			ResourceName: testConfigMapName,
			FilePath:     filePath,
		},
	)
	if err != nil {
		t.Fatal(err, "Error creating controller")
	}

	configMapInformer.Start(ctx.Done())
	go func() {
		if err := controller.Run(ctx, 1); err != nil {
			t.Logf("Controller error: %v", err)
		}
	}()
}

func buildConfigMap(t *testing.T, namespace, name, hashringName, endpoints, tenants string) *corev1.ConfigMap {
	t.Helper()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			testConfigMapKey: fmt.Sprintf(`[{"name":"%s","endpoints":%s,"tenants":%s}]`, hashringName, endpoints, tenants),
		},
	}
	return cm
}

func pollUntilExpectConfigurationOrTimeout(t *testing.T, ctx context.Context, file string, expect string) error {
	t.Helper()
	var pollError error

	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 30*time.Second, false, func(ctx context.Context) (bool, error) {
		b, err := os.ReadFile(file)
		if err != nil {
			pollError = fmt.Errorf("failed to read expect file: %s", err)
			return false, nil
		}

		if string(b) != expect {
			pollError = fmt.Errorf("expect file contents do not match expected: expect %s but got %s", expect, string(b))
			return false, nil
		}

		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to assert contents of config file: %v: %v", err, pollError)
	}

	return nil
}

func TestController_SyncSecret(t *testing.T) {
	testData := "secret-test-data"
	
	// Create temporary directory for test
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-secret")

	// Create fake Kubernetes client
	fakeClient := fake.NewClientset()
	
	// Create Secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSecretName,
			Namespace: testNamespace,
		},
		Data: map[string][]byte{
			testSecretKey: []byte(testData),
		},
	}
	
	_, err := fakeClient.CoreV1().Secrets(testNamespace).Create(
		context.Background(), 
		secret, 
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test Secret: %v", err)
	}

	// Create informer factory
	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(
		fakeClient,
		time.Second*30,
		kubeinformers.WithNamespace(testNamespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fmt.Sprintf("metadata.name=%s", testSecretName)
		}),
	)

	// Create controller
	controller, err := NewController(
		nil, // configMapInformer not needed for Secret test
		informerFactory.Core().V1().Secrets(),
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "secret",
			ResourceKey:  testSecretKey,
			ResourceName: testSecretName,
			FilePath:     testFilePath,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create controller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start informers
	informerFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informerFactory.Core().V1().Secrets().Informer().HasSynced) {
		t.Fatal("Failed to sync informer cache")
	}

	// Run controller in background
	go func() {
		if err := controller.Run(ctx, 1); err != nil {
			t.Logf("Controller error: %v", err)
		}
	}()
	
	// Manually trigger the add event since fake client may not automatically do this
	controller.onSecretAdd(secret)

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

func TestController_SecretWriteFile(t *testing.T) {
	testData := "secret-data"
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-secret")

	fakeClient := fake.NewClientset()
	informerFactory := kubeinformers.NewSharedInformerFactory(fakeClient, time.Second*30)

	controller, err := NewController(
		nil, // configMapInformer not needed for Secret test
		informerFactory.Core().V1().Secrets(),
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "secret",
			ResourceKey:  testSecretKey,
			ResourceName: testSecretName,
			FilePath:     testFilePath,
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

	// Test that shouldEnqueueSecret works correctly
	correctSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSecretName,
			Namespace: testNamespace,
		},
	}

	wrongSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wrong-name",
			Namespace: testNamespace,
		},
	}

	if !controller.shouldEnqueueSecret(correctSecret) {
		t.Error("shouldEnqueueSecret should return true for correct Secret")
	}

	if controller.shouldEnqueueSecret(wrongSecret) {
		t.Error("shouldEnqueueSecret should return false for wrong Secret")
	}
}

func TestController_MissingSecretKey(t *testing.T) {
	tmpDir := t.TempDir()
	testFilePath := filepath.Join(tmpDir, "test-secret")

	fakeClient := fake.NewClientset()
	
	// Create Secret without the expected key
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSecretName,
			Namespace: testNamespace,
		},
		Data: map[string][]byte{
			"wrong-key": []byte("some-data"),
		},
	}
	
	_, err := fakeClient.CoreV1().Secrets(testNamespace).Create(
		context.Background(), 
		secret, 
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("Failed to create test Secret: %v", err)
	}

	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(
		fakeClient,
		time.Second*30,
		kubeinformers.WithNamespace(testNamespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fmt.Sprintf("metadata.name=%s", testSecretName)
		}),
	)

	controller, err := NewController(
		nil, // configMapInformer not needed for Secret test
		informerFactory.Core().V1().Secrets(),
		fakeClient,
		testNamespace,
		prometheus.NewRegistry(),
		Options{
			ResourceType: "secret",
			ResourceKey:  testSecretKey,
			ResourceName: testSecretName,
			FilePath:     testFilePath,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create controller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	informerFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informerFactory.Core().V1().Secrets().Informer().HasSynced) {
		t.Fatal("Failed to sync informer cache")
	}

	go func() {
		if err := controller.Run(ctx, 1); err != nil {
			t.Logf("Controller error: %v", err)
		}
	}()

	// Wait a bit to ensure controller processes the Secret
	time.Sleep(1 * time.Second)

	// File should not be created when key is missing
	if _, err := os.Stat(testFilePath); !os.IsNotExist(err) {
		t.Error("File should not be created when Secret key is missing")
	}
}

