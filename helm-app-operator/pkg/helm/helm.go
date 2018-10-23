// Copyright 2018 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	"github.com/sirupsen/logrus"

	yaml "gopkg.in/yaml.v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/helm/pkg/chartutil"
	"k8s.io/helm/pkg/engine"
	"k8s.io/helm/pkg/kube"
	cpb "k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/tiller"
	"k8s.io/helm/pkg/tiller/environment"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/apis/app/v1alpha1"
)

const (
	// HelmChartWatchesEnvVar is the environment variable for a YAML
	// configuration file containing mappings of GVKs to helm charts. Use of
	// this environment variable overrides the watch configuration provided
	// by API_VERSION, KIND, and HELM_CHART, and it allows users to configure
	// multiple watches, each with a different chart.
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

	operatorName                = "helm-app-operator"
	defaultHelmChartWatchesFile = "/opt/helm/watches.yaml"
)

// Installer can install and uninstall Helm releases given a custom resource
// which provides runtime values for the Chart.
type Installer interface {
	ReconcileRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, bool, error)
	UninstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

type installer struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chartDir         string
}

type watch struct {
	Group   string `yaml:"group"`
	Version string `yaml:"version"`
	Kind    string `yaml:"kind"`
	Chart   string `yaml:"chart"`
}

// NewInstaller returns a new Helm installer capable of installing and uninstalling releases.
func NewInstaller(storageBackend *storage.Storage, tillerKubeClient *kube.Client, chartDir string) Installer {
	return installer{storageBackend, tillerKubeClient, chartDir}
}

// newInstallerFromEnv returns a GVK and installer based on configuration provided
// in the environment.
func newInstallerFromEnv(storageBackend *storage.Storage, tillerKubeClient *kube.Client) (schema.GroupVersionKind, Installer, error) {
	apiVersion := os.Getenv(APIVersionEnvVar)
	kind := os.Getenv(KindEnvVar)
	chartDir := os.Getenv(HelmChartEnvVar)

	var gvk schema.GroupVersionKind
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return gvk, nil, err
	}
	gvk = gv.WithKind(kind)

	if err := verifyGVK(gvk); err != nil {
		return gvk, nil, fmt.Errorf("invalid GVK: %s: %s", gvk, err)
	}

	if _, err := chartutil.IsChartDir(chartDir); err != nil {
		return gvk, nil, fmt.Errorf("invalid chart directory %s: %s", chartDir, err)
	}

	installer := NewInstaller(storageBackend, tillerKubeClient, chartDir)
	return gvk, installer, nil
}

// NewInstallersFromEnv returns a map of installers, keyed by GVK, based on
// configuration provided in the environment.
func NewInstallersFromEnv(storageBackend *storage.Storage, tillerKubeClient *kube.Client) (map[schema.GroupVersionKind]Installer, error) {
	if watchesFile, ok := getWatchesFile(); ok {
		return NewInstallersFromFile(storageBackend, tillerKubeClient, watchesFile)
	}
	gvk, installer, err := newInstallerFromEnv(storageBackend, tillerKubeClient)
	if err != nil {
		return nil, err
	}
	return map[schema.GroupVersionKind]Installer{gvk: installer}, nil
}

// NewInstallersFromFile reads the config file at the provided path and returns a map
// of installers, keyed by each GVK in the config.
func NewInstallersFromFile(storageBackend *storage.Storage, tillerKubeClient *kube.Client, path string) (map[schema.GroupVersionKind]Installer, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %s", err)
	}
	watches := []watch{}
	err = yaml.Unmarshal(b, &watches)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %s", err)
	}

	m := map[schema.GroupVersionKind]Installer{}
	for _, w := range watches {
		gvk := schema.GroupVersionKind{
			Group:   w.Group,
			Version: w.Version,
			Kind:    w.Kind,
		}

		if err := verifyGVK(gvk); err != nil {
			return nil, fmt.Errorf("invalid GVK: %s: %s", gvk, err)
		}

		if _, err := chartutil.IsChartDir(w.Chart); err != nil {
			return nil, fmt.Errorf("invalid chart directory %s: %s", w.Chart, err)
		}

		if _, ok := m[gvk]; ok {
			return nil, fmt.Errorf("duplicate GVK: %s", gvk)
		}
		m[gvk] = NewInstaller(storageBackend, tillerKubeClient, w.Chart)
	}
	return m, nil
}

