// Package hcloud implements controller.Assiger interface for Hetzner Cloud.
package hcloud

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hetznercloud/hcloud-go/hcloud"
	"github.com/invidian/metallb-hcloud-controller/pkg/controller"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// AssignerConfig represents Hetzner Cloud Floating IP Assigner configuration, which
// can be used with this controller.
type AssignerConfig struct {
	// AuthToken is a Hetzner Cloud read-write token and must be set to non-empty value.
	AuthToken string

	// When mapping Kubernetes Node objects to Hetzner Cloud Servers, this suffix will be added
	// to the Kubernetes Node object name before mapping.
	//
	// E.g. if your Kubernetes node has name "foo" and your Hetzner Cloud server has name "foo.example.com",
	// you should set this to ".example.com".
	NodeSuffix string

	// Optional Prometheus Registerer to register Hetzner Cloud client metrics.
	PrometheusRegistrer prometheus.Registerer

	// Optional logger to use. If nil, controller.Klogger will be used.
	Logger controller.Logger

	// If true, no assignment changes will be actually performed. They will only be logged.
	DryRun bool
}

// New validates AssignerConfig and returns assigner implementing controller.Assigner interface.
func (c AssignerConfig) New() (controller.Assigner, error) {
	client, err := c.newHcloudClient()
	if err != nil {
		return nil, fmt.Errorf("initializing Hetzner Cloud client: %w", err)
	}

	ha := &hcloudAssigner{
		config: c,
		client: client,
		logger: c.Logger,
	}

	if ha.logger == nil {
		ha.logger = controller.Klogger{}
	}

	return ha, nil
}

// newHcloudClient initialized Hetzner Cloud client together with custom metrics and debug logger.
func (c AssignerConfig) newHcloudClient() (*hcloud.Client, error) {
	if c.AuthToken == "" {
		return nil, fmt.Errorf("auth token can't be empty: %w", errGeneric)
	}

	options := []hcloud.ClientOption{
		hcloud.WithToken(c.AuthToken),
		hcloud.WithDebugWriter(controller.Klogger{}),
	}

	if c.PrometheusRegistrer != nil {
		hcloudCalls := prometheus.NewCounterVec(prometheus.CounterOpts{
			Help:        "The total number of API calls to Hetzner Cloud.",
			Namespace:   "metallb_hcloud_controller",
			Subsystem:   "hcloud_api",
			Name:        "requests_total",
			ConstLabels: prometheus.Labels{},
		}, []string{})

		if err := c.PrometheusRegistrer.Register(hcloudCalls); err != nil {
			return nil, fmt.Errorf("registering Hetzner Cloud API calls metric: %w", err)
		}

		httpClient := &http.Client{ //nolint:exhaustivestruct
			Transport: promhttp.InstrumentRoundTripperCounter(hcloudCalls, http.DefaultTransport),
		}

		options = append(options, hcloud.WithHTTPClient(httpClient))
	}

	client := hcloud.NewClient(options...)

	if _, _, err := client.Server.List(context.TODO(), hcloud.ServerListOpts{}); err != nil {
		return nil, fmt.Errorf("verifying Hetzner Cloud client by listing servers: %w", err)
	}

	return client, nil
}

// hcloudAssigner implements controller.Assigner interface.
type hcloudAssigner struct {
	logger controller.Logger
	client *hcloud.Client
	config AssignerConfig
	ips    map[string]*hcloud.FloatingIP
}

// EnsureAssigned implements controller.Assigner interface for Hetzner Cloud virtual servers.
func (n *hcloudAssigner) EnsureAssigned(ctx context.Context, nodeAnnouncingByIP map[string]string) error {
	// Build list of floating IPs to be assigned.
	if err := n.updateFloatingIPs(ctx); err != nil {
		return fmt.Errorf("updating list of Floating IPs: %w", err)
	}

	for ip, node := range nodeAnnouncingByIP {
		if err := n.ensureNodeHasIPAssigned(ctx, node, ip); err != nil {
			return fmt.Errorf("ensuring node %q has IP address %q assigned: %w", node, ip, err)
		}
	}

	return nil
}

