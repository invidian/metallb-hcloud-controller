// Package controller implements logic for following MetalLB announce events
// and passing them to configured platform-specific Floating IP assigners.
package controller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

const (
	// In case we somehow miss new announce event, perform full
	// re-sync periodically. This can be fine-tuned if one cannot
	// accept 30 seconds downtime.
	resyncInterval = 30 * time.Second

	// Maximum number of pending events to be processed within
	// wait duration period. If exceeded, watcher hook and periodic
	// resync loop will block until buffer is drained.
	eventsBufferSize = 1024

	// To avoid hitting cloud-provider API on every announce event, batch
	// them by buffering the events and wait for new ones incoming. If MetalLB
	// publishes all announce events for a single node within 5 seconds, only one
	// assignment request will be performed.
	eventsBufferWaitDuration = 5 * time.Second

	// Default klog level where debug logs will be sent.
	klogLevelDebug = 10

	// Field selector selecting MetalLB emitted events.
	metalLBEventFieldSelector = "reason=nodeAssigned"

	// Regular expression for extracting node name from MetalLB events.
	metalLBEventMessageRegexp = `^announcing from node "(\S+)"$`
)

// Assigner describes an interface which must be implemented by cloud platform specific
// implementation to assign Floating IP addresses to cluster nodes.
type Assigner interface {
	EnsureAssigned(ctx context.Context, nodeAnnouncingByIP map[string]string) error
}

// MetalLBHCloudControllerConfig is used to configure and run MetalLB Hetzner Cloud
// controller.
type MetalLBHCloudControllerConfig struct {
	// Path to kubeconfig file to use. If empty, either in-cluster kubeconfig file will
	// be used or will fallback to use ~/.kube/config file.
	KubeconfigPath string

	// Logger implementation to use. If omitted, Klogger will be used.
	Logger Logger

	// Map of assigner implementations, where alternative assigner implementations can
	// be added.
	Assigners map[string]Assigner

	// Channel on which controller will look for shutdown signal. If nil, channel will be
	// initialized and it is up to the caller to close it.
	StopCh <-chan struct{}
}

// New Validates controller configuration and starts the controller.
//
//nolint:golint
func (cc MetalLBHCloudControllerConfig) New() (*metalLBHCloudController, error) {
	c, err := cc.buildDependencies()
	if err != nil {
		return nil, fmt.Errorf("building dependencies: %w", err)
	}

	c.logger = cc.Logger
	c.StopCh = cc.StopCh
	c.config = cc

	c.setDefaults()

	c.logAssigners()

	c.configureInformers()

	// When stop signal is received, close events buffer channel to exit the sync loop.
	go func() {
		<-c.StopCh
		close(c.eventsBuffer)
	}()

	go c.resyncLoop()

	go c.syncLoop()

	return c, nil
}

// metalLBHCloudController contains actual implementation of the controller.
type metalLBHCloudController struct {
	clientset     *kubernetes.Clientset
	logger        Logger
	config        MetalLBHCloudControllerConfig
	StopCh        <-chan struct{}
	ShutdownCh    chan struct{}
	eventsLister  listercorev1.EventLister
	serviceLister listercorev1.ServiceLister
	eventsBuffer  chan struct{}
}

// buildDependencies builds sub-objects which controller depends on and returns partially
// initialized controller with those objects set.
func (cc MetalLBHCloudControllerConfig) buildDependencies() (*metalLBHCloudController, error) {
	config, err := kubeconfig(cc.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("getting kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}

	return &metalLBHCloudController{ //nolint:exhaustivestruct
		clientset: clientset,
	}, nil
}

// kubeconfig builds REST config from given kubeconfig path.
//
// If in-cluster configuration is detected, given path is ignored.
func kubeconfig(path string) (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, fmt.Errorf("getting in cluster config: %w", err)
	}

	kubeconfigPath, err := kubeconfigPath(path)
	if err != nil {
		return nil, fmt.Errorf("selecting kubeconfig: %w", err)
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}

// kubeconfigPath selects kubeconfig path to use. If empty path is given,
// ~/.kube/config is returned.
//
// If user's home directory cannot be expanded, error is returned.
func kubeconfigPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}

	if home := homedir.HomeDir(); home != "" {
		return filepath.Join(home, ".kube", "config"), nil
	}

	return "", fmt.Errorf("unable to select kubeconfig file to use: %w", errGeneric)
}

// setSefaults initializes controller fields.
func (c *metalLBHCloudController) setDefaults() {
	c.ShutdownCh = make(chan struct{})

	c.eventsBuffer = make(chan struct{}, eventsBufferSize)

	if c.logger == nil {
		c.logger = Klogger{}
	}

	if c.StopCh == nil {
		c.StopCh = make(chan struct{})
	}
}

