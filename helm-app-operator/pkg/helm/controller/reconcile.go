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

package controller

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/types"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/util"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/release"
)

var _ reconcile.Reconciler = &HelmOperatorReconciler{}

// HelmOperatorReconciler reconciles custom resources as Helm releases.
type HelmOperatorReconciler struct {
	Client         client.Client
	GVK            schema.GroupVersionKind
	ManagerFactory release.ManagerFactory
	ResyncPeriod   time.Duration
}

const (
	finalizer = "uninstall-helm-release"
)

// Reconcile reconciles the requested resource by installing, updating, or
// uninstalling a Helm release based on the resource's current state. If no
// release changes are necessary, Reconcile will create or patch the underlying
// resources to match the expected release manifest.
func (r HelmOperatorReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(r.GVK)
	o.SetNamespace(request.Namespace)
	o.SetName(request.Name)
	logrus.Debugf("Processing %s", util.ResourceString(o))

	err := r.Client.Get(context.TODO(), request.NamespacedName, o)
	if apierrors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		logrus.Errorf("failed to lookup %s: %s", util.ResourceString(o), err)
		return reconcile.Result{}, err
	}

	manager := r.ManagerFactory.NewManager(o)
	status := types.StatusFor(o)
	releaseName := manager.ReleaseName()

	deleted := o.GetDeletionTimestamp() != nil
	pendingFinalizers := o.GetFinalizers()
	if !deleted && !contains(pendingFinalizers, finalizer) {
		logrus.Debugf("Adding finalizer \"%s\" to %s", finalizer, util.ResourceString(o))
		finalizers := append(pendingFinalizers, finalizer)
		o.SetFinalizers(finalizers)
		if len(status.Conditions) == 0 {
			status.SetCondition(types.HelmAppCondition{
				Type:   types.ConditionInitializing,
				Status: types.StatusTrue,
			})
		}
		err := r.Client.Update(context.TODO(), o)
		return reconcile.Result{}, err
	}
	status.RemoveCondition(types.ConditionInitializing)

	if err := manager.Sync(context.TODO()); err != nil {
		logrus.Errorf("failed to sync release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
		status.SetCondition(types.HelmAppCondition{
			Type:    types.ConditionIrreconcilable,
			Status:  types.StatusTrue,
			Reason:  types.ReasonReconcileError,
			Message: err.Error(),
		})
		_ = r.updateResourceStatus(o, status)
		return reconcile.Result{}, err
	}
	status.RemoveCondition(types.ConditionIrreconcilable)

	if deleted {
		if !contains(pendingFinalizers, finalizer) {
			logrus.Infof("Resource %s is terminated, skipping reconciliation", util.ResourceString(o))
			return reconcile.Result{}, nil
		}

		uninstalledRelease, err := manager.UninstallRelease(context.TODO())
		if err != nil && err != release.ErrNotFound {
			status.SetCondition(types.HelmAppCondition{
				Type:    types.ConditionReleaseFailed,
				Status:  types.StatusTrue,
				Reason:  types.ReasonUninstallError,
				Message: err.Error(),
			})
			_ = r.updateResourceStatus(o, status)
			return reconcile.Result{}, err
		}
		status.RemoveCondition(types.ConditionReleaseFailed)

		if err == release.ErrNotFound {
			logrus.Infof("Release %s for resource %s not found, removing finalizer", releaseName, util.ResourceString(o))
		} else {
			diff := util.Diff(uninstalledRelease.GetManifest(), "")
			logrus.Infof("Uninstalled release for %s release=%s; diff:\n%s", util.ResourceString(o), releaseName, diff)
			status.SetCondition(types.HelmAppCondition{
				Type:   types.ConditionDeployed,
				Status: types.StatusFalse,
				Reason: types.ReasonUninstallSuccessful,
			})
		}
		finalizers := []string{}
		for _, pendingFinalizer := range pendingFinalizers {
			if pendingFinalizer != finalizer {
				finalizers = append(finalizers, pendingFinalizer)
			}
		}
		o.SetFinalizers(finalizers)
		err = r.Client.Update(context.TODO(), o)
		return reconcile.Result{}, err
	}

	if !manager.IsInstalled() {
		installedRelease, err := manager.InstallRelease(context.TODO())
		if err != nil {
			logrus.Errorf("failed to install release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
			status.SetCondition(types.HelmAppCondition{
				Type:    types.ConditionReleaseFailed,
				Status:  types.StatusTrue,
				Reason:  types.ReasonInstallError,
				Message: err.Error(),
				Release: installedRelease,
			})
			_ = r.updateResourceStatus(o, status)
			return reconcile.Result{}, err
		}
		status.RemoveCondition(types.ConditionReleaseFailed)

		diff := util.Diff("", installedRelease.GetManifest())
		logrus.Infof("Installed release for %s release=%s; diff:\n%s", util.ResourceString(o), releaseName, diff)
		status.SetCondition(types.HelmAppCondition{
			Type:    types.ConditionDeployed,
			Status:  types.StatusTrue,
			Reason:  types.ReasonInstallSuccessful,
			Message: installedRelease.GetInfo().GetStatus().GetNotes(),
			Release: installedRelease,
		})
		err = r.updateResourceStatus(o, status)
		return reconcile.Result{RequeueAfter: r.ResyncPeriod}, err
	}

	if manager.IsUpdateRequired() {
		previousRelease, updatedRelease, err := manager.UpdateRelease(context.TODO())
		if err != nil {
			logrus.Errorf("failed to update release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
			status.SetCondition(types.HelmAppCondition{
				Type:    types.ConditionReleaseFailed,
				Status:  types.StatusTrue,
				Reason:  types.ReasonUpdateError,
				Message: err.Error(),
				Release: updatedRelease,
			})
			_ = r.updateResourceStatus(o, status)
			return reconcile.Result{}, err
		}
		status.RemoveCondition(types.ConditionReleaseFailed)

		diff := util.Diff(previousRelease.GetManifest(), updatedRelease.GetManifest())
		logrus.Infof("Updated release for %s release=%s; diff:\n%s", util.ResourceString(o), releaseName, diff)
		status.SetCondition(types.HelmAppCondition{
			Type:    types.ConditionDeployed,
			Status:  types.StatusTrue,
			Reason:  types.ReasonUpdateSuccessful,
			Message: updatedRelease.GetInfo().GetStatus().GetNotes(),
			Release: updatedRelease,
		})
		err = r.updateResourceStatus(o, status)
		return reconcile.Result{RequeueAfter: r.ResyncPeriod}, err
	}

	_, err = manager.ReconcileRelease(context.TODO())
	if err != nil {
		logrus.Errorf("failed to reconcile release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
		status.SetCondition(types.HelmAppCondition{
			Type:    types.ConditionIrreconcilable,
			Status:  types.StatusTrue,
			Reason:  types.ReasonReconcileError,
			Message: err.Error(),
		})
		_ = r.updateResourceStatus(o, status)
		return reconcile.Result{}, err
	}
	status.RemoveCondition(types.ConditionIrreconcilable)

	logrus.Infof("Reconciled release for %s release=%s", util.ResourceString(o), releaseName)
	err = r.updateResourceStatus(o, status)
	return reconcile.Result{RequeueAfter: r.ResyncPeriod}, err
}

func (r HelmOperatorReconciler) updateResourceStatus(o *unstructured.Unstructured, status *types.HelmAppStatus) error {
	o.Object["status"] = status
	return r.Client.Update(context.TODO(), o)
}

func contains(l []string, s string) bool {
	for _, elem := range l {
		if elem == s {
			return true
		}
	}
	return false
}
