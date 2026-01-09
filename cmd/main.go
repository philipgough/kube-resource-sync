package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	synccontroller "github.com/philipgough/kube-resource-sync/internal/pkg/sync"
)

// ResourceType represents the type of Kubernetes resource that can be synced
type ResourceType string

const (
	// ResourceTypeConfigMap represents a Kubernetes ConfigMap resource
	ResourceTypeConfigMap ResourceType = "configmap"
	// ResourceTypeSecret represents a Kubernetes Secret resource
	ResourceTypeSecret ResourceType = "secret"
)

const (
	defaultListen = ":8080"

	resyncPeriod = time.Minute

	// initModeTimeout is the maximum time to wait for the resource to be available
	initModeTimeout = 5 * time.Minute
	// initModeRetryDelay is the delay between retries when checking for the resource
	initModeRetryDelay = 5 * time.Second
)

var (
	masterURL  string
	kubeconfig string

	namespace    string
	resourceType ResourceType
	resourceName string
	resourceKey  string
	writePath    string

	listen   string
	initMode bool
)

// performInitialSync waits for the specified resource to exist and writes it to the target path
func performInitialSync(ctx context.Context, kubeClient kubernetes.Interface, namespace, resourceType, resourceName, resourceKey, writePath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, initModeTimeout)
	defer cancel()

	slog.Info("waiting for resource to be available",
		"namespace", namespace,
		"resourceType", resourceType,
		"resourceName", resourceName,
		"resourceKey", resourceKey,
		"timeout", initModeTimeout)

	ticker := time.NewTicker(initModeRetryDelay)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timeout waiting for resource %s/%s in namespace %s", resourceType, resourceName, namespace)
		case <-ticker.C:
			data, err := fetchResourceData(kubeClient, namespace, resourceType, resourceName, resourceKey)
			if err != nil {
				slog.Debug("resource not yet available, retrying", "error", err)
				continue
			}

			if len(data) == 0 {
				slog.Debug("resource exists but key not found, retrying", "resourceKey", resourceKey)
				continue
			}

			slog.Info("resource found, writing to file", "writePath", writePath, "dataSize", len(data))
			if err := os.WriteFile(writePath, data, 0644); err != nil {
				return fmt.Errorf("failed to write file: %w", err)
			}

			slog.Info("successfully wrote resource data to file", "writePath", writePath)
			return nil
		}
	}
}