// updateFloatingIPs ensures that list of Floating IP addresses stored in assigner is up to date.
func (n *hcloudAssigner) updateFloatingIPs(ctx context.Context) error {
	// Build list of floating IPs to be assigned.
	floatingIPByIP := map[string]*hcloud.FloatingIP{}

	fips, err := n.client.FloatingIP.All(ctx)
	if err != nil {
		return fmt.Errorf("listing floating IPs: %w", err)
	}

	n.logger.Infof("hcloud: fetched %d Floating IPs", len(fips))

	for _, fip := range fips {
		floatingIPByIP[fip.IP.String()] = fip
	}

	n.ips = floatingIPByIP

	return nil
}

// ensureNodeHasIPAssigned ensures that given Kubernetes Node has given IP address assigned.
func (n *hcloudAssigner) ensureNodeHasIPAssigned(ctx context.Context, node, ip string) error {
	server, err := n.serverFromNodeName(ctx, node)
	if err != nil {
		return fmt.Errorf("server for IP %q not found: %w", ip, err)
	}

	fip, ok := n.ips[ip]
	if !ok {
		return fmt.Errorf("floating IP with IP %q not found: %w", ip, errGeneric)
	}

	if err := n.ensureFloatingIPAssigned(ctx, fip, server); err != nil {
		return fmt.Errorf("ensuring IP %q is assigned to node %q: %w", ip, node, errGeneric)
	}

	return nil
}

// serverFromNodeName returns Hetzner Cloud server object for a given Kubernetes Node name.
//
// If server do not exists, error is returned.
func (n *hcloudAssigner) serverFromNodeName(ctx context.Context, name string) (*hcloud.Server, error) {
	serverName := name + n.config.NodeSuffix

	n.logger.Infof("hcloud: Fetching server %q", serverName)

	server, _, err := n.client.Server.Get(ctx, serverName)
	if err != nil {
		return nil, fmt.Errorf("getting server %q: %w", serverName, err)
	}

	return server, nil
}

// ensureFloatingIPAssigned checks and decides if given Floating IP should be assigned to a given server.
//
// Decisions are logger to allow easy debugging.
//
// If assigner runs in dry-run mode, no actual assignment will be performed.
//
// If assignment succeeds, given Floating IP object is updated to reflect new assignment, so assignment
// is not done multiple times.
func (n *hcloudAssigner) ensureFloatingIPAssigned(ctx context.Context, fip *hcloud.FloatingIP, s *hcloud.Server) error {
	if fip.Server == nil {
		n.logger.Infof("Floating IP %q (%s) has no server assigned", fip.IP, fip.Name)
	}

	if fip.Server != nil && fip.Server.ID != s.ID {
		n.logger.Infof("Floating IP %q (%s) is assigned to server %d, "+
			"should be assigned to %d", fip.IP, fip.Name, fip.Server.ID, s.ID)
	}

	if fip.Server != nil && fip.Server.ID == s.ID {
		return nil
	}

	if n.config.DryRun {
		n.logger.Infof("Dry run, not doing anything.")

		return nil
	}

	n.logger.Infof("hcloud: Assigning floating IP %q (%s) to server %q\n", fip.IP, fip.Name, s.Name)

	if _, _, err := n.client.FloatingIP.Assign(ctx, fip, s); err != nil {
		return fmt.Errorf("assigning floating IP %q (%s) to server %q: %w", fip.Name, fip.IP, s.Name, err)
	}

	// Override FloatingIP field to reflect assignment in local list to avoid assigning
	// same Floating IP to single node multiple times.
	fip.Server.ID = s.ID

	return nil
}

type genericError string

func (e genericError) Error() string {
	return string(e)
}

const errGeneric genericError = "generic error"
