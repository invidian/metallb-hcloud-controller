package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/invidian/metallb-hcloud-controller/pkg/assigners/hcloud"
	"github.com/invidian/metallb-hcloud-controller/pkg/controller"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

const (
	hcloudTokenEnv = "HCLOUD_TOKEN"
	kubeconfigEnv  = "KUBECONFIG"
	nodeSuffixEnv  = "NODE_SUFFIX"
	dryRunEnv      = "DRY_RUN"

	// The only expected HTTP server clients are kubelet performing liveness probes and
	// Prometheus scraping the metrics, so it is not even that important to shut down gracefully.
	httpShutdownTimeout = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		klog.Infof("Running failed: %v", err)

		os.Exit(1)
	}
}

func run() error {
	if err := initializeKlog(); err != nil {
		return fmt.Errorf("initializing klog for logging: %w", err)
	}

	done := make(chan struct{})

	reg, err := defaultPrometheusRegisterer()
	if err != nil {
		return fmt.Errorf("building default Prometheus metrics register: %w", err)
	}

	hcloudAssigner, err := hcloudAssignerWithMetrics(reg, done)
	if err != nil {
		return fmt.Errorf("initializing Hetzner Cloud Assigner with Prometheus metrics: %w", err)
	}

	config := controller.MetalLBHCloudControllerConfig{ //nolint:exhaustivestruct
		KubeconfigPath: os.Getenv(kubeconfigEnv),
		StopCh:         done,
		Assigners: map[string]controller.Assigner{
			"hcloud": hcloudAssigner,
		},
		PrometheusRegistrer: reg,
	}

	// Start controller.
	controller, err := config.New()
	if err != nil {
		return fmt.Errorf("starting controller: %w", err)
	}

	// Block until controller shuts down.
	<-controller.ShutdownCh

	return nil
}

func initializeKlog() error {
	klog.InitFlags(nil)

	if err := flag.Set("v", "4"); err != nil {
		return fmt.Errorf("setting flag %q: %w", "v", err)
	}

	return nil
}

func hcloudAssignerWithMetrics(reg *prometheus.Registry, done chan struct{}) (controller.Assigner, error) {
	server := startMetricsServer(reg)

	// Handle signals.
	go handleInterrupts(server, done)

	hcloudAssignerConfig := hcloud.AssignerConfig{ //nolint:exhaustivestruct
		AuthToken:           os.Getenv(hcloudTokenEnv),
		NodeSuffix:          os.Getenv(nodeSuffixEnv),
		PrometheusRegistrer: reg,
		DryRun:              os.Getenv(dryRunEnv) != "",
	}

	hcloudAssigner, err := hcloudAssignerConfig.New()
	if err != nil {
		return nil, fmt.Errorf("creating Hetzner Cloud assigner: %w", err)
	}

	return hcloudAssigner, nil
}

func defaultPrometheusRegisterer() (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()

	if err := reg.Register(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("registering process collector: %w", err)
	}

	if err := reg.Register(prometheus.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("registering Go collector: %w", err)
	}

	return reg, nil
}

func startMetricsServer(gatherer prometheus.Gatherer) *http.Server {
	mux := http.NewServeMux()

	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))

	server := &http.Server{ //nolint:exhaustivestruct
		Addr:    ":2112",
		Handler: mux,
	}

	// Start HTTP server.
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			klog.Errorf("listening for metrics: %v", err)
		}
	}()

	return server
}

func handleInterrupts(server *http.Server, done chan struct{}) {
	// signChan channel is used to transmit signal notifications.
	signChan := make(chan os.Signal, 1)

	// Catch and relay certain signal(s) to signChan channel.
	signal.Notify(signChan, os.Interrupt, syscall.SIGTERM)

	// Blocking until a signal is sent over signChan channel.
	<-signChan

	klog.Infof("Received shutdown signal, shutting down HTTP server...")

	// Create a new context with a timeout duration. It helps allowing
	// timeout duration to all active connections in order for them to
	// finish their job. Any connections that won't complete within the
	// allowed timeout duration gets halted.
	ctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		klog.Errorf("Failed shutting down HTTP server: %v", err)
	}

	klog.Infof("Finished shutting down HTTP server")

	// Actual shutdown trigger.
	close(done)
}