// logAssigners checks what assigners has been configured and prints information about them
// to the logger, so user is aware, that either none of them are configured or which one are
// enabled.
func (c *metalLBHCloudController) logAssigners() {
	if len(c.config.Assigners) == 0 {
		c.logger.Errorf("No assigners configured, no IP addresses will be actually assigned")
	}

	for name := range c.config.Assigners {
		c.logger.Infof("Registered %q assigner", name)
	}
}

// configureInformers creates and starts Kubernetes informers, which are used to be able to efficiently
// watch, list and get Kubernetes resources which this controller operates on.
func (c *metalLBHCloudController) configureInformers() {
	// Service informer.
	serviceInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(c.clientset, resyncInterval)

	c.serviceLister = serviceInformerFactory.Core().V1().Services().Lister()

	serviceInformerFactory.Start(c.StopCh)

	serviceInformerFactory.WaitForCacheSync(c.StopCh)

	// Events informer.
	eventsInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(c.clientset, resyncInterval,
		kubeinformers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = metalLBEventFieldSelector
		}),
	)

	c.eventsLister = eventsInformerFactory.Core().V1().Events().Lister()

	eventsInformer := eventsInformerFactory.Core().V1().Events().Informer()

	eventsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{ //nolint:exhaustivestruct
		AddFunc: func(obj interface{}) {
			c.logger.Infof("New node announcement event received")
			c.eventsBuffer <- struct{}{}
		},
	})

	eventsInformerFactory.Start(c.StopCh)

	eventsInformerFactory.WaitForCacheSync(c.StopCh)
}

// resyncLoop is a sub-procedure, intended to run in separate goroutine together with the controller,
// which periodically sends a signal to events channel, so even when no new events are generated
// by MetalLB, we still peridically trigger the assigners to verify with cloud providers that all
// assignments are correct.
func (c metalLBHCloudController) resyncLoop() {
	for {
		time.Sleep(resyncInterval)

		select {
		case <-c.StopCh:
			c.logger.Infof("Shutting down periodic resync trigger")

			return
		default:
		}

		c.logger.Infof("Forcing periodic resync")

		c.eventsBuffer <- struct{}{}
	}
}

// syncLoop is a main controller loop, which watches for new events, buffers them to avoid
// stressing assigners with too many cloud API requests and triggers main sync function.
func (c metalLBHCloudController) syncLoop() {
	for range c.eventsBuffer {
		c.logger.Infof("New event received, waiting for more events...")
		time.Sleep(eventsBufferWaitDuration)

		if l := len(c.eventsBuffer); l > 0 {
			c.logger.Infof("Received %d events while waiting, draining...", l)

			for range c.eventsBuffer {
				if len(c.eventsBuffer) == 0 {
					break
				}
			}

			c.logger.Infof("Events channel drained")
		}

		if err := c.syncOnce(); err != nil {
			c.logger.Errorf("Error occured while syncing: %v", err)
		}
	}

	c.logger.Infof("events buffer channel closed, shutting down gracefully")

	c.ShutdownCh <- struct{}{}
}

// syncOnce builds information about MetalLB IP assignments and sends them to
// assigners to reflect those assignments in cloud API.
//
// To avoid sending many cloud API requests, assignments are deduplicated and only
// unique IP-Node pairs are send to assigners.
func (c metalLBHCloudController) syncOnce() error {
	c.logger.Infof("Syncing Floating IP assignments based on MetalLB events.")

	events, err := c.eventsLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("getting events: %w", err)
	}

	if len(events) == 0 {
		c.logger.Errorf("No events received. "+
			"Is MetalLB publishing events matching field selector %q?", metalLBEventFieldSelector)

		return nil
	}

	serviceEvents := groupEventsByService(events)

	// Print debug information.
	for _, service := range serviceEvents {
		c.logger.Infof("Found %d events for service %q in namespace %q \n",
			len(service.events),
			service.name,
			service.namespace)
	}

	// Build a list of IP addresses to be assigned for specific nodes.
	//
	// One node can have multiple IP addresses assigned, but one IP address
	// can only be attached to a single node, hence map with IP address
	// as a key.
	nodeAnnouncingByIP, err := c.nodeAnnouncingByIP(serviceEvents)
	if err != nil {
		return fmt.Errorf("building list of IPs with announcing nodes: %w", err)
	}

	for ip, node := range nodeAnnouncingByIP {
		c.logger.Infof("IP address %q announced by node %q", ip, node)
	}

	for name, assigner := range c.config.Assigners {
		if err := assigner.EnsureAssigned(context.TODO(), nodeAnnouncingByIP); err != nil {
			return fmt.Errorf("assigning IPs using %q assigner: %w", name, err)
		}
	}

	return nil
}