func verifyGVK(gvk schema.GroupVersionKind) error {
	// A GVK without a group is valid. Certain scenarios may cause a GVK
	// without a group to fail in other ways later in the initialization
	// process.
	if gvk.Version == "" {
		return errors.New("version must not be empty")
	}
	if gvk.Kind == "" {
		return errors.New("kind must not be empty")
	}
	return nil
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

// ReconcileRelease accepts a custom resource, ensures the described release is deployed,
// and returns the custom resource with updated `status`.
// - If the custom resource does not have a release, a new release will be installed
// - If the custom resource has changes for an existing release, the release will be updated
// - If the custom resource has no changes for an existing release, the underlying resources will be reconciled.
func (c installer) ReconcileRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, bool, error) {
	needsUpdate := false

	// chart is mutated by the call to processRequirements,
	// so we need to reload it from disk every time.
	chart, err := chartutil.LoadDir(c.chartDir)
	if err != nil {
		return r, needsUpdate, fmt.Errorf("failed to load chart: %s", err)
	}

	cr, err := valuesFromResource(r)
	if err != nil {
		return r, needsUpdate, fmt.Errorf("failed to parse values: %s", err)
	}
	config := &cpb.Config{Raw: string(cr)}
	logrus.Debugf("Using values: %s", config.GetRaw())

	err = processRequirements(chart, config)
	if err != nil {
		return r, needsUpdate, fmt.Errorf("failed to process chart requirements: %s", err)
	}

	tiller := c.tillerRendererForCR(r)

	status := v1alpha1.StatusFor(r)
	if err := c.syncReleaseStatus(*status); err != nil {
		return r, needsUpdate, fmt.Errorf("failed to sync release status: %s", err)
	}

	// Get release history for this release name
	releases, err := c.storageBackend.History(releaseName(r))
	if err != nil && !notFoundErr(err) {
		return r, needsUpdate, fmt.Errorf("failed to retrieve release history: %s", err)
	}

	// Cleanup non-deployed release versions. If all release versions are
	// non-deployed, this will ensure that failed installations are correctly
	// retried.
	for _, rel := range releases {
		if rel.GetInfo().GetStatus().GetCode() != release.Status_DEPLOYED {
			_, err := c.storageBackend.Delete(rel.GetName(), rel.GetVersion())
			if err != nil && !notFoundErr(err) {
				return r, needsUpdate, fmt.Errorf("failed to delete stale release version: %s", err)
			}
		}
	}

	var updatedRelease *release.Release
	latestRelease, err := c.storageBackend.Deployed(releaseName(r))
	if err != nil || latestRelease == nil {
		updatedRelease, err = c.installRelease(r, tiller, chart, config)
		if err != nil {
			return r, needsUpdate, fmt.Errorf("install error: %s", err)
		}
		needsUpdate = true
		logrus.Infof("Installed release for %s release=%s", ResourceString(r), updatedRelease.GetName())
	} else {
		candidateRelease, err := c.getCandidateRelease(r, tiller, chart, config)
		if err != nil {
			return r, needsUpdate, fmt.Errorf("failed to generate candidate release: %s", err)
		}

		latestManifest := latestRelease.GetManifest()
		if latestManifest == candidateRelease.GetManifest() {
			err = c.reconcileRelease(r, latestManifest)
			if err != nil {
				return r, needsUpdate, fmt.Errorf("reconcile error: %s", err)
			}
			updatedRelease = latestRelease
			logrus.Infof("Reconciled release for %s release=%s", ResourceString(r), updatedRelease.GetName())
		} else {
			updatedRelease, err = c.updateRelease(r, tiller, chart, config)
			if err != nil {
				return r, needsUpdate, fmt.Errorf("update error: %s", err)
			}
			needsUpdate = true
			logrus.Infof("Updated release for %s release=%s", ResourceString(r), updatedRelease.GetName())
		}
	}

	status = v1alpha1.StatusFor(r)
	status.SetRelease(updatedRelease)
	// TODO(alecmerdler): Call `status.SetPhase()` with `NOTES.txt` of rendered Chart
	status.SetPhase(v1alpha1.PhaseApplied, v1alpha1.ReasonApplySuccessful, "")
	r.Object["status"] = status

	return r, needsUpdate, nil
}