// fetchResourceData retrieves the specified data from a ConfigMap or Secret
func fetchResourceData(kubeClient kubernetes.Interface, namespace, resourceType, resourceName, resourceKey string) ([]byte, error) {
	switch resourceType {
	case string(ResourceTypeConfigMap):
		cm, err := kubeClient.CoreV1().ConfigMaps(namespace).Get(context.Background(), resourceName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		data, exists := cm.Data[resourceKey]
		if !exists {
			return nil, fmt.Errorf("key %s not found in ConfigMap", resourceKey)
		}
		return []byte(data), nil

	case string(ResourceTypeSecret):
		secret, err := kubeClient.CoreV1().Secrets(namespace).Get(context.Background(), resourceName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		data, exists := secret.Data[resourceKey]
		if !exists {
			return nil, fmt.Errorf("key %s not found in Secret", resourceKey)
		}
		return data, nil

	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
}

func main() {
	flag.Parse()

	if writePath == "" {
		slog.Error("-write-path flag is required")
		os.Exit(1)
	}

	if resourceType == "" {
		slog.Error("resource-type flag is required")
		os.Exit(1)
	}

	if resourceType != ResourceTypeConfigMap && resourceType != ResourceTypeSecret {
		slog.Error("unsupported resource type, must be 'configmap' or 'secret'", "type", resourceType)
		os.Exit(1)
	}

	if resourceName == "" {
		slog.Error("resource-name flag is required")
		os.Exit(1)
	}

	ctx := setupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		slog.Error("error building kubeconfig", "error", err)
		os.Exit(1)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Error("error building kubernetes clientset", "error", err)
		os.Exit(1)
	}

	// Handle init mode - perform initial sync and exit
	if initMode {
		slog.Info("running in init mode - performing initial sync and exiting")
		if err := performInitialSync(ctx, kubeClient, namespace, string(resourceType), resourceName, resourceKey, writePath); err != nil {
			slog.Error("init mode failed", "error", err)
			os.Exit(1)
		}
		slog.Info("init mode completed successfully")
		os.Exit(0)
	}

	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(r, promhttp.HandlerOpts{}))

	informerFactory, err := createInformerFactory(kubeClient, namespace, resourceType, resourceName)
	if err != nil {
		slog.Error("failed to create informer factory", "error", err)
		os.Exit(1)
	}

	// Create controller based on resource type
	var controller *synccontroller.Controller

	switch resourceType {
	case ResourceTypeConfigMap:
		configMapInformer := informerFactory.Core().V1().ConfigMaps()
		controller, err = createController(configMapInformer, nil, kubeClient, namespace, r, string(resourceType), resourceName, resourceKey, writePath)
	case ResourceTypeSecret:
		secretInformer := informerFactory.Core().V1().Secrets()
		controller, err = createController(nil, secretInformer, kubeClient, namespace, r, string(resourceType), resourceName, resourceKey, writePath)
	default:
		slog.Error("unsupported resource type", "type", resourceType)
		os.Exit(1)
	}

	if err != nil {
		slog.Error("failed to create controller", "error", err)
		os.Exit(1)
	}

	slog.Info("starting kube-resource-sync controller", "namespace", namespace)

	server := &http.Server{
		Addr:    listen,
		Handler: mux,
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var wg sync.WaitGroup

	// Start HTTP server
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("starting HTTP server", "address", listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	// Start controller
	wg.Add(1)
	go func() {
		defer wg.Done()
		informerFactory.Start(ctx.Done())
		if controller != nil {
			if err := controller.Run(ctx, 1); err != nil {
				slog.Error("controller error", "error", err)
			}
		}
		slog.Info("stopping controller")
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	slog.Info("received shutdown signal, starting graceful shutdown")

	// Shutdown HTTP server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("error shutting down HTTP server", "error", err)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	slog.Info("controller stopped gracefully")
}

// createController creates the appropriate controller for resources
func createController(configMapInformer coreinformers.ConfigMapInformer, secretInformer coreinformers.SecretInformer, kubeClient kubernetes.Interface, namespace string, registry prometheus.Registerer, resourceType, resourceName, resourceKey, writePath string) (*synccontroller.Controller, error) {
	opts := synccontroller.Options{
		ResourceType: resourceType,
		ResourceKey:  resourceKey,
		ResourceName: resourceName,
		FilePath:     writePath,
	}

	return synccontroller.NewController(
		configMapInformer,
		secretInformer,
		kubeClient,
		namespace,
		registry,
		opts,
	)
}

// createInformerFactory creates and returns an appropriate informer factory based on resource type
func createInformerFactory(kubeClient kubernetes.Interface, namespace string, resourceType ResourceType, resourceName string) (kubeinformers.SharedInformerFactory, error) {
	// Validate resource type
	if resourceType != ResourceTypeConfigMap && resourceType != ResourceTypeSecret {
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	return kubeinformers.NewSharedInformerFactoryWithOptions(
		kubeClient,
		resyncPeriod,
		kubeinformers.WithNamespace(namespace),
		kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.FieldSelector = fmt.Sprintf("metadata.name=%s", resourceName)
		}),
	), nil
}

// setupSignalHandler creates a context that is cancelled when SIGTERM or SIGINT is received
func setupSignalHandler() context.Context {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		cancel()
	}()

	return ctx
}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file. Only required if running out-of-cluster")
	flag.StringVar(&masterURL, "master", "", "Kubernetes API server address. Overrides kubeconfig. Only required if running out-of-cluster")
	flag.StringVar(&listen, "listen", defaultListen, "HTTP server listen address for metrics and health checks")

	flag.StringVar(&namespace, "namespace", metav1.NamespaceDefault, "Kubernetes namespace to watch for resources")
	flag.Func("resource-type", "Type of Kubernetes resource to sync (configmap or secret)", func(s string) error {
		resourceType = ResourceType(s)
		return nil
	})
	flag.StringVar(&resourceName, "resource-name", "", "Name of the Kubernetes resource to watch and sync")
	flag.StringVar(&resourceKey, "resource-key", "", "Specific key within the resource to sync. If empty, syncs all keys")
	flag.StringVar(&writePath, "write-path", "", "Filesystem path where synced resource data will be written (can be mounted ConfigMap/Secret volume)")
	flag.BoolVar(&initMode, "init-mode", false, "Run as init container: perform initial sync and exit. Does not start HTTP server or continuous watching.")

}
