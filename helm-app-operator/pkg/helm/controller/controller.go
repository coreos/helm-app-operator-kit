package controller

import (
	"fmt"
	"log"
	"strings"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/installer"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/controller"
	crthandler "sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// WatchOptions contains the necessary values to create a new controller that
// manages helm releases in a particular namespace based on a GVK watch.
type WatchOptions struct {
	Installer installer.Installer
	Namespace string
	GVK       schema.GroupVersionKind
	// StopChannel is used to deal with the bug:
	// https://github.com/kubernetes-sigs/controller-runtime/issues/103
	StopChannel <-chan struct{}
}

// Add creates a new helm operator controller and adds it to the manager
func Add(mgr manager.Manager, options WatchOptions) {
	hor := &helmOperatorReconciler{
		Client:    mgr.GetClient(),
		GVK:       options.GVK,
		Installer: options.Installer,
	}

	// Register the GVK with the schema
	mgr.GetScheme().AddKnownTypeWithName(options.GVK, &unstructured.Unstructured{})
	metav1.AddToGroupVersion(mgr.GetScheme(), options.GVK.GroupVersion())

	// Create new controller-runtime controller and set the controller to watch this GVK.
	c, err := controller.New(fmt.Sprintf("%v-controller", strings.ToLower(options.GVK.Kind)), mgr, controller.Options{
		Reconciler: hor,
	})
	if err != nil {
		log.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(options.GVK)
	if err := c.Watch(&source.Kind{Type: u}, &crthandler.EnqueueRequestForObject{}); err != nil {
		log.Fatal(err)
	}

	log.Printf("Watching %s, %s", options.GVK, options.Namespace)
}
