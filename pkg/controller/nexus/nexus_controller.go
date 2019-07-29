package nexus

import (
	"context"
	"fmt"
	"nexus-operator/pkg/service"
	"time"

	edpv1alpha1 "nexus-operator/pkg/apis/edp/v1alpha1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_nexus")

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new Nexus Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	scheme := mgr.GetScheme()
	client := mgr.GetClient()
	platformService, _ := service.NewPlatformService(scheme)

	nexusService := service.NewNexusService(platformService, client)
	return &ReconcileNexus{
		client:  client,
		scheme:  scheme,
		service: nexusService,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("nexus-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Nexus
	err = c.Watch(&source.Kind{Type: &edpv1alpha1.Nexus{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileNexus{}

// ReconcileNexus reconciles a Nexus object
type ReconcileNexus struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client  client.Client
	scheme  *runtime.Scheme
	service service.NexusService
}

// Reconcile reads that state of the cluster for a Nexus object and makes changes based on the state read
// and what is in the Nexus.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  This example creates
// a Pod as an example
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileNexus) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling Nexus")

	// Fetch the Nexus instance
	instance := &edpv1alpha1.Nexus{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	err = r.service.Install(instance)
	if err != nil {
		log.Error(err, fmt.Sprintf("[ERROR] Cannot install Nexus object with name %s", instance.Name))
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	reqLogger.Info("Reconciling Nexus component %s/%s has been finished", request.Namespace, request.Name)
	return reconcile.Result{}, nil
}
