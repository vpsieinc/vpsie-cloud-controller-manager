package vpsie

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vpsie/govpsie"
	v1 "k8s.io/api/core/v1"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/cloud-provider/api"
	"k8s.io/klog/v2"
)

var errLBNotFound error = errors.New("loadbalancer not found")

type loadbalancers struct {
	client     *govpsie.Client
	datacenter string
}

func newLoadbalancers(client *govpsie.Client, datacenter string) cloudprovider.LoadBalancer {
	return &loadbalancers{
		client:     client,
		datacenter: datacenter,
	}
}

func (l *loadbalancers) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (status *v1.LoadBalancerStatus, exists bool, err error) {
	lbName := l.GetLoadBalancerName(ctx, clusterName, service)

	lb, err := l.getLoadBalancerByName(ctx, lbName)
	if err != nil {
		if err == errLBNotFound {
			return nil, false, nil
		}

		return nil, false, err
	}

	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{
			{
				IP: lb.DefaultIP,
			},
		},
	}, true, nil

}

func (l *loadbalancers) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	return getLoadBalancerName(service)
}

func (l *loadbalancers) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	lbName := l.GetLoadBalancerName(ctx, clusterName, service)

	lbRequest, err := l.buildLoadBalancerRequest(ctx, service, nodes)
	if err != nil {
		return nil, fmt.Errorf("failed to build load-balancer request: %s", err)
	}

	lb, err := l.getLoadBalancerByName(ctx, lbName)
	if err == nil {
		err = l.updateLoadBalancer(ctx, lb, service, nodes)
		if err != nil {
			return nil, err
		}
	} else if err == errLBNotFound {
		err = l.client.LB.CreateLB(ctx, lbRequest)
		if err != nil {
			return nil, fmt.Errorf("failed to create load-balancer: %s", err)
		}
	} else {
		return nil, err
	}

	lbDetail, err := l.getLoadBalancerByName(ctx, lbName)
	if err != nil {
		if err == errLBNotFound {
			return nil, api.NewRetryError("loadbalancer is in the process of creation", 65*time.Second)
		}

		return nil, err
	}

	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{
			{
				IP: lbDetail.DefaultIP,
			},
		},
	}, nil
}

func (l *loadbalancers) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	lbName := l.GetLoadBalancerName(ctx, clusterName, service)

	lb, err := l.getLoadBalancerByName(ctx, lbName)
	if err != nil {
		return err
	}

	return l.updateLoadBalancer(ctx, lb, service, nodes)
}

func (l *loadbalancers) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	lbName := l.GetLoadBalancerName(ctx, clusterName, service)

	lb, err := l.getLoadBalancerByName(ctx, lbName)
	if err != nil {
		if err == errLBNotFound {
			return nil
		}

		return err
	}

	klog.Infof("Deleting loadbalancer %s: with id %s", lbName, lb.Identifier)
	for _, rule := range lb.Rules {
		l.client.LB.DeleteLBRule(ctx, rule.RuleID)
	}
	return l.client.LB.DeleteLB(ctx, lb.Identifier, "no longer needed", "from cloud-controller")
}

func getLoadBalancerName(service *v1.Service) string {
	name := service.Annotations[loadbalancerNameAnnotation]

	if len(name) > 0 {
		return name
	}

	return cloudprovider.DefaultLoadBalancerName(service)
}

