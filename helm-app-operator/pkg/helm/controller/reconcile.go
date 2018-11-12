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

type HelmOperatorReconciler struct {
	Client         client.Client
	GVK            schema.GroupVersionKind
	ManagerFactory release.ManagerFactory
	ResyncPeriod   time.Duration
}

const (
	finalizer = "uninstall-helm-release"
)

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
	releaseName := manager.GetReleaseName()

	deleted := o.GetDeletionTimestamp() != nil
	pendingFinalizers := o.GetFinalizers()
	if !deleted && !contains(pendingFinalizers, finalizer) {
		logrus.Debugf("Adding finalizer \"%s\" to %s", finalizer, util.ResourceString(o))
		finalizers := append(pendingFinalizers, finalizer)
		o.SetFinalizers(finalizers)
		err := r.updateResource(o, status)
		return reconcile.Result{}, err
	}

	if err := manager.Sync(); err != nil {
		logrus.Errorf("failed to sync info for %s release=%s: %s", util.ResourceString(o), releaseName, err)
		status.SetPhase(types.PhaseFailed, types.ReasonApplyFailed, err.Error())
		_ = r.updateResource(o, status)
		return reconcile.Result{}, err
	}

	if deleted {
		if !contains(pendingFinalizers, finalizer) {
			logrus.Infof("Skipping uninstall for %s release=%s: finalizer not found", util.ResourceString(o), releaseName)
			return reconcile.Result{}, nil
		}

		uninstalledRelease, err := manager.UninstallRelease(context.TODO())
		if err != nil {
			if err == release.ErrNotFound {
				logrus.Infof("Skipping uninstall for %s release=%s: %s", util.ResourceString(o), releaseName, err)
				return reconcile.Result{}, nil
			}
			logrus.Errorf("failed to uninstall release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
			status.SetPhase(types.PhaseFailed, types.ReasonApplyFailed, err.Error())
			_ = r.updateResource(o, status)
			return reconcile.Result{}, err
		}

		finalizers := []string{}
		for _, pendingFinalizer := range pendingFinalizers {
			if pendingFinalizer != finalizer {
				finalizers = append(finalizers, pendingFinalizer)
			}
		}
		o.SetFinalizers(finalizers)
		diffStr := util.Diff(uninstalledRelease.GetManifest(), "")
		logrus.Infof("Uninstalled release for %s release=%s; diff:\n%s", util.ResourceString(o), releaseName, diffStr)
		status.SetPhase(types.PhaseNone, types.ReasonApplySuccessful, "")
		status.SetRelease(nil)
		err = r.updateResource(o, status)
		return reconcile.Result{}, err
	}

	err = manager.PrepareRelease(context.TODO())
	if err != nil {
		logrus.Errorf("failed to prepare release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
		status.SetPhase(types.PhaseFailed, types.ReasonApplyFailed, err.Error())
		_ = r.updateResource(o, status)
		return reconcile.Result{}, err
	}

	if !manager.IsReleaseInstalled() {
		installedRelease, err := manager.InstallRelease(context.TODO())
		if err != nil {
			logrus.Errorf("failed to install release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
			status.SetPhase(types.PhaseFailed, types.ReasonApplyFailed, err.Error())
			_ = r.updateResource(o, status)
			return reconcile.Result{}, err
		}
		diffStr := util.Diff("", installedRelease.GetManifest())
		logrus.Infof("Installed release for %s release=%s; diff:\n%s", util.ResourceString(o), releaseName, diffStr)
		status.SetPhase(types.PhaseApplied, types.ReasonApplySuccessful, installedRelease.GetInfo().GetStatus().GetNotes())
		status.SetRelease(installedRelease)
		err = r.updateResource(o, status)
		return reconcile.Result{RequeueAfter: r.ResyncPeriod}, err
	}

	if manager.IsUpdateRequired() {
		previousRelease, updatedRelease, err := manager.UpdateRelease(context.TODO())
		if err != nil {
			logrus.Errorf("failed to update release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
			status.SetPhase(types.PhaseFailed, types.ReasonApplyFailed, err.Error())
			_ = r.updateResource(o, status)
			return reconcile.Result{}, err
		}
		diffStr := util.Diff(previousRelease.GetManifest(), updatedRelease.GetManifest())
		logrus.Infof("Updated release for %s release=%s; diff:\n%s", util.ResourceString(o), releaseName, diffStr)
		status.SetPhase(types.PhaseApplied, types.ReasonApplySuccessful, updatedRelease.GetInfo().GetStatus().GetNotes())
		status.SetRelease(updatedRelease)
		err = r.updateResource(o, status)
		return reconcile.Result{RequeueAfter: r.ResyncPeriod}, err
	}

	_, err = manager.ReconcileRelease(context.TODO())
	if err != nil {
		logrus.Errorf("failed to reconcile release for %s release=%s: %s", util.ResourceString(o), releaseName, err)
		status.SetPhase(types.PhaseFailed, types.ReasonApplyFailed, err.Error())
		_ = r.updateResource(o, status)
		return reconcile.Result{}, err
	}

	logrus.Infof("Reconciled release for %s release=%s", util.ResourceString(o), releaseName)
	return reconcile.Result{RequeueAfter: r.ResyncPeriod}, nil
}

func (r HelmOperatorReconciler) updateResource(o *unstructured.Unstructured, status *types.HelmAppStatus) error {
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
