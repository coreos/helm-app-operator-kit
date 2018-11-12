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
	"errors"
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
	rpb "k8s.io/helm/pkg/proto/hapi/release"
	"k8s.io/helm/pkg/proto/hapi/services"
	"k8s.io/helm/pkg/storage"
	"k8s.io/helm/pkg/tiller"
	"k8s.io/helm/pkg/tiller/environment"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/engine"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/types"
)

var (
	// ErrNotFound indicates that a release could not be found
	ErrNotFound = errors.New("release not found")
)

// Manager can install and uninstall Helm releases given a custom resource
// which provides runtime values for the Chart.
type Manager interface {
	Sync() error
	GetReleaseName() string
	PrepareRelease(context.Context) error
	InstallRelease(context.Context) (*rpb.Release, error)
	UpdateRelease(context.Context) (*rpb.Release, *rpb.Release, error)
	ReconcileRelease(context.Context) (*rpb.Release, error)
	UninstallRelease(context.Context) (*rpb.Release, error)
	IsReleaseInstalled() bool
	IsUpdateRequired() bool
}

type manager struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chartDir         string
	tiller           *tiller.ReleaseServer

	namespace   string
	releaseName string

	spec   interface{}
	status *types.HelmAppStatus

	chart  *cpb.Chart
	config *cpb.Config

	isReleaseInstalled bool
	isUpdateRequired   bool
	deployedRelease    *rpb.Release
}

func newManagerForCR(storageBackend *storage.Storage, tillerKubeClient *kube.Client, chartDir string, u *unstructured.Unstructured) Manager {
	m := &manager{
		storageBackend:   storageBackend,
		tillerKubeClient: tillerKubeClient,
		chartDir:         chartDir,
		namespace:        u.GetNamespace(),
		releaseName:      releaseNameForCR(u),
		spec:             u.Object["spec"],
		status:           types.StatusFor(u),
	}
	m.tiller = m.tillerRendererForCR(u)
	return m
}

// Sync ensures that the resource status is synced with the tiller storage
// backend.
func (c manager) Sync() error {
	if c.status.Release != nil {
		name := c.status.Release.GetName()
		version := c.status.Release.GetVersion()
		_, err := c.storageBackend.Get(name, version)
		if err != nil {
			err = c.storageBackend.Create(c.status.Release)
			if err != nil {
				return err
			}
		}
	}

	// Get release history for this release name
	releases, err := c.storageBackend.History(c.releaseName)
	if err != nil && !notFoundErr(err) {
		return fmt.Errorf("failed to retrieve release history: %s", err)
	}
	// Cleanup non-deployed release versions. If all release versions are
	// non-deployed, this will ensure that failed installations are correctly
	// retried.
	for _, rel := range releases {
		if rel.GetInfo().GetStatus().GetCode() != rpb.Status_DEPLOYED {
			_, err := c.storageBackend.Delete(rel.GetName(), rel.GetVersion())
			if err != nil && !notFoundErr(err) {
				return fmt.Errorf("failed to delete stale release version: %s", err)
			}
		}
	}

	return nil
}

func notFoundErr(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

// GetReleaseName returns the release name for the release managed by this
// release manager.
func (c manager) GetReleaseName() string {
	return c.releaseName
}

// PrepareRelease loads the chart and config for the release and updates
// state that is used to determine what release steps should be executed.
func (c *manager) PrepareRelease(ctx context.Context) error {
	// Load the chart and config for this release.
	chart, config, err := c.loadChartAndConfig()
	if err != nil {
		return fmt.Errorf("failed to load chart and config: %s", err)
	}
	c.chart = chart
	c.config = config

	// Load the deployed release from the storage backend, if it exists.
	deployedRelease, err := c.getDeployedRelease()
	if err == ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to retrieve deployed release info: %s", err)
	}
	c.deployedRelease = deployedRelease
	c.isReleaseInstalled = true

	// If there is a deployed release, do a dry run update to see if we need to
	// update the release or just reconcile resources.
	dryRunReq := &services.UpdateReleaseRequest{
		Name:   c.releaseName,
		Chart:  c.chart,
		Values: c.config,
		DryRun: true,
	}
	dryRunResponse, err := c.tiller.UpdateRelease(ctx, dryRunReq)
	if err != nil {
		return fmt.Errorf("failed to execute dry run update: %s", err)
	}
	if c.deployedRelease.GetManifest() != dryRunResponse.GetRelease().GetManifest() {
		c.isUpdateRequired = true
	}

	return nil
}

