package installer

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/helm/pkg/kube"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type clientGetter struct {
	mgr manager.Manager
}

// assert interface
var _ genericclioptions.RESTClientGetter = &clientGetter{}

// NewTillerClientFromManager returns a Kubernetes client that can be used with
// a Tiller server.
func NewTillerClientFromManager(mgr manager.Manager) *kube.Client {
	return kube.New(&clientGetter{mgr})
}

func (c *clientGetter) ToRESTConfig() (*rest.Config, error) {
	return c.mgr.GetConfig(), nil
}

func (c *clientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(c.mgr.GetConfig())
	if err != nil {
		return nil, err
	}
	return &nonCachedDiscoveryClient{*dc}, nil
}

func (c *clientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	return c.mgr.GetRESTMapper(), nil
}

func (c *clientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return nil
}

type nonCachedDiscoveryClient struct {
	discovery.DiscoveryClient
}

func (dc *nonCachedDiscoveryClient) Fresh() bool {
	return true
}

func (dc *nonCachedDiscoveryClient) Invalidate() {}
