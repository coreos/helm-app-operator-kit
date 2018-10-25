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

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm"
	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/internal/util"
)

type helmOperatorReconciler struct {
	Client       client.Client
	GVK          schema.GroupVersionKind
	Installer    helm.Installer
	ResyncPeriod time.Duration
}

const (
	finalizer = "uninstall-helm-release"
)

func (r *helmOperatorReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
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

	deleted := o.GetDeletionTimestamp() != nil
	pendingFinalizers := o.GetFinalizers()
	if !deleted && !contains(pendingFinalizers, finalizer) {
		logrus.Debugf("Adding finalizer \"%s\" to %s", finalizer, util.ResourceString(o))
		finalizers := append(pendingFinalizers, finalizer)
		o.SetFinalizers(finalizers)
		err := r.Client.Update(context.TODO(), o)
		return reconcile.Result{}, err
	}
	if deleted {
		if !contains(pendingFinalizers, finalizer) {
			logrus.Infof("Resource %s is terminated, skipping reconciliation", util.ResourceString(o))
			return reconcile.Result{}, nil
		}

		_, err = r.Installer.UninstallRelease(o)
		if err != nil {
			return reconcile.Result{}, err
		}

		finalizers := []string{}
		for _, pendingFinalizer := range pendingFinalizers {
			if pendingFinalizer != finalizer {
				finalizers = append(finalizers, pendingFinalizer)
			}
		}
		o.SetFinalizers(finalizers)
		err := r.Client.Update(context.TODO(), o)
		return reconcile.Result{}, err
	}

	updatedResource, needsUpdate, err := r.Installer.ReconcileRelease(o)
	if err != nil {
		logrus.Errorf("failed to reconcile release for %s: %s", util.ResourceString(o), err)
		return reconcile.Result{}, err
	}

	if needsUpdate {
		err = r.Client.Update(context.TODO(), updatedResource)
		if err != nil {
			logrus.Errorf("failed to update resource status for %s: %s", util.ResourceString(o), err)
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{RequeueAfter: r.ResyncPeriod}, nil
}

func contains(l []string, s string) bool {
	for _, elem := range l {
		if elem == s {
			return true
		}
	}
	return false
}
