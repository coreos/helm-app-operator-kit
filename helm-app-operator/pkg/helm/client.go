package helm

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/helm/pkg/kube"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// NewTillerClientFromManager returns a Kubernetes client that can be used with
// a Tiller server.
func NewTillerClientFromManager(mgr manager.Manager) (*kube.Client, error) {
	c, err := newClientGetter(mgr)
	if err != nil {
		return nil, err
	}
	return kube.New(c), nil
}

type clientGetter struct {
	restConfig      *rest.Config
	discoveryClient discovery.CachedDiscoveryInterface
	restMapper      meta.RESTMapper
}

func (c *clientGetter) ToRESTConfig() (*rest.Config, error) {
	return c.restConfig, nil
}

func (c *clientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	return c.discoveryClient, nil
}

func (c *clientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	return c.restMapper, nil
}

func (c *clientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return nil
}

func newClientGetter(mgr manager.Manager) (*clientGetter, error) {
	cfg := mgr.GetConfig()
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	cdc := cached.NewMemCacheClient(dc)
	rm := mgr.GetRESTMapper()

	return &clientGetter{
		restConfig:      cfg,
		discoveryClient: cdc,
		restMapper:      rm,
	}, nil
}
