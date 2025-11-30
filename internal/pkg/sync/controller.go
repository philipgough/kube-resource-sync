package sync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	resourceTypeConfigMap = "configmap"
	resourceTypeSecret    = "secret"
)

// Controller handles syncing Kubernetes resources to disk
type Controller struct {
	// client is the kubernetes client
	client clientset.Interface

	// configMapLister is able to list/get configmaps and is populated by the
	// shared informer passed to NewController (only used when resourceType is configmap)
	configMapLister corelisters.ConfigMapLister
	// configMapSynced returns true if the configmaps shared informer has been synced at least once.
	// Added as a member to the struct to allow injection for testing.
	configMapSynced cache.InformerSynced

	// secretLister is able to list/get secrets and is populated by the
	// shared informer passed to NewController (only used when resourceType is secret)
	secretLister corelisters.SecretLister
	// secretSynced returns true if the secrets shared informer has been synced at least once.
	secretSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]

	// workerLoopPeriod is the time between worker runs. The workers
	// process the queue of service and pod changes
	workerLoopPeriod time.Duration

	// resourceType is the type of resource being synced (configmap or secret)
	resourceType string
	// resourceName is the name of the resource that the controller will sync
	resourceName string
	// namespace is the namespace of the resource that the controller will sync
	namespace string

	metrics *metrics

	path string
	key  string
}

// Options contains configuration for the Controller
type Options struct {
	// ResourceType is the type of Kubernetes resource (configmap or secret)
	ResourceType string
	// ResourceKey is the key within the resource data
	ResourceKey string
	// ResourceName is the name of the Kubernetes resource
	ResourceName string
	// FilePath is the path to which the data gets written
	FilePath string
}

