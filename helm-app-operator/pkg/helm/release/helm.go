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

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/martinlindhe/base36"
	"github.com/pborman/uuid"
	"github.com/sirupsen/logrus"

	yaml "gopkg.in/yaml.v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/helm/pkg/chartutil"
	helmengine "k8s.io/helm/pkg/engine"
	"k8s.io/helm/pkg/kube"
	cpb "k8s.io/helm/pkg/proto/hapi/chart"
	"k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/tiller"
	"k8s.io/helm/pkg/tiller/environment"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/engine"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/types"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/util"
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

	status := types.StatusFor(r)
	if err := c.syncReleaseStatus(*status); err != nil {
		return r, needsUpdate, fmt.Errorf("failed to sync release status: %s", err)
	}

	releaseName := getReleaseName(r)

	// Get release history for this release name
	releases, err := c.storageBackend.History(releaseName)
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
	latestRelease, err := c.storageBackend.Deployed(releaseName)
	if err != nil || latestRelease == nil {
		// If there's no deployed release, attempt a tiller install.
		updatedRelease, err = c.installRelease(tiller, r.GetNamespace(), releaseName, chart, config)
		if err != nil {
			return r, needsUpdate, fmt.Errorf("install error: %s", err)
		}
		needsUpdate = true
		diffStr := util.Diff("", updatedRelease.GetManifest())
		logrus.Infof("Installed release for %s release=%s; diff:\n%s", util.ResourceString(r), updatedRelease.GetName(), diffStr)
	} else {
		candidateRelease, err := c.getCandidateRelease(tiller, releaseName, chart, config)
		if err != nil {
			return r, needsUpdate, fmt.Errorf("failed to generate candidate release: %s", err)
		}

		latestManifest := latestRelease.GetManifest()
		if latestManifest == candidateRelease.GetManifest() {
			err = c.reconcileRelease(r.GetNamespace(), latestManifest)
			if err != nil {
				return r, needsUpdate, fmt.Errorf("reconcile error: %s", err)
			}
			updatedRelease = latestRelease
			logrus.Infof("Reconciled release for %s release=%s", util.ResourceString(r), updatedRelease.GetName())
		} else {
			updatedRelease, err = c.updateRelease(tiller, releaseName, chart, config)
			if err != nil {
				return r, needsUpdate, fmt.Errorf("update error: %s", err)
			}
			needsUpdate = true
			diffStr := util.Diff(latestManifest, updatedRelease.GetManifest())
			logrus.Infof("Updated release for %s release=%s; diff:\n%s", util.ResourceString(r), updatedRelease.GetName(), diffStr)
		}
	}

	status = types.StatusFor(r)
	status.SetRelease(updatedRelease)
	// TODO(alecmerdler): Call `status.SetPhase()` with `NOTES.txt` of rendered Chart
	status.SetPhase(types.PhaseApplied, types.ReasonApplySuccessful, "")
	r.Object["status"] = status

	return r, needsUpdate, nil
}

// UninstallRelease accepts a custom resource, uninstalls the existing Helm release
// using Tiller, and returns the custom resource with updated `status`.
func (c installer) UninstallRelease(r *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	releaseName := getReleaseName(r)

	// Get history of this release
	h, err := c.storageBackend.History(releaseName)
	if err != nil {
		return r, fmt.Errorf("failed to get release history: %s", err)
	}

	// If there is no history, the release has already been uninstalled,
	// so there's nothing to do.
	if len(h) == 0 {
		return r, nil
	}

	tiller := c.tillerRendererForCR(r)
	uninstallResponse, err := tiller.UninstallRelease(context.TODO(), &services.UninstallReleaseRequest{
		Name:  releaseName,
		Purge: true,
	})
	if err != nil {
		return r, err
	}
	diffStr := util.Diff(uninstallResponse.GetRelease().GetManifest(), "")
	logrus.Infof("Uninstalled release for %s release=%s; diff:\n%s", util.ResourceString(r), releaseName, diffStr)
	return r, nil
}

func (c installer) installRelease(tiller *tiller.ReleaseServer, namespace, name string, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	installReq := &services.InstallReleaseRequest{
		Namespace: namespace,
		Name:      name,
		Chart:     chart,
		Values:    config,
	}

	releaseResponse, err := tiller.InstallRelease(context.TODO(), installReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			uninstallReq := &services.UninstallReleaseRequest{
				Name:  releaseResponse.GetRelease().GetName(),
				Purge: true,
			}
			_, uninstallErr := tiller.UninstallRelease(context.TODO(), uninstallReq)
			if uninstallErr != nil {
				return nil, fmt.Errorf("failed to roll back failed installation: %s: %s", uninstallErr, err)
			}
		}
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

func (c installer) updateRelease(tiller *tiller.ReleaseServer, name string, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	updateReq := &services.UpdateReleaseRequest{
		Name:   name,
		Chart:  chart,
		Values: config,
	}

	releaseResponse, err := tiller.UpdateRelease(context.TODO(), updateReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			rollbackReq := &services.RollbackReleaseRequest{
				Name:  name,
				Force: true,
			}
			_, rollbackErr := tiller.RollbackRelease(context.TODO(), rollbackReq)
			if rollbackErr != nil {
				return nil, fmt.Errorf("failed to roll back failed update: %s: %s", rollbackErr, err)
			}
		}
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

func (c installer) reconcileRelease(namespace string, expectedManifest string) error {
	expectedInfos, err := c.tillerKubeClient.BuildUnstructured(namespace, bytes.NewBufferString(expectedManifest))
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

		_, err = helper.Patch(expected.Namespace, expected.Name, apitypes.MergePatchType, patch)
		if err != nil {
			return fmt.Errorf("patch error: %s", err)
		}
		return nil
	})
}

func (c installer) getCandidateRelease(tiller *tiller.ReleaseServer, name string, chart *cpb.Chart, config *cpb.Config) (*release.Release, error) {
	dryRunReq := &services.UpdateReleaseRequest{
		Name:   name,
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

func (c installer) syncReleaseStatus(status types.HelmAppStatus) error {
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
	baseEngine := helmengine.New()
	e := engine.NewOwnerRefEngine(baseEngine, ownerRefs)
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

func getReleaseName(r *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", r.GetName(), shortenUID(r.GetUID()))
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

func shortenUID(uid apitypes.UID) (shortUID string) {
	u := uuid.Parse(string(uid))
	uidBytes, err := u.MarshalBinary()
	if err != nil {
		shortUID = strings.Replace(string(uid), "-", "", -1)
	}
	shortUID = strings.ToLower(base36.EncodeBytes(uidBytes))
	return
}
