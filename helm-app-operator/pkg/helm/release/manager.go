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
	ErrNotFound = errors.New("release not found")
)

// Manager can install and uninstall Helm releases given a custom resource
// which provides runtime values for the Chart.
type Manager interface {
	Sync(*unstructured.Unstructured) error
	IsReleaseInstalled(*unstructured.Unstructured) (bool, error)
	IsUpdateRequired(context.Context, *unstructured.Unstructured) (bool, error)
	InstallRelease(context.Context, *unstructured.Unstructured) (*rpb.Release, error)
	UpdateRelease(context.Context, *unstructured.Unstructured) (*rpb.Release, *rpb.Release, error)
	ReconcileRelease(context.Context, *unstructured.Unstructured) (*rpb.Release, error)
	UninstallRelease(context.Context, *unstructured.Unstructured) (*rpb.Release, error)
}

type manager struct {
	storageBackend   *storage.Storage
	tillerKubeClient *kube.Client
	chartDir         string
}

type info struct {
	tiller          *tiller.ReleaseServer
	namespace       string
	releaseName     string
	deployedRelease *rpb.Release
	chart           *cpb.Chart
	config          *cpb.Config
}

// Sync accepts a custom resource, and syncs its status with the manager's
// storage backend.
func (c manager) Sync(r *unstructured.Unstructured) error {
	status := types.StatusFor(r)
	if status.Release != nil {
		name := status.Release.GetName()
		version := status.Release.GetVersion()
		_, err := c.storageBackend.Get(name, version)
		if err != nil {
			err = c.storageBackend.Create(status.Release)
			if err != nil {
				return err
			}
		}
	}

	// Get release history for this release name
	releaseName := GetReleaseName(r)
	releases, err := c.storageBackend.History(releaseName)
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

// IsReleaseInstalled returns whether a release is deployed. If the release is
// not found, it returns ErrNotFound. If an error occurs fetching the release
// information from the manager's storage backend, the underlying error is
// returned.
func (c manager) IsReleaseInstalled(r *unstructured.Unstructured) (bool, error) {
	releaseName := GetReleaseName(r)
	_, err := c.getDeployedRelease(releaseName)
	if err == ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// IsUpdateRequired executes a dry run update and returns whether the dry run
// output is different than the currently deployed release. If an error occurs
// getting release status or executing the dry run update, the underlying error
// is returned.
func (c manager) IsUpdateRequired(ctx context.Context, r *unstructured.Unstructured) (bool, error) {
	info, err := c.infoForCR(r)
	if err != nil {
		return false, err
	}
	dryRunReq := &services.UpdateReleaseRequest{
		Name:   info.releaseName,
		Chart:  info.chart,
		Values: info.config,
		DryRun: true,
	}
	dryRunResponse, err := info.tiller.UpdateRelease(ctx, dryRunReq)
	if err != nil {
		return false, err
	}
	return info.deployedRelease.GetManifest() != dryRunResponse.GetRelease().GetManifest(), nil
}

// InstallRelease installs a new Helm release based on the passed in object.
// If an installation error occurs, this method will attempt to uninstall
// the release and return the underlying error.
func (c manager) InstallRelease(ctx context.Context, r *unstructured.Unstructured) (*rpb.Release, error) {
	info, err := c.infoForCR(r)
	if err != nil {
		return nil, err
	}

	installReq := &services.InstallReleaseRequest{
		Namespace: info.namespace,
		Name:      info.releaseName,
		Chart:     info.chart,
		Values:    info.config,
	}

	releaseResponse, err := info.tiller.InstallRelease(ctx, installReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			uninstallReq := &services.UninstallReleaseRequest{
				Name:  info.releaseName,
				Purge: true,
			}
			_, uninstallErr := info.tiller.UninstallRelease(ctx, uninstallReq)
			if uninstallErr != nil {
				return nil, fmt.Errorf("failed to roll back failed installation: %s: %s", uninstallErr, err)
			}
		}
		return nil, err
	}
	return releaseResponse.GetRelease(), nil
}

// UpdateRelease updates an existing Helm release based on the passed in
// object. If an update error occurs, this method will attempt to rollback
// the release and return the underlying error.
func (c manager) UpdateRelease(ctx context.Context, r *unstructured.Unstructured) (*rpb.Release, *rpb.Release, error) {
	info, err := c.infoForCR(r)
	if err != nil {
		return nil, nil, err
	}

	updateReq := &services.UpdateReleaseRequest{
		Name:   info.releaseName,
		Chart:  info.chart,
		Values: info.config,
	}

	releaseResponse, err := info.tiller.UpdateRelease(ctx, updateReq)
	if err != nil {
		// Workaround for helm/helm#3338
		if releaseResponse.GetRelease() != nil {
			rollbackReq := &services.RollbackReleaseRequest{
				Name:  info.releaseName,
				Force: true,
			}
			_, rollbackErr := info.tiller.RollbackRelease(ctx, rollbackReq)
			if rollbackErr != nil {
				return nil, nil, fmt.Errorf("failed to roll back failed update: %s: %s", rollbackErr, err)
			}
		}
		return nil, nil, err
	}
	return info.deployedRelease, releaseResponse.GetRelease(), nil
}

// ReconcileRelease reconciles the underlying resources of an existing Helm
// release based on the passed in object. If an error occurs, it will be
// returned.
func (c manager) ReconcileRelease(ctx context.Context, r *unstructured.Unstructured) (*rpb.Release, error) {
	info, err := c.infoForCR(r)
	if err != nil {
		return nil, err
	}

	expectedInfos, err := c.tillerKubeClient.BuildUnstructured(info.namespace, bytes.NewBufferString(info.deployedRelease.GetManifest()))
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
	return info.deployedRelease, nil
}

// UninstallRelease uninstalls the Helm release based on the passed in object.
// If no release exists for the object, ErrNotFound will be returned. If an
// uninstall error occurs, it will be returned.
func (c manager) UninstallRelease(ctx context.Context, r *unstructured.Unstructured) (*rpb.Release, error) {
	releaseName := GetReleaseName(r)

	// Get history of this release
	h, err := c.storageBackend.History(releaseName)
	if err != nil {
		return nil, fmt.Errorf("failed to get release history: %s", err)
	}

	// If there is no history, return ErrNotFound.
	if len(h) == 0 {
		return nil, ErrNotFound
	}

	tiller := c.tillerRendererForCR(r)
	uninstallResponse, err := tiller.UninstallRelease(ctx, &services.UninstallReleaseRequest{
		Name:  releaseName,
		Purge: true,
	})
	return uninstallResponse.GetRelease(), err
}

func (c manager) infoForCR(r *unstructured.Unstructured) (*info, error) {
	chart, config, err := c.chartAndConfigForCR(r)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart and config: %s", err)
	}

	releaseName := GetReleaseName(r)
	deployedRelease, err := c.getDeployedRelease(releaseName)
	if err != nil && err != ErrNotFound {
		return nil, fmt.Errorf("failed to retrieve deployed release info: %s", err)
	}

	return &info{
		tiller:          c.tillerRendererForCR(r),
		namespace:       r.GetNamespace(),
		releaseName:     releaseName,
		deployedRelease: deployedRelease,
		chart:           chart,
		config:          config,
	}, nil
}

func (c manager) chartAndConfigForCR(r *unstructured.Unstructured) (*cpb.Chart, *cpb.Config, error) {
	// chart is mutated by the call to processRequirements,
	// so we need to reload it from disk every time.
	chart, err := chartutil.LoadDir(c.chartDir)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load chart: %s", err)
	}

	cr, err := valuesFromResource(r)
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

func (c manager) getDeployedRelease(releaseName string) (*rpb.Release, error) {
	deployedRelease, err := c.storageBackend.Deployed(releaseName)
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