// NewController creates a new Controller instance
func NewController(
	configMapInformer coreinformers.ConfigMapInformer,
	secretInformer coreinformers.SecretInformer,
	client clientset.Interface,
	namespace string,
	registry prometheus.Registerer,
	opts Options,
) (*Controller, error) {

	ctrlMetrics := newMetrics()
	if registry != nil {
		ctrlMetrics.register(registry)
	}

	if err := opts.valid(); err != nil {
		return nil, err
	}

	c := &Controller{
		client:       client,
		resourceType: opts.ResourceType,
		resourceName: opts.ResourceName,
		path:         opts.FilePath,
		key:          opts.ResourceKey,
		// This is similar to the DefaultControllerRateLimiter, just with a
		// significantly higher default backoff (1s vs 5ms). A more significant
		// rate limit back off here helps ensure that the Controller does not
		// overwhelm the API Server.
		queue:            workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		workerLoopPeriod: time.Second,
		namespace:        namespace,
		metrics:          ctrlMetrics,
	}

	switch opts.ResourceType {
	case resourceTypeConfigMap:
		if configMapInformer == nil {
			return nil, fmt.Errorf("configMapInformer cannot be nil for configmap resource type")
		}
		_, err := configMapInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    c.onConfigMapAdd,
			UpdateFunc: c.onConfigMapUpdate,
			DeleteFunc: c.onConfigMapDelete,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to add ConfigMap event handler: %w", err)
		}
		c.configMapLister = configMapInformer.Lister()
		c.configMapSynced = configMapInformer.Informer().HasSynced
	case resourceTypeSecret:
		if secretInformer == nil {
			return nil, fmt.Errorf("secretInformer cannot be nil for secret resource type")
		}
		_, err := secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    c.onSecretAdd,
			UpdateFunc: c.onSecretUpdate,
			DeleteFunc: c.onSecretDelete,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to add Secret event handler: %w", err)
		}
		c.secretLister = secretInformer.Lister()
		c.secretSynced = secretInformer.Informer().HasSynced
	default:
		return nil, fmt.Errorf("unsupported resource type: %s", opts.ResourceType)
	}

	return c, nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the queue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(ctx context.Context, workers int) error {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	slog.Info("starting resource sync controller")
	slog.Info("waiting for informer caches to sync")

	var cacheSyncFunc cache.InformerSynced
	switch c.resourceType {
	case resourceTypeConfigMap:
		cacheSyncFunc = c.configMapSynced
	case resourceTypeSecret:
		cacheSyncFunc = c.secretSynced
	default:
		return fmt.Errorf("unsupported resource type: %s", c.resourceType)
	}

	if ok := cache.WaitForCacheSync(ctx.Done(), cacheSyncFunc); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	slog.Info("starting workers", "count", workers)
	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	slog.Info("started workers")
	<-ctx.Done()
	slog.Info("shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the queue.
func (c *Controller) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// enqueueResource takes a Kubernetes resource
// It converts it into a namespace/name string which is then put onto the queue.
func (c *Controller) enqueueResource(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

// processNextWorkItem will read a single work item off the queue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, shutdown := c.queue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.queue.Done.
	err := func(key string) error {
		// We call Done here so the queue knows we have finished processing this item.
		// We also must remember to call Forget if we do not want this work item being re-queued.
		// For example, we do not call Forget if a transient error occurs, instead the item is
		// put back on the queue and attempted again after a back-off period.
		defer c.queue.Done(key)

		// Run the syncHandler, passing it the namespace/name string of the ConfigMap resource to be synced.
		if err := c.syncHandler(ctx, key); err != nil {
			// Put the item back on the queue to handle any transient errors.
			c.queue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.queue.Forget(key)
		slog.Debug("successfully synced", "resourceName", key)
		return nil
	}(key)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// syncHandler compares the actual state with the desired, and attempts to converge the two.
func (c *Controller) syncHandler(_ context.Context, key string) error {
	timer := c.metrics.startEventTimer(c.resourceType, c.namespace, c.resourceName)
	defer timer.ObserveDuration()
	
	slog.Debug("syncHandler called", "resourceName", key, "resourceType", c.resourceType)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	var data []byte
	var found bool

	switch c.resourceType {
	case resourceTypeConfigMap:
		cm, err := c.configMapLister.ConfigMaps(namespace).Get(name)
		if err != nil {
			if errors.IsNotFound(err) {
				utilruntime.HandleError(fmt.Errorf("ConfigMap '%s' in work queue no longer exists", key))
				return nil
			}
			return err
		}
		dataStr, ok := cm.Data[c.key]
		if !ok {
			slog.Warn("no data found for key in ConfigMap", "key", c.key)
			return nil
		}
		data = []byte(dataStr)
		found = true

	case resourceTypeSecret:
		secret, err := c.secretLister.Secrets(namespace).Get(name)
		if err != nil {
			if errors.IsNotFound(err) {
				utilruntime.HandleError(fmt.Errorf("Secret '%s' in work queue no longer exists", key))
				return nil
			}
			return err
		}
		secretData, ok := secret.Data[c.key]
		if !ok {
			slog.Warn("no data found for key in Secret", "key", c.key)
			return nil
		}
		data = secretData
		found = true

	default:
		return fmt.Errorf("unsupported resource type: %s", c.resourceType)
	}

	if !found {
		slog.Warn("no data found for key", "key", c.key, "resourceType", c.resourceType)
		return nil
	}

	if err := os.WriteFile(c.path, data, 0644); err != nil {
		slog.Error("failed to write file", "error", err, "resourceType", c.resourceType)
		return err
	}
	c.metrics.setLastWriteSuccessTime(c.resourceType, c.namespace, c.resourceName, c.key, float64(time.Now().Unix()))
	c.metrics.setResourceDataHash(c.resourceType, c.namespace, c.resourceName, c.key, hashAsMetricValue(data))
	return nil
}

func (c *Controller) onConfigMapAdd(obj interface{}) {
	slog.Info("ConfigMap add event received")
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		slog.Error("unexpected object type", "type", fmt.Sprintf("%T", obj))
		return
	}

	slog.Info("ConfigMap add event", "name", cm.Name, "namespace", cm.Namespace)
	c.metrics.recordEventReceived(c.resourceType, cm.Namespace, cm.Name, "add")
	
	if !c.shouldEnqueueConfigMap(cm) {
		slog.Info("ConfigMap filtered out", "name", cm.Name, "namespace", cm.Namespace, "expectedName", c.resourceName, "expectedNamespace", c.namespace)
		return
	}
	slog.Info("Enqueueing ConfigMap", "name", cm.Name, "namespace", cm.Namespace)
	c.enqueueResource(cm)
}

func (c *Controller) onConfigMapUpdate(oldObj, newObj interface{}) {
	slog.Info("ConfigMap update event received")
	newCM, ok := newObj.(*corev1.ConfigMap)
	if !ok {
		slog.Error("unexpected object type in update", "type", fmt.Sprintf("%T", newObj))
		return
	}
	oldCM, ok := oldObj.(*corev1.ConfigMap)
	if !ok {
		slog.Error("unexpected object type in update", "type", fmt.Sprintf("%T", oldObj))
		return
	}

	slog.Info("ConfigMap update event", "name", newCM.Name, "namespace", newCM.Namespace, "oldRV", oldCM.ResourceVersion, "newRV", newCM.ResourceVersion)
	c.metrics.recordEventReceived(c.resourceType, newCM.Namespace, newCM.Name, "update")

	if !c.shouldEnqueueConfigMap(newCM) {
		slog.Info("ConfigMap update filtered out", "name", newCM.Name, "namespace", newCM.Namespace)
		return
	}

	if newCM.ResourceVersion == oldCM.ResourceVersion {
		slog.Info("ConfigMap update skipped - same resource version", "name", newCM.Name, "rv", newCM.ResourceVersion)
		// Periodic resync will send update events for all known ConfigMap.
		// Two different versions will always have different RVs.
		return
	}

	slog.Info("Enqueueing ConfigMap update", "name", newCM.Name, "namespace", newCM.Namespace, "newRV", newCM.ResourceVersion)
	c.enqueueResource(newCM)
}

func (c *Controller) onConfigMapDelete(obj interface{}) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		slog.Error("unexpected object type", "type", fmt.Sprintf("%T", obj))
		return
	}
	slog.Info("ConfigMap deleted - ignoring", "name", cm.Name, "namespace", cm.Namespace)
}

func (c *Controller) onSecretAdd(obj interface{}) {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		slog.Error("unexpected object type", "type", fmt.Sprintf("%T", obj))
		return
	}

	if !c.shouldEnqueueSecret(secret) {
		return
	}
	c.enqueueResource(secret)
}

