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
)

var (
	masterURL  string
	kubeconfig string

	namespace    string
	resourceType ResourceType
	resourceName string
	resourceKey  string
	pathToWrite  string

	listen string
)

func main() {
	flag.Parse()

	if pathToWrite == "" {
		slog.Error("path flag is required")
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
		controller, err = createController(configMapInformer, nil, kubeClient, namespace, r, string(resourceType), resourceName, resourceKey, pathToWrite)
	case ResourceTypeSecret:
		secretInformer := informerFactory.Core().V1().Secrets()
		controller, err = createController(nil, secretInformer, kubeClient, namespace, r, string(resourceType), resourceName, resourceKey, pathToWrite)
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
func createController(configMapInformer coreinformers.ConfigMapInformer, secretInformer coreinformers.SecretInformer, kubeClient kubernetes.Interface, namespace string, registry prometheus.Registerer, resourceType, resourceName, resourceKey, pathToWrite string) (*synccontroller.Controller, error) {
	opts := synccontroller.Options{
		ResourceType: resourceType,
		ResourceKey:  resourceKey,
		ResourceName: resourceName,
		FilePath:     pathToWrite,
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
	switch resourceType {
	case ResourceTypeConfigMap:
		return kubeinformers.NewSharedInformerFactoryWithOptions(
			kubeClient,
			resyncPeriod,
			kubeinformers.WithNamespace(namespace),
			kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.FieldSelector = fmt.Sprintf("metadata.name=%s", resourceName)
			}),
		), nil
	case ResourceTypeSecret:
		return kubeinformers.NewSharedInformerFactoryWithOptions(
			kubeClient,
			resyncPeriod,
			kubeinformers.WithNamespace(namespace),
			kubeinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
				options.FieldSelector = fmt.Sprintf("metadata.name=%s", resourceName)
			}),
		), nil
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", resourceType)
	}
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
	flag.StringVar(&pathToWrite, "path", "", "Local filesystem path where synced resource data will be written")

}
