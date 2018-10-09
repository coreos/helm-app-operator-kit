package installer

import (
	"fmt"
	"io/ioutil"
	"os"

	yaml "gopkg.in/yaml.v2"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/helm/pkg/kube"
	"k8s.io/helm/pkg/storage"
)

const (

	// HelmChartWatchesEnvVar is the environment variable for a YAML
	// configuration file containing mappings of GVKs to helm charts.
	// Use of this environment variable overrides the watch configuration
	// provided by API_VERSION, KIND, and HELM_CHART, and allows users to
	// configurable multiple watches, each with a different chart.
	HelmChartWatchesEnvVar = "HELM_CHART_WATCHES"

	// APIVersionEnvVar is the environment variable for the group and version
	// to be watched using the format `<group>/<version>`
	// (e.g. "example.com/v1alpha1").
	APIVersionEnvVar = "API_VERSION"

	// KindEnvVar is the environment variable for the kind to be watched. The
	// value is typically singular and should be CamelCased (e.g. "MyApp").
	KindEnvVar = "KIND"

	// HelmChartEnvVar is the environment variable for the directory location
	// of the helm chart to be installed for CRs that match the values for the
	// API_VERSION and KIND environment variables.
	HelmChartEnvVar = "HELM_CHART"
)

// watch holds data used to create a mapping of GVK to helm chart.
// The mapping is used to compose a helm app operator.
type watch struct {
	Version string `yaml:"version"`
	Group   string `yaml:"group"`
	Kind    string `yaml:"kind"`
	Chart   string `yaml:"chart"`
}

// NewFromEnv returns a map of installers based on configuration provided in
// the environment.
func NewFromEnv(tillerKubeClient *kube.Client, storageBackend *storage.Storage) (map[schema.GroupVersionKind]Installer, error) {
	// If there is a watches file available, get Installers from it
	if watchesFile, ok := getWatchesFile(); ok {
		return NewFromWatches(tillerKubeClient, storageBackend, watchesFile)
	}

	// Otherwise, we'll fall back to the GVK environment variables
	gv, err := schema.ParseGroupVersion(os.Getenv(APIVersionEnvVar))
	if err != nil {
		return nil, err
	}
	kind := os.Getenv(KindEnvVar)
	s := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    kind,
	}

	chartDir := os.Getenv(HelmChartEnvVar)
	if chartDir == "" {
		return nil, fmt.Errorf("chart must be defined for %v", s)
	}

	m := map[schema.GroupVersionKind]Installer{
		s: New(tillerKubeClient, storageBackend, chartDir),
	}

	return m, nil
}

// NewFromWatches reads the config file at the provided path and returns a map
// of installers for each GVK in the config.
func NewFromWatches(tillerKubeClient *kube.Client, storageBackend *storage.Storage, path string) (map[schema.GroupVersionKind]Installer, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}
	watches := []watch{}
	err = yaml.Unmarshal(b, &watches)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	m := map[schema.GroupVersionKind]Installer{}
	for _, w := range watches {
		s := schema.GroupVersionKind{
			Group:   w.Group,
			Version: w.Version,
			Kind:    w.Kind,
		}
		// Check if schema is a duplicate
		if _, ok := m[s]; ok {
			return nil, fmt.Errorf("duplicate GVK: %v", s.String())
		}
		if w.Chart == "" {
			return nil, fmt.Errorf("chart must be defined for %v", s)
		}
		m[s] = New(tillerKubeClient, storageBackend, w.Chart)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("no watches configured in watches file")
	}
	return m, nil
}

// New returns a new Helm installer capable of installing and uninstalling releases.
func New(tillerKubeClient *kube.Client, storageBackend *storage.Storage, chartDir string) Installer {
	return installer{tillerKubeClient, storageBackend, chartDir}
}

func getWatchesFile() (string, bool) {
	// If the watches env variable is set (even if it's an empty string), use it
	// since the user explicitly set it.
	if watchesFile, ok := os.LookupEnv(HelmChartWatchesEnvVar); ok {
		return watchesFile, true
	}

	// Next, check if the default watches file is present. If so, use it.
	if _, err := os.Stat(defaultHelmChartWatchesFile); err == nil {
		return defaultHelmChartWatchesFile, true
	}
	return "", false
}