func (c *Controller) onSecretUpdate(oldObj, newObj interface{}) {
	newSecret, ok := newObj.(*corev1.Secret)
	if !ok {
		slog.Error("unexpected object type in update", "type", fmt.Sprintf("%T", newObj))
		return
	}
	oldSecret, ok := oldObj.(*corev1.Secret)
	if !ok {
		slog.Error("unexpected object type in update", "type", fmt.Sprintf("%T", oldObj))
		return
	}

	if !c.shouldEnqueueSecret(newSecret) {
		return
	}

	if newSecret.ResourceVersion == oldSecret.ResourceVersion {
		// Periodic resync will send update events for all known Secret.
		// Two different versions will always have different RVs.
		return
	}

	c.enqueueResource(newSecret)
}

func (c *Controller) onSecretDelete(obj interface{}) {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		slog.Error("unexpected object type", "type", fmt.Sprintf("%T", obj))
		return
	}
	slog.Info("Secret deleted - ignoring", "name", secret.Name, "namespace", secret.Namespace)
}

func (c *Controller) shouldEnqueueConfigMap(cm *corev1.ConfigMap) bool {
	// Only process the specific ConfigMap we're watching
	return cm.Name == c.resourceName && cm.Namespace == c.namespace
}

func (c *Controller) shouldEnqueueSecret(secret *corev1.Secret) bool {
	// Only process the specific Secret we're watching
	return secret.Name == c.resourceName && secret.Namespace == c.namespace
}

func (o Options) valid() error {
	if o.FilePath == "" {
		return fmt.Errorf("filepath cannot be empty")
	}
	if o.ResourceName == "" {
		return fmt.Errorf("resource name cannot be empty")
	}
	if o.ResourceKey == "" {
		return fmt.Errorf("resource key cannot be empty")
	}
	if o.ResourceType == "" {
		return fmt.Errorf("resource type cannot be empty")
	}
	if o.ResourceType != resourceTypeConfigMap && o.ResourceType != resourceTypeSecret {
		return fmt.Errorf("resource type must be 'configmap' or 'secret'")
	}
	return nil
}