// InstallRelease installs a new Helm release. If an installation error occurs,
// this method will attempt to uninstall the release and return the underlying
// error.
func (c manager) InstallRelease(ctx context.Context) (*rpb.Release, error) {
	installReq := &services.InstallReleaseRequest{
		Namespace: c.namespace,
		Name:      c.releaseName,
		Chart:     c.chart,
		Values:    c.config,
	}

	releaseResponse, err := c.tiller.InstallRelease(ctx, installReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			uninstallReq := &services.UninstallReleaseRequest{
				Name:  c.releaseName,
				Purge: true,
			}
			_, uninstallErr := c.tiller.UninstallRelease(ctx, uninstallReq)
			if uninstallErr != nil {
				return nil, fmt.Errorf("failed to roll back failed installation: %s: %s", uninstallErr, err)
			}
		}
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

// UpdateRelease updates an existing Helm release. If an update error occurs,
// this method will attempt to rollback the release and return the underlying
// error.
func (c manager) UpdateRelease(ctx context.Context) (*rpb.Release, *rpb.Release, error) {
	updateReq := &services.UpdateReleaseRequest{
		Name:   c.releaseName,
		Chart:  c.chart,
		Values: c.config,
	}

	releaseResponse, err := c.tiller.UpdateRelease(ctx, updateReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			rollbackReq := &services.RollbackReleaseRequest{
				Name:  c.releaseName,
				Force: true,
			}
			_, rollbackErr := c.tiller.RollbackRelease(ctx, rollbackReq)
			if rollbackErr != nil {
				return nil, nil, fmt.Errorf("failed to roll back failed update: %s: %s", rollbackErr, err)
			}
		}
		return nil, nil, err
	}
	return c.deployedRelease, releaseResponse.GetRelease(), nil
}

// ReconcileRelease reconciles the underlying resources of an existing Helm
// release. If an error occurs, it will be returned.
func (c manager) ReconcileRelease(ctx context.Context) (*rpb.Release, error) {
	expectedInfos, err := c.tillerKubeClient.BuildUnstructured(c.namespace, bytes.NewBufferString(c.deployedRelease.GetManifest()))
	if err != nil {
		return nil, err
	}
	err = expectedInfos.Visit(func(expected *resource.Info, err error) error {
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
	if err != nil {
		return nil, err
	}
	return c.deployedRelease, nil
}

// UninstallRelease uninstalls the Helm release based on the passed in object.
// If no release exists for the object, ErrNotFound will be returned. If an
// uninstall error occurs, it will be returned.
func (c manager) UninstallRelease(ctx context.Context) (*rpb.Release, error) {
	// Get history of this release
	h, err := c.storageBackend.History(c.releaseName)
	if err != nil {
		return nil, fmt.Errorf("failed to get release history: %s", err)
	}

	// If there is no history, return ErrNotFound.
	if len(h) == 0 {
		return nil, ErrNotFound
	}

	uninstallResponse, err := c.tiller.UninstallRelease(ctx, &services.UninstallReleaseRequest{
		Name:  c.releaseName,
		Purge: true,
	})
	return uninstallResponse.GetRelease(), err
}

// IsReleaseInstalled returns whether a release is installed. This method must
// be called only after PrepareRelease has been called.
func (c manager) IsReleaseInstalled() bool {
	return c.isReleaseInstalled
}

// IsUpdateRequired returns whether a release needs to be updated. This
// method must be called only after PrepareRelease has been called.
func (c manager) IsUpdateRequired() bool {
	return c.isUpdateRequired
}

func (c manager) loadChartAndConfig() (*cpb.Chart, *cpb.Config, error) {
	// chart is mutated by the call to processRequirements,
	// so we need to reload it from disk every time.
	chart, err := chartutil.LoadDir(c.chartDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load chart: %s", err)
	}

	cr, err := yaml.Marshal(c.spec)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse values: %s", err)
	}
	config := &cpb.Config{Raw: string(cr)}
	logrus.Debug("Using values: %s", config.GetRaw())

	err = processRequirements(chart, config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to process chart requirements: %s", err)
	}
	return chart, config, nil
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

func (c manager) getDeployedRelease() (*rpb.Release, error) {
	deployedRelease, err := c.storageBackend.Deployed(c.releaseName)
	if err != nil {
		if strings.Contains(err.Error(), "has no deployed releases") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return deployedRelease, nil
}

// tillerRendererForCR creates a ReleaseServer configured with a rendering engine that adds ownerrefs to rendered assets
// based on the CR.
func (c manager) tillerRendererForCR(r *unstructured.Unstructured) *tiller.ReleaseServer {
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

func releaseNameForCR(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s-%s", u.GetName(), shortenUID(u.GetUID()))
}

func shortenUID(uid apitypes.UID) string {
	u := uuid.Parse(string(uid))
	uidBytes, err := u.MarshalBinary()
	if err != nil {
		return strings.Replace(string(uid), "-", "", -1)
	}
	return strings.ToLower(base36.EncodeBytes(uidBytes))
}