// serviceEvents is helper for grouping events by relevant Service object.
type serviceEvents struct {
	name      string
	namespace string
	events    []*v1.Event
}

// groupEventsByService takes Kubernetes Event objects as an argument and groups them
// by Service object, so for example last Event for each Service can be selected, which
// is useful for selecting the right node assignment published by MetalLB to send to
// cloud provider API.
func groupEventsByService(events []*v1.Event) []serviceEvents {
	eventsByServiceNamespace := map[string]map[string][]*v1.Event{}

	for _, event := range events {
		namespaceName := event.InvolvedObject.Namespace
		serviceName := event.InvolvedObject.Name

		eventsByService, ok := eventsByServiceNamespace[namespaceName]
		if !ok {
			eventsByService = map[string][]*v1.Event{}
		}

		eventsByService[serviceName] = append(eventsByService[serviceName], event)
		eventsByServiceNamespace[namespaceName] = eventsByService
	}

	groups := []serviceEvents{}

	for namespaceName, eventsByService := range eventsByServiceNamespace {
		for serviceName, events := range eventsByService {
			groups = append(groups, serviceEvents{
				name:      serviceName,
				namespace: namespaceName,
				events:    events,
			})
		}
	}

	return groups
}

// lastEvent sorts events associated with a given service and returns newest one.
//
// serviceEvents must have at least one event populated.
func (se serviceEvents) lastEvent() *v1.Event {
	// Sort events descending by creation timestamp, from newest to oldest.
	sort.Slice(se.events, func(i, j int) bool {
		nextEventTime := se.events[j].LastTimestamp

		return nextEventTime.Before(&se.events[i].LastTimestamp)
	})

	return se.events[0]
}

// ips returns external IP addresses associated with a given Service.
//
// If service no longer exists, it means that published event mentions a service, which
// no longer exists, so it shouldn't be synced with cloud provider, so an error is returned.
//
// If service has no external IP addresses configured, MetalLB should not be announcing it,
// so an error is returned.
func (se serviceEvents) ips(svcLister listercorev1.ServiceLister) ([]string, error) {
	services, err := svcLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}

	var svc *v1.Service

	for _, service := range services {
		if service.Name == se.name && service.Namespace == se.namespace {
			svc = service

			break
		}
	}

	if svc == nil {
		return nil, fmt.Errorf("couldn't find service %q in namespace %q: %w", se.name, se.namespace, errGeneric)
	}

	ips := []string{}

	for _, lbIngress := range svc.Status.LoadBalancer.Ingress {
		ips = append(ips, lbIngress.IP)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPs found: %w", errGeneric)
	}

	return ips, nil
}

// currentNode extracts Kubernetes Node name from last Event message.
//
// Returned node name should have Floating IPs assigned returned by ips().
func (se serviceEvents) currentNode() (string, error) {
	event := se.lastEvent()

	re := regexp.MustCompile(metalLBEventMessageRegexp)

	rs := re.FindStringSubmatch(event.Message)

	if expectedMatches := 2; len(rs) != expectedMatches {
		return "", fmt.Errorf("%q did not match %q, got: %v: %w", event.Message, metalLBEventMessageRegexp, rs, errGeneric)
	}

	return rs[1], nil
}

// nodeAnnouncingByIP builds a map of IP address with Kubernetes Node name, which should have Floating IPs
// assigned. This map should later be passed to each configured assigner, so it is reflected in cloud provider.
func (c metalLBHCloudController) nodeAnnouncingByIP(services []serviceEvents) (map[string]string, error) {
	nodeAnnouncingByIP := map[string]string{}

	for _, service := range services {
		nodeName, err := service.currentNode()
		if err != nil {
			return nil, fmt.Errorf("getting current node name from service %q in namespace %q: %w",
				service.name,
				service.namespace,
				err)
		}

		ips, err := service.ips(c.serviceLister)
		if err != nil {
			return nil, fmt.Errorf(`getting service "%s/%s" IPs: %w`, service.name, service.namespace, err)
		}

		for _, ip := range ips {
			nodeAnnouncingByIP[ip] = nodeName
		}
	}

	return nodeAnnouncingByIP, nil
}

type genericError string

func (e genericError) Error() string {
	return string(e)
}

const errGeneric genericError = "generic error"
