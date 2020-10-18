package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/hetznercloud/hcloud-go/hcloud"
	v1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

type genericError string

func (e genericError) Error() string {
	return string(e)
}

const errGeneric genericError = "generic error"

const (
	hcloudTokenEnv = "HCLOUD_TOKEN"
	kubeconfigEnv  = "KUBECONFIG"
	nodeSuffixEnv  = "NODE_SUFFIX"

	metalLBEventFieldSelector = "reason=nodeAssigned"
	metalLBEventMessageRegexp = `^announcing from node "(\S+)"$`
)

func kubeconfigPath() (string, error) {
	if p := os.Getenv(kubeconfigEnv); p != "" {
		return p, nil
	}

	if home := homedir.HomeDir(); home != "" {
		return filepath.Join(home, ".kube", "config"), nil
	}

	return "", fmt.Errorf("unable to select kubeconfig file to use: %w", errGeneric)
}

func kubeconfig() (*rest.Config, error) {
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, fmt.Errorf("getting in cluster config: %w", err)
	}

	kubeconfigPath, err := kubeconfigPath()
	if err != nil {
		return nil, fmt.Errorf("selecting kubeconfig: %w", err)
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}

//nolint:funlen,gocognit,gocyclo
func run() error {
	config, err := kubeconfig()
	if err != nil {
		return fmt.Errorf("getting kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating clientset: %w", err)
	}

	log.Printf("Getting events with selector %q from all namespaces...", metalLBEventFieldSelector)

	events, err := clientset.EventsV1().Events("").List(context.TODO(), metav1.ListOptions{
		FieldSelector: metalLBEventFieldSelector,
	})
	if err != nil {
		return fmt.Errorf("listing events: %w", err)
	}

	if len(events.Items) == 0 {
		return fmt.Errorf("no events found. Is MetalLB functional: %w", errGeneric)
	}

	// Group events per service/namespace.
	eventsByServiceNamespace := map[string]map[string][]v1.Event{}

	for _, event := range events.Items {
		namespaceName := event.Regarding.Namespace
		serviceName := event.Regarding.Name

		eventsByService, ok := eventsByServiceNamespace[namespaceName]
		if !ok {
			eventsByService = map[string][]v1.Event{}
		}

		eventsByService[serviceName] = append(eventsByService[serviceName], event)
		eventsByServiceNamespace[namespaceName] = eventsByService
	}

	// Print debug information.
	for namespaceName, eventsByService := range eventsByServiceNamespace {
		for serviceName, events := range eventsByService {
			log.Printf("Found %d events for service %q in namespace %q \n", len(events), serviceName, namespaceName)
		}
	}

	// Pick only latest event from each service/namespace.
	eventByServiceNamespace := map[string]map[string]v1.Event{}

	for namespaceName, eventsByService := range eventsByServiceNamespace {
		if _, ok := eventByServiceNamespace[namespaceName]; !ok {
			eventByServiceNamespace[namespaceName] = map[string]v1.Event{}
		}

		for serviceName, events := range eventsByService {
			for _, event := range events {
				latestEvent, ok := eventByServiceNamespace[namespaceName][serviceName]
				if !ok {
					eventByServiceNamespace[namespaceName][serviceName] = event

					continue
				}

				latestEventTime := latestEvent.CreationTimestamp
				if latestEventTime.Before(&event.CreationTimestamp) {
					eventByServiceNamespace[namespaceName][serviceName] = event
				}
			}
		}
	}

	re := regexp.MustCompile(metalLBEventMessageRegexp)

	// Build a list of IP addresses to be assigned for specific nodes.
	nodeAnnouncingByIP := map[string]string{}

	for namespaceName, eventsByService := range eventByServiceNamespace {
		for serviceName, event := range eventsByService {
			rs := re.FindStringSubmatch(event.Note)
			nodeName := rs[1]

			service, err := clientset.CoreV1().Services(namespaceName).Get(context.TODO(), serviceName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf(`getting service "%s/%s": %w`, serviceName, namespaceName, err)
			}

			for _, lbIngress := range service.Status.LoadBalancer.Ingress {
				log.Printf("Service %s in namespace %q has IP %q and is announced by node %q",
					serviceName, namespaceName, lbIngress.IP, nodeName)

				nodeAnnouncingByIP[lbIngress.IP] = nodeName
			}
		}
	}

	client := hcloud.NewClient(hcloud.WithToken(os.Getenv(hcloudTokenEnv)))

	// Build list of server objects to use for assignment.
	serverAnnouncingByIP := map[string]*hcloud.Server{}

	for ip, nodeName := range nodeAnnouncingByIP {
		serverName := nodeName + os.Getenv(nodeSuffixEnv)
		log.Printf("Node %q becomes server %q", nodeName, serverName)

		server, _, err := client.Server.Get(context.TODO(), serverName)
		if err != nil {
			return fmt.Errorf("getting server %q: %w", serverName, err)
		}

		serverAnnouncingByIP[ip] = server
	}

	// Build list of floating IPs to be assigned.
	floatingIPByIP := map[string]*hcloud.FloatingIP{}

	fips, _, err := client.FloatingIP.List(context.TODO(), hcloud.FloatingIPListOpts{})
	if err != nil {
		return fmt.Errorf("listing floating IPs: %w", err)
	}

	for _, fip := range fips {
		floatingIPByIP[fip.IP.String()] = fip
	}

	for ip, fip := range floatingIPByIP {
		server := serverAnnouncingByIP[ip]
		if server == nil {
			return fmt.Errorf("server for IP %q not found: %w", ip, errGeneric)
		}

		if fip.Server == nil {
			log.Printf("Floating IP %q (%s) has no server assigned", fip.IP, fip.Name)
		}

		if fip.Server != nil && fip.Server.ID != server.ID {
			log.Printf("Floating IP %q (%s) is assigned to server %d, "+
				"should be assigned to %d", fip.IP, fip.Name, fip.Server.ID, server.ID)
		}

		if fip.Server == nil || fip.Server.ID != server.ID {
			log.Printf("Assigning floating IP %q (%s) to server %q\n", fip.IP, fip.Name, server.Name)

			if _, _, err := client.FloatingIP.Assign(context.TODO(), fip, server); err != nil {
				return fmt.Errorf("assigning floating IP %q (%s) to server %q: %w", fip.Name, fip.IP, server.Name, err)
			}
		}
	}

	return nil
}
