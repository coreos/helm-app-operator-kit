package controller

import (
	"context"
	"log"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// reconcileLoop emits source events on a specific interval for the defined GVK
type reconcileLoop struct {
	Source   chan event.GenericEvent
	Stop     <-chan struct{}
	GVK      schema.GroupVersionKind
	Interval time.Duration
	Client   client.Client
}

// newReconcileLoop creates a new loop for a GVK.
// The reconcilation loop is needed because the resync period
// for the informer is not suitable for this use case.
func newReconcileLoop(interval time.Duration, gvk schema.GroupVersionKind, c client.Client) reconcileLoop {
	s := make(chan event.GenericEvent, 1025)
	return reconcileLoop{
		Source:   s,
		GVK:      gvk,
		Interval: interval,
		Client:   c,
	}
}

// Start starts the reconcile loop
func (r *reconcileLoop) Start() {
	go func() {
		ticker := time.NewTicker(r.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// List all object for the GVK
				ul := &unstructured.UnstructuredList{}
				ul.SetGroupVersionKind(r.GVK)
				err := r.Client.List(context.Background(), nil, ul)
				if err != nil {
					log.Printf("unable to list resources for GV: %v during reconcilation", r.GVK)
					continue
				}
				for _, u := range ul.Items {
					e := event.GenericEvent{
						Meta:   &u,
						Object: &u,
					}
					r.Source <- e
				}
			case <-r.Stop:
				return
			}
		}
	}()
}
