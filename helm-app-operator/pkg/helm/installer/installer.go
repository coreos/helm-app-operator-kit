package installer

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"reflect"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/api"

	yaml "gopkg.in/yaml.v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/engine"
	"k8s.io/helm/pkg/kube"
	cpb "k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	"k8s.io/helm/pkg/storage"
	storageerrors "k8s.io/helm/pkg/storage/errors"
	"k8s.io/helm/pkg/tiller"
	"k8s.io/helm/pkg/tiller/environment"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
)

// Installer can install and uninstall Helm releases given a custom resource
// which provides runtime values for the Chart.
type Installer interface {
	InstallRelease(u *unstructured.Unstructured) (*unstructured.Unstructured, error)
	UninstallRelease(u *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

// assert interface
var _ Installer = installer{}

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

	// OperatorNameEnvVar is the environment variable for the operator name,
	// which is used in the release name for helm releases. If not set, a
	// default value will be used.
	OperatorNameEnvVar = "OPERATOR_NAME"

	defaultOperatorName         = "helm-app-operator"
	defaultHelmChartWatchesFile = "/opt/helm/watches.yaml"
)

var (
	operatorName = defaultOperatorName
)

func init() {
	setOperatorName()
}

// installer is an implementation of the Installer interface, which is used to
// reconcile CR updates for GVKs registered as helm apps.
type installer struct {
	tillerKubeClient *kube.Client
	storageBackend   *storage.Storage
	chartDir         string
}

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

// InstallRelease accepts an unstructured object, installs a Helm release using Tiller,
// and returns the object with updated `status`.
func (i installer) InstallRelease(u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	rel := releaseName(u)
	cr, err := valuesFromResource(u)
	if err != nil {
		return u, err
	}

	chart, err := chartutil.LoadDir(i.chartDir)
	if err != nil {
		return u, fmt.Errorf("failed loading chart %s: %s", i.chartDir, err)
	}

	err = i.syncReleaseStatus(u)
	if err != nil {
		return u, fmt.Errorf("failed syncing release status: %s", err)
	}

	tiller := i.tillerRendererForCR(u)

	var updatedRelease *release.Release
	latestRelease, err := i.storageBackend.Last(releaseName(u))
	if err != nil || latestRelease == nil {
		installReq := &services.InstallReleaseRequest{
			Namespace: u.GetNamespace(),
			Name:      releaseName(u),
			Chart:     chart,
			Values:    &cpb.Config{Raw: string(cr)},
		}

		err := processRequirements(installReq.Chart, installReq.Values)
		if err != nil {
			return u, fmt.Errorf("failed processing requirements for release %s: %s", rel, err)
		}

		log.Printf("installing release for %s", rel)
		releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
		if err != nil {
			return u, fmt.Errorf("tiller failed installing release for %s: %s", rel, err)
		}
		updatedRelease = releaseResponse.GetRelease()
	} else {
		updateReq := &services.UpdateReleaseRequest{
			Name:   releaseName(u),
			Chart:  chart,
			Values: &cpb.Config{Raw: string(cr)},
		}

		err := processRequirements(updateReq.Chart, updateReq.Values)
		if err != nil {
			return u, fmt.Errorf("failed processing requirements for release %s: %s", rel, err)
		}

		if reflect.DeepEqual(latestRelease.Chart, updateReq.Chart) && reflect.DeepEqual(latestRelease.Config, updateReq.Values) {
			log.Printf("skipping release update for %s: no change detected", rel)
			return u, nil
		}

		log.Printf("updating release for %s", rel)
		releaseResponse, err := tiller.UpdateRelease(context.TODO(), updateReq)
		if err != nil {
			return u, fmt.Errorf("tiller failed updating release for %s: %s", rel, err)
		}
		updatedRelease = releaseResponse.GetRelease()
	}

	status := api.StatusFor(u)
	status.SetRelease(updatedRelease)
	status.SetPhase(api.PhaseApplied, api.ReasonApplySuccessful, "")
	u.Object["status"] = status

	return u, nil
}

// UninstallRelease accepts an unstructured object, uninstalls a Helm release
// using Tiller, and returns the object with updated `status`.
func (i installer) UninstallRelease(u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	rel := releaseName(u)
	tiller := i.tillerRendererForCR(u)

	log.Printf("uninstalling release for %s", rel)
	_, err := tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
		Name:  releaseName(u),
		Purge: true,
	})
	if err != nil {
		return u, fmt.Errorf("tiller failed uninstalling release for %s: %s", rel, err)
	}
	return u, nil
}

func valuesFromResource(u *unstructured.Unstructured) ([]byte, error) {
	return yaml.Marshal(u.Object["spec"])
}

func releaseName(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", operatorName, u.GetName())
}

// syncReleaseStatus ensures the object's release is present in the storage
// backend
func (i installer) syncReleaseStatus(u *unstructured.Unstructured) error {
	status := api.StatusFor(u)
	if status.Release == nil {
		return nil
	}
	_, err := i.storageBackend.Get(status.Release.GetName(), status.Release.GetVersion())
	if err != nil {
		key := fmt.Sprintf("%s.v%d", status.Release.GetName(), status.Release.GetVersion())
		if err.Error() == storageerrors.ErrReleaseNotFound(key).Error() {
			return i.storageBackend.Create(status.Release)
		}
		return err
	}
	return nil
}

// tillerRendererForCR creates a ReleaseServer configured with a rendering
// engine that adds ownerrefs to rendered assets based on the CR.
func (i installer) tillerRendererForCR(u *unstructured.Unstructured) *tiller.ReleaseServer {
	controllerRef := metav1.NewControllerRef(u, u.GroupVersionKind())
	ownerRefs := []metav1.OwnerReference{
		*controllerRef,
	}
	baseEngine := engine.New()
	e := newOwnerRefEngine(baseEngine, ownerRefs)
	var ey environment.EngineYard = map[string]environment.Engine{
		environment.GoTplEngine: e,
	}
	env := &environment.Environment{
		EngineYard: ey,
		Releases:   i.storageBackend,
		KubeClient: i.tillerKubeClient,
	}
	cfg, _ := i.tillerKubeClient.ToRESTConfig()
	internalClientSet, err := internalclientset.NewForConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	return tiller.NewReleaseServer(env, internalClientSet, false)
}

// processRequirements will process the requirements file.
// It will disable/enable the charts based on condition in requirements file,
// and it imports the specified chart values from child to parent.
func processRequirements(chart *cpb.Chart, values *cpb.Config) error {
	err := chartutil.ProcessRequirementsEnabled(chart, values)
	if err != nil {
		return err
	}
	err = chartutil.ProcessRequirementsImportValues(chart)
	if err != nil {
		return err
	}
	return nil
}

func setOperatorName() {
	v := os.Getenv(OperatorNameEnvVar)
	if v != "" {
		operatorName = v
	}
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