func (l *loadbalancers) getLoadBalancerByName(ctx context.Context, lbName string) (*govpsie.LBDetails, error) {
	lbs, err := l.client.LB.ListLBs(ctx, &govpsie.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, lb := range lbs {
		if lb.LBName == lbName {
			return l.client.LB.GetLB(ctx, lb.Identifier)
		}
	}

	return nil, errLBNotFound
}

func (l *loadbalancers) updateLoadBalancer(ctx context.Context, lb *govpsie.LBDetails, service *v1.Service, nodes []*v1.Node) error {
	lbRequest, err := l.buildLoadBalancerRequest(ctx, service, nodes)
	if err != nil {
		return err
	}

	err = l.reconcileRules(ctx, lb, lbRequest)
	if err != nil {
		return err
	}

	if len(lbRequest.Rule) > 0 {
		backends := lbRequest.Rule[0].Backends
		err = l.reconcileBackends(ctx, lb, backends)
		if err != nil {
			return err
		}
	}

	return nil
}

func (l *loadbalancers) reconcileRules(ctx context.Context, lb *govpsie.LBDetails, lbRequest *govpsie.CreateLBReq) error {

	lbList := lb.Rules

	for _, req := range lbRequest.Rule {
		unchangedRule := -1
		tobeUpated := -1
		for i, rule := range lbList {

			if fmt.Sprint(rule.FrontPort) != req.FrontPort || req.Scheme != rule.Scheme {
				continue
			}

			if fmt.Sprint(rule.BackPort) != req.BackPort {
				tobeUpated = i
				continue
			}

			unchangedRule = i
			break
		}

		if unchangedRule != -1 {
			lbList[unchangedRule] = lbList[len(lbList)-1]
			lbList = lbList[:len(lbList)-1]
			continue
		}

		if unchangedRule == -1 && tobeUpated != -1 {
			frontPort, err := strconv.Atoi(req.FrontPort)
			if err != nil {
				return err
			}

			backPort, err := strconv.Atoi(req.BackPort)
			if err != nil {
				return err
			}

			err = l.client.LB.UpdateLBRules(ctx, &govpsie.RuleUpdateReq{
				RuleID:    lbList[tobeUpated].RuleID,
				Backends:  req.Backends,
				BackPort:  backPort,
				Scheme:    req.Scheme,
				FrontPort: frontPort,
			})

			if err != nil {
				return err
			}

			lbList[tobeUpated] = lbList[len(lbList)-1]
			lbList = lbList[:len(lbList)-1]
			continue
		}

		if unchangedRule == -1 && tobeUpated == -1 {
			err := l.client.LB.AddLBRule(ctx, &govpsie.AddRuleReq{
				Scheme:    req.Scheme,
				FrontPort: req.FrontPort,
				LbId:      lb.Identifier,
				BackPort:  req.BackPort,
			})

			if err != nil {
				return err
			}
		}
	}

	for _, rule := range lbList {
		err := l.client.LB.DeleteLBRule(ctx, rule.RuleID)

		if err != nil {
			return err
		}
	}

	return nil
}

func (l *loadbalancers) reconcileBackends(ctx context.Context, lb *govpsie.LBDetails, backends []govpsie.Backend) error {
	for _, rule := range lb.Rules {
		if rule.Scheme == "tcp" {

			err := l.client.LB.UpdateLBRules(ctx, &govpsie.RuleUpdateReq{
				RuleID:    rule.RuleID,
				Backends:  backends,
				BackPort:  rule.BackPort,
				Scheme:    rule.Scheme,
				FrontPort: rule.FrontPort,
			})

			if err != nil {
				return err
			}
		} else {
			for _, domain := range rule.Domains {
				err := l.client.LB.UpdateDomainBackend(ctx, domain.DomainID, backends)

				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (l *loadbalancers) buildLoadBalancerRequest(ctx context.Context, service *v1.Service, nodes []*v1.Node) (*govpsie.CreateLBReq, error) {

	redirect, err := getRedirectHttp(service)
	if err != nil {
		return nil, err
	}

	cookieCheck, err := getCookieCheck(service)
	if err != nil {
		return nil, err
	}
	var cookieName string = ""
	if cookieCheck {
		cookieName, err = getCookieName(service)
		if err != nil {
			return nil, err
		}
	}

	getResourceIdentifier, err := getResourceIdentifier(service)
	if err != nil {
		return nil, err
	}

	rules, err := l.buildForwardingRules(service, nodes)
	if err != nil {
		return nil, err
	}

	return &govpsie.CreateLBReq{
		CookieName:         cookieName,
		CookieCheck:        false,
		RedirectHTTP:       redirect,
		ResourceIdentifier: getResourceIdentifier,
		DcIdentifier:       l.datacenter,
		Rule:               rules,
		LBName:             getLoadBalancerName(service),
		Algorithm:          getAlgorithm(service),
	}, nil
}

// getAlgorithm returns the algorithm to be used for load balancer service
// defaults to round_robin if no algorithm is provided.
func getAlgorithm(service *v1.Service) string {
	algo := service.Annotations[algorithmAnnotation]
	if algo == "least_connections" {
		return "leastconn"
	}

	return "roundrobin"
}

// getCookieName returns cookie name
func getCookieName(service *v1.Service) (string, error) {
	name, ok := service.Annotations[cookieNameAnnotation]
	if !ok || name == "" {
		return "", fmt.Errorf("cookie name not specified, but required")
	}

	return name, nil
}

func getRedirectHttp(service *v1.Service) (int, error) {
	redirect, ok := service.Annotations[redirectHttpAnnotation]
	if !ok || redirect == "" {
		return 0, nil
	}

	return strconv.Atoi(redirect)
}

func getCookieCheck(service *v1.Service) (bool, error) {
	cookieCheck, ok := service.Annotations[cookieCheckAnnotation]
	if !ok || cookieCheck == "" {
		return false, nil
	}

	return strconv.ParseBool(cookieCheck)
}

// getResourceIdentifier returns resource identifier
func getResourceIdentifier(service *v1.Service) (string, error) {
	identifier, ok := service.Annotations[resourceIdentifierAnnotation]
	if !ok || identifier == "" {
		return "", fmt.Errorf("resource identifier  not specified, but required")
	}

	return identifier, nil
}

func (l *loadbalancers) buildForwardingRules(service *v1.Service, nodes []*v1.Node) ([]govpsie.Rule, error) {
	// serverIdentifiers, err := buildServerList(nodes)
	// if err != nil {
	// 	return nil, err
	// }

	// backends, err := l.buildBackendList(context.Background(), serverIdentifiers)
	// if err != nil {
	// 	return nil, err
	// }

	backends, err := buildBackends(nodes)
	if err != nil {
		return nil, err
	}

	defaultProtocol, err := getLBProtocol(service)
	if err != nil {
		return nil, err
	}

	httpPorts, err := getHTTPPorts(service)
	if err != nil {
		return nil, err
	}

	httpsPorts, err := getHTTPSPorts(service)
	if err != nil {
		return nil, err
	}

	http2Ports, err := getHTTP2Ports(service)
	if err != nil {
		return nil, err
	}

	httpPortMap := map[int32]bool{}
	for _, port := range httpPorts {
		httpPortMap[int32(port)] = true
	}
	httpsPortMap := map[int32]bool{}
	for _, port := range httpsPorts {
		httpsPortMap[int32(port)] = true
	}
	http2PortMap := map[int32]bool{}
	for _, port := range http2Ports {
		http2PortMap[int32(port)] = true
	}

	var rules []govpsie.Rule
	for _, port := range service.Spec.Ports {
		protocol := defaultProtocol
		if httpPortMap[port.Port] {
			protocol = protocolHTTP
		}
		if httpsPortMap[port.Port] {
			protocol = protocolHTTPS
		}
		if http2PortMap[port.Port] {
			protocol = protocolHTTP2
		}

		rule, err := buildForwardingRule(service, &port, protocol, backends)
		if err != nil {
			return nil, err
		}
		rules = append(rules, *rule)
	}

	return rules, nil
}

func buildForwardingRule(service *v1.Service, port *v1.ServicePort, protocol string, backends []govpsie.Backend) (*govpsie.Rule, error) {
	var rule govpsie.Rule

	if port.Protocol == "udp" {
		return nil, fmt.Errorf("TCP protocol is only supported: received %s", port.Protocol)
	}

	rule.Scheme = protocol

	rule.FrontPort = fmt.Sprint(port.Port)
	rule.BackPort = fmt.Sprint(port.NodePort)
	if backends == nil {
		rule.Backends = []govpsie.Backend{}
		klog.Infof("backends nil: %v", rule.Backends)
	} else {

		rule.Backends = backends
	}

	return &rule, nil
}

func serverIDFromProviderID(providerID string) (string, error) {
	klog.Infof("profiderId: %s", providerID)
	if providerID == "" {
		return "", fmt.Errorf("empty providerID")
	}

	split := strings.Split(providerID, "://")
	if len(split) != 2 {
		return "", fmt.Errorf("invalid providerID")
	}

	if split[0] != providerName {
		return "", fmt.Errorf("invalid providerID")
	}

	return split[1], nil
}

// buildInstanceList create list of nodes to be attached to a load balancer
func buildServerList(nodes []*v1.Node) ([]string, error) {
	var list []string

	for _, node := range nodes {
		serverIdentifier, err := serverIDFromProviderID(node.Spec.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("error getting the provider ID %s : %s", node.Spec.ProviderID, err)
		}

		list = append(list, serverIdentifier)
	}

	klog.Infof("server list: %v", list)

	return list, nil
}

func buildBackends(nodes []*v1.Node) ([]govpsie.Backend, error) {
	var list []govpsie.Backend

	for _, node := range nodes {
		for _, address := range node.Status.Addresses {
			if address.Type == v1.NodeExternalIP || address.Type == v1.NodeInternalIP {
				list = append(list, govpsie.Backend{
					Ip:           address.Address,
					VmIdentifier: node.Spec.ProviderID,
					Type:         "k8s",
				})

			}
		}
	}

	return list, nil
}

func getLBProtocol(service *v1.Service) (string, error) {
	protocol, ok := service.Annotations[lBProtocolAnnotation]
	if !ok {
		return protocolTCP, nil
	}

	switch protocol {
	case protocolTCP, protocolHTTP, protocolHTTPS, protocolHTTP2:
	default:
		return "", fmt.Errorf("invalid protocol %q specified in annotation %q", protocol, lBProtocolAnnotation)
	}

	return protocol, nil

}

// getHTTPPorts returns the ports for the given service that are set to use
// HTTP.
func getHTTPPorts(service *v1.Service) ([]int, error) {
	return getPorts(service, httpPortsAnnotation)
}

// getHTTP2Ports returns the ports for the given service that are set to use
// HTTP2.
func getHTTP2Ports(service *v1.Service) ([]int, error) {
	return getPorts(service, http2PortsAnnotation)
}

// getHTTPSPorts returns the ports for the given service that are set to use
// HTTPS.
func getHTTPSPorts(service *v1.Service) ([]int, error) {
	return getPorts(service, httpsPortsAnnotation)
}

// getPorts returns the ports for the given service and annotation.
func getPorts(service *v1.Service, anno string) ([]int, error) {
	ports, ok := service.Annotations[anno]
	if !ok {
		return nil, nil
	}

	portsSlice := strings.Split(ports, ",")

	portsInt := make([]int, len(portsSlice))
	for i, port := range portsSlice {
		port, err := strconv.Atoi(port)
		if err != nil {
			return nil, err
		}

		portsInt[i] = port
	}

	return portsInt, nil
}

func (l *loadbalancers) buildBackendList(ctx context.Context, serverIdentifiers []string) ([]govpsie.Backend, error) {
	var backends []govpsie.Backend

	for _, serverIdentifier := range serverIdentifiers {
		backend, err := l.client.LB.GetLB(ctx, serverIdentifier)
		if err != nil {
			return nil, err
		}

		backends = append(backends, govpsie.Backend{
			Ip:           backend.DefaultIP,
			VmIdentifier: serverIdentifier,
		})
	}

	return backends, nil
}
