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
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	crthandler "sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/release"
)

// WatchOptions contains the necessary values to create a new controller that
// manages helm releases in a particular namespace based on a GVK watch.
type WatchOptions struct {
	Namespace    string
	GVK          schema.GroupVersionKind
	Installer    release.Installer
	ResyncPeriod time.Duration
}

// Add creates a new helm operator controller and adds it to the manager
func Add(mgr manager.Manager, options WatchOptions) {
	if options.ResyncPeriod == 0 {
		options.ResyncPeriod = time.Minute
	}
	r := &helmOperatorReconciler{
		Client:       mgr.GetClient(),
		GVK:          options.GVK,
		Installer:    options.Installer,
		ResyncPeriod: options.ResyncPeriod,
	}

	// Register the GVK with the schema
	mgr.GetScheme().AddKnownTypeWithName(options.GVK, &unstructured.Unstructured{})
	metav1.AddToGroupVersion(mgr.GetScheme(), options.GVK.GroupVersion())

	controllerName := fmt.Sprintf("%v-controller", strings.ToLower(options.GVK.Kind))
	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		logrus.Fatal(err)
	}

	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(options.GVK)
	if err := c.Watch(&source.Kind{Type: o}, &crthandler.EnqueueRequestForObject{}); err != nil {
		logrus.Fatal(err)
	}

	logrus.Infof("Watching %s, %s, %s, %d", options.GVK.GroupVersion(), options.GVK.Kind, options.Namespace, options.ResyncPeriod)
}
