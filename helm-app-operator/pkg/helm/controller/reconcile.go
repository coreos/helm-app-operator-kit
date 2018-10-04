package controller

import (
	"context"
	"log"

	"github.com/operator-framework/helm-app-operator-kit/helm-app-operator/pkg/helm/installer"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type helmOperatorReconciler struct {
	GVK       schema.GroupVersionKind
	Installer installer.Installer
	Client    client.Client
}

// assert interface
var _ reconcile.Reconciler = &helmOperatorReconciler{}

// Reconcile handles events by installing, updating, or uninstalling the
// associated helm releases.
func (r *helmOperatorReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(r.GVK)
	u.SetNamespace(request.Namespace)
	u.SetName(request.Name)

	err := r.Client.Get(context.TODO(), request.NamespacedName, u)
	if err != nil {
		// If the CR is not found, it must have just been deleted. Uninstall it.
		if apierrors.IsNotFound(err) {
			u, err = r.Installer.UninstallRelease(u)
			if err != nil {
				log.Print(err)
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Printf("Failed to get %s: %v", r.GVK.Kind, err)
		return reconcile.Result{}, err
	}

	// If the CR doesn't have a spec, add an empty spec and requeue it.
	s := u.Object["spec"]
	_, ok := s.(map[string]interface{})
	if !ok {
		u.Object["spec"] = map[string]interface{}{}
		r.Client.Update(context.TODO(), u)
		return reconcile.Result{Requeue: true}, nil
	}

	// Install the release
	u, err = r.Installer.InstallRelease(u)
	if err != nil {
		log.Print(err)
		return reconcile.Result{}, err
	}

	// Update the CR with the updated status.
	r.Client.Update(context.TODO(), u)

	return reconcile.Result{}, nil
}
