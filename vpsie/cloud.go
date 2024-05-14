package vpsie

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/vpsie/govpsie"
	"golang.org/x/oauth2"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const (
	// providerName defines the cloud provider
	providerName                 = "vpsie"
	accessTokenEnv               = "VPSIE_API_KEY" //nolint
	dcIdentifier                 = "DCIDENNTIFIER"
	apiURL                       = "API_URL"
	userAgent                    = "vpsie-cloud-controller-manager/1.0.0"
	loadbalancerNameAnnotation   = "service.beta.kubernetes.io/vpsie-loadbalancer-name"
	hostnameAnnotation           = "service.beta.kubernetes.io/vpsie-hostname"
	algorithmAnnotation          = "service.beta.kubernetes.io/vpsie-algorithm"
	cookieNameAnnotation         = "service.beta.kubernetes.io/vpsie-cookie-name"
	cookieCheckAnnotation        = "service.beta.kubernetes.io/vpsie-cookie-check"
	resourceIdentifierAnnotation = "service.beta.kubernetes.io/vpsie-resource-identifier"
	redirectHttpAnnotation       = "service.beta.kubernetes.io/vpsie-redirecthttp"
	lBProtocolAnnotation         = "service.beta.kubernetes.io/vpsie-lb-protocol"
	protocolTCP                  = "tcp"
	protocolHTTP                 = "http"
	protocolHTTPS                = "https"
	protocolHTTP2                = "http2"
	httpPortsAnnotation          = "service.beta.kubernetes.io/vpsie-http-port"
	httpsPortsAnnotation         = "service.beta.kubernetes.io/vpsie-https-port"
	http2PortsAnnotation         = "service.beta.kubernetes.io/vpsie-http2-port"
)

type cloud struct {
	client        *govpsie.Client
	loadbalancers cloudprovider.LoadBalancer
}

func (p *cloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
}

// LoadBalancer returns a balancer interface. Also returns true if the interface is supported, false otherwise.
func (c *cloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	klog.V(5).Info("called LoadBalancer") //nolint
	return c.loadbalancers, true

}

// Instances returns an instances interface. Also returns true if the interface is supported, false otherwise.
func (c *cloud) Instances() (cloudprovider.Instances, bool) {
	return nil, false
}

func (c *cloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	return nil, false
}

// Zones returns a zones interface. Also returns true if the interface is supported, false otherwise.
func (c *cloud) Zones() (cloudprovider.Zones, bool) {
	return nil, false

}

// Clusters returns a clusters interface.  Also returns true if the interface is supported, false otherwise.
func (c *cloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false

}

// Routes returns a routes interface along with whether the interface is supported.
func (c *cloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

// ProviderName returns the cloud provider ID.
func (c *cloud) ProviderName() string {
	return providerName

}

// HasClusterID returns true if a ClusterID is required and set
func (c *cloud) HasClusterID() bool {
	return false
}

func newCloud() (cloudprovider.Interface, error) {
	accessToken := os.Getenv(accessTokenEnv)

	if accessToken == "" {
		return nil, fmt.Errorf("environment variable %q is requried", accessTokenEnv)
	}

	datacenter := os.Getenv(dcIdentifier)
	if dcIdentifier == "" {
		return nil, fmt.Errorf("environment variable %q is required", dcIdentifier)
	}

	klog.Info("Creating Vpsie client")

	client := govpsie.NewClient(oauth2.NewClient(context.Background(), nil))

	client.SetUserAgent(userAgent)
	client.SetRequestHeaders(map[string]string{
		"Vpsie-Auth": accessToken,
	})

	return &cloud{
		client:        client,
		loadbalancers: newLoadbalancers(client, datacenter),
	}, nil
}

func init() {
	cloudprovider.RegisterCloudProvider(providerName, func(io.Reader) (cloudprovider.Interface, error) {
		return newCloud()
	})
}