// UninstallRelease accepts a custom resource, uninstalls the existing Helm release
// using Tiller, and returns the custom resource with updated `status`.
func (c installer) UninstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// Get history of this release
	h, err := c.storageBackend.History(releaseName(r))
	if err != nil {
		return r, fmt.Errorf("failed to get release history: %s", err)
	}

	// If there is no history, the release has already been uninstalled,
	// so there's nothing to do.
	if len(h) == 0 {
		return r, nil
	}

	tiller := c.tillerRendererForCR(r)
	_, err = tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
		Name:  releaseName(r),
		Purge: true,
	})
	if err != nil {
		return r, err
	}
	logrus.Infof("Uninstalled release for %s release=%s", ResourceString(r), releaseName(r))
	return r, nil
}

// ResourceString returns a human friendly string for the custom resource
func ResourceString(r *unstructured.Unstructured) string {
	return fmt.Sprintf("apiVersion=%s kind=%s name=%s/%s", r.GetAPIVersion(), r.GetKind(), r.GetNamespace(), r.GetName())
}

func (c installer) installRelease(r *unstructured.Unstructured, tiller *tiller.ReleaseServer, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	installReq := &services.InstallReleaseRequest{
		Namespace: r.GetNamespace(),
		Name:      releaseName(r),
		Chart:     chart,
		Values:    config,
	}

	releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
	if err != nil {
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

func (c installer) updateRelease(r *unstructured.Unstructured, tiller *tiller.ReleaseServer, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	updateReq := &services.UpdateReleaseRequest{
		Name:   releaseName(r),
		Chart:  chart,
		Values: config,
	}

	releaseResponse, err := tiller.UpdateRelease(context.TODO(), updateReq)
	if err != nil {
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

func (c installer) reconcileRelease(r *unstructured.Unstructured, expectedManifest string) error {
	expectedInfos, err := c.tillerKubeClient.BuildUnstructured(r.GetNamespace(), bytes.NewBufferString(expectedManifest))
	if err != nil {
		return err
	}
	return expectedInfos.Visit(func(expected *resource.Info, err error) error {
		if err != nil {
			return err
		}
		helper := resource.NewHelper(expected.Client, expected.Mapping)
		_, err = helper.Create(expected.Namespace, true, expected.Object)
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create error: %s", err)
		}

		patch, err := json.Marshal(expected.Object)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON patch: %s", err)
		}

		_, err = helper.Patch(expected.Namespace, expected.Name, types.MergePatchType, patch)
		if err != nil {
			return fmt.Errorf("patch error: %s", err)
		}
		return nil
	})
}

func (c installer) getCandidateRelease(r *unstructured.Unstructured, tiller *tiller.ReleaseServer, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	dryRunReq := &services.UpdateReleaseRequest{
		Name:   releaseName(r),
		Chart:  chart,
		Values: config,
		DryRun: true,
	}
	dryRunResponse, err := tiller.UpdateRelease(context.TODO(), dryRunReq)
	if err != nil {
		return nil, err
	}
	return dryRunResponse.GetRelease(), nil
}

func (c installer) syncReleaseStatus(status v1alpha1.HelmAppStatus) error {
	if status.Release == nil {
		return nil
	}

	name := status.Release.GetName()
	version := status.Release.GetVersion()
	_, err := c.storageBackend.Get(name, version)
	if err == nil {
		return nil
	}

	if !notFoundErr(err) {
		return err
	}
	return c.storageBackend.Create(status.Release)
}

// tillerRendererForCR creates a ReleaseServer configured with a rendering engine that adds ownerrefs to rendered assets
// based on the CR.
func (c installer) tillerRendererForCR(r *unstructured.Unstructured) *tiller.ReleaseServer {
	controllerRef := metav1.NewControllerRef(r, r.GroupVersionKind())
	ownerRefs := []metav1.OwnerReference{
		*controllerRef,
	}
	baseEngine := engine.New()
	e := NewOwnerRefEngine(baseEngine, ownerRefs)
	var ey environment.EngineYard = map[string]environment.Engine{
		environment.GoTplEngine: e,
	}
	env := &environment.Environment{
		EngineYard: ey,
		Releases:   c.storageBackend,
		KubeClient: c.tillerKubeClient,
	}
	kubeconfig, _ := c.tillerKubeClient.ToRESTConfig()
	internalClientSet, _ := internalclientset.NewForConfig(kubeconfig)

	return tiller.NewReleaseServer(env, internalClientSet, false)
}

func releaseName(r *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", operatorName, r.GetName())
}

func notFoundErr(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

func valuesFromResource(r *unstructured.Unstructured) ([]byte, error) {
	return yaml.Marshal(r.Object["spec"])
}

// processRequirements will process the requirements file
// It will disable/enable the charts based on condition in requirements file
// Also imports the specified chart values from child to parent.
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
