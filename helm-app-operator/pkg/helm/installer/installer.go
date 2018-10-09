package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/api"

	yaml "gopkg.in/yaml.v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"
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

// InstallRelease accepts an unstructured object, installs a Helm release using Tiller,
// and returns the object with updated `status`.
func (i installer) InstallRelease(u *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	rel := releaseName(u)
	cr, err := valuesFromResource(u)
	if err != nil {
		return u, fmt.Errorf("failed parsing values for release %s: %s", rel, err)
	}
	config := &cpb.Config{Raw: string(cr)}

	chart, err := chartutil.LoadDir(i.chartDir)
	if err != nil {
		return u, fmt.Errorf("failed loading chart %s for release %s: %s", i.chartDir, rel, err)
	}

	err = processRequirements(chart, config)
	if err != nil {
		return u, fmt.Errorf("failed processing requirements for release %s: %s", rel, err)
	}

	err = i.syncReleaseStatus(u)
	if err != nil {
		return u, fmt.Errorf("failed syncing status for release %s: %s", rel, err)
	}

	tiller := i.tillerRendererForCR(u)

	var updatedRelease *release.Release
	deployedRelease, err := i.storageBackend.Deployed(rel)
	if err != nil || deployedRelease == nil {
		updatedRelease, err = i.installRelease(u, tiller, chart, config)
		if err != nil {
			return u, fmt.Errorf("failed installing release %s: %s", rel, err)
		}
	} else {
		updatedRelease, err = i.updateRelease(u, tiller, deployedRelease, chart, config)
		if err != nil {
			return u, fmt.Errorf("failed updating release %s: %s", rel, err)
		}
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

	// If the release is not in the storage backend, it has already been uninstalled.
	_, err := i.storageBackend.Last(rel)
	if err != nil {
		return u, nil
	}

	log.Printf("uninstalling release for %s", rel)

	_, err = tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
		Name:  rel,
		Purge: true,
	})
	if err != nil {
		return u, fmt.Errorf("tiller failed uninstalling release %s: %s", rel, err)
	}
	return u, nil
}

func (i installer) installRelease(u *unstructured.Unstructured, tiller *tiller.ReleaseServer, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	rel := releaseName(u)
	installReq := &services.InstallReleaseRequest{
		Namespace: u.GetNamespace(),
		Name:      rel,
		Chart:     chart,
		Values:    config,
	}

	log.Printf("installing release for %s", rel)
	releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
	if err != nil {
		return nil, fmt.Errorf("tiller failed install: %s", err)
	}
	return releaseResponse.GetRelease(), nil
}

func (i installer) updateRelease(u *unstructured.Unstructured, tiller *tiller.ReleaseServer, deployedRelease *release.Release, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	rel := releaseName(u)
	force := isForceUpdate(u)
	dryRunReq := &services.UpdateReleaseRequest{
		Name:   rel,
		Chart:  chart,
		Values: config,
		Force:  force,
		DryRun: true,
	}

	dryRunResponse, err := tiller.UpdateRelease(context.TODO(), dryRunReq)
	if err != nil {
		return nil, fmt.Errorf("tiller failed dry run update: %s", err)
	}

	deployedManifest := deployedRelease.GetManifest()
	candidateManifest := dryRunResponse.GetRelease().GetManifest()

	if deployedManifest == candidateManifest {
		// reconcile resources
		log.Printf("reconciling resources for unchanged release %s", rel)
		if err := i.reconcileResources(u, deployedManifest, force); err != nil {
			return nil, fmt.Errorf("failed reconciling resources: %s", err)
		}

		// release didn't change so return the deployed release
		return deployedRelease, nil
	}

	log.Printf("updating release for %s", rel)

	updateReq := &services.UpdateReleaseRequest{
		Name:   rel,
		Chart:  chart,
		Values: config,
		Force:  force,
	}

	updateResponse, err := tiller.UpdateRelease(context.TODO(), updateReq)
	if err != nil {
		return nil, fmt.Errorf("tiller failed update: %s", err)
	}

	return updateResponse.GetRelease(), nil
}

func (i installer) reconcileResources(u *unstructured.Unstructured, expectedManifest string, force bool) error {
	expectedInfos, err := i.tillerKubeClient.BuildUnstructured(u.GetNamespace(), bytes.NewBufferString(expectedManifest))
	if err != nil {
		return fmt.Errorf("failed building unstructured objects: %s", err)
	}

	return expectedInfos.Visit(func(expected *resource.Info, err error) error {
		if err != nil {
			return err
		}
		err = reconcileObject(expected, force)
		if err != nil {
			return fmt.Errorf("failed reconciling object: %s", err)
		}
		return nil
	})
}

func reconcileObject(expected *resource.Info, force bool) error {
	helper := resource.NewHelper(expected.Client, expected.Mapping)

	// Attempt to create object
	_, err := helper.Create(expected.Namespace, true, expected.Object)
	if err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}

	// If object already exists, patch it instead. We can't do a diff patch
	// because Kubernetes sometimes automatically adds immutable fields
	// (e.g. `clusterIp` to a Service). This can cause reconciliation
	// failures even when the objects are otherwise completely unchanged.
	patch, err := json.Marshal(expected.Object)
	if err != nil {
		return fmt.Errorf("failed to marshal patch for object: %s", err)
	}

	_, err = helper.Patch(expected.Namespace, expected.Name, types.MergePatchType, patch)
	if err != nil {
		if !force {
			return fmt.Errorf("failed patching object: %s", err)
		}

		// If forcing update, delete and recreate object
		_, err = helper.Delete(expected.Namespace, expected.Name)
		if err != nil {
			return fmt.Errorf("failed deleting object: %s", err)
		}
		_, err := helper.Create(expected.Namespace, true, expected.Object)
		if err != nil {
			return fmt.Errorf("failed creating object: %s", err)
		}
	}
	return nil
}

func valuesFromResource(u *unstructured.Unstructured) ([]byte, error) {
	return yaml.Marshal(u.Object["spec"])
}

func releaseName(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", operatorName, u.GetName())
}

// Force updates are currently unsupported, so just return false for now.
func isForceUpdate(u *unstructured.Unstructured) bool {
	return false
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
