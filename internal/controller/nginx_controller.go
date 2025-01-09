/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	serverv1alpha1 "github.com/example/nginx-operator/api/v1alpha1"
)

// Definitions to manage status conditions
const (
	// typeAvailableNginx represents the status of the Deployment reconciliation
	typeAvailableNginx = "Available"
	// typeDegradedNginx represents the status used when the custom resource is deleted and the finalizer operations are yet to occur.
	typeDegradedNginx = "Degraded"
)

// NginxReconciler reconciles a Nginx object
type NginxReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=server.example.com,resources=nginxes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=server.example.com,resources=nginxes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=server.example.com,resources=nginxes/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Nginx object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.4/pkg/reconcile
func (r *NginxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// log := log.FromContext(ctx)

	// Fetch the Nginx instance
	// The purpose is check if the Custom Resource for the Kind Nginx
	// is applied on the cluster if not we return nil to stop the reconciliation
	nginx := &serverv1alpha1.Nginx{}
	err := r.Get(ctx, req.NamespacedName, nginx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			//log.Info("nginx resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		//log.Error(err, "Failed to get nginx")
		return ctrl.Result{}, err
	}

	// Let's just set the status as Unknown when no status is available
	if nginx.Status.Conditions == nil || len(nginx.Status.Conditions) == 0 {
		meta.SetStatusCondition(&nginx.Status.Conditions, metav1.Condition{Type: typeAvailableNginx, Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: "Starting reconciliation"})
		if err = r.Status().Update(ctx, nginx); err != nil {
			//log.Error(err, "Failed to update Nginx status")
			return ctrl.Result{}, err
		}

		// Let's re-fetch the nginx Custom Resource after updating the status
		// so that we have the latest state of the resource on the cluster and we will avoid
		// raising the error "the object has been modified, please apply
		// your changes to the latest version and try again" which would re-trigger the reconciliation
		// if we try to update it again in the following operations
		if err := r.Get(ctx, req.NamespacedName, nginx); err != nil {
			//log.Error(err, "Failed to re-fetch nginx")
			return ctrl.Result{}, err
		}
	}

	// Check if the deployment already exists, if not create a new one
	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: nginx.Name, Namespace: nginx.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new deployment
		dep, err := r.deploymentForNginx(nginx)
		if err != nil {
			//log.Error(err, "Failed to define new Deployment resource for Nginx")

			// The following implementation will update the status
			meta.SetStatusCondition(&nginx.Status.Conditions, metav1.Condition{Type: typeAvailableNginx,
				Status: metav1.ConditionFalse, Reason: "Reconciling",
				Message: fmt.Sprintf("Failed to create Deployment for the custom resource (%s): (%s)", nginx.Name, err)})

			if err := r.Status().Update(ctx, nginx); err != nil {
				//log.Error(err, "Failed to update Nginx status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		//log.Info("Creating a new Deployment",
		//"Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err = r.Create(ctx, dep); err != nil {
			//log.Error(err, "Failed to create new Deployment",
			//"Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}

		// Deployment created successfully
		// We will requeue the reconciliation so that we can ensure the state
		// and move forward for the next operations
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	} else if err != nil {
		//log.Error(err, "Failed to get Deployment")
		// Let's return the error for the reconciliation be re-trigged again
		return ctrl.Result{}, err
	}

	currentImage := found.Spec.Template.Spec.Containers[0].Image
	desiredImage := "bitnami/nginx:" + nginx.Spec.Version
	if currentImage != desiredImage {
		//log.Info("Image version mismatch, deleting and recreating deployment",
		//"currentImage", currentImage, "desiredImage", desiredImage)

		// Delete the existing deployment
		if err := r.Delete(ctx, found); err != nil {
			//log.Error(err, "Failed to delete existing Deployment")
			return ctrl.Result{}, err
		}

		// Create a new deployment with the correct image version
		dep, err := r.deploymentForNginx(nginx)
		if err != nil {
			//log.Error(err, "Failed to define new Deployment resource for Nginx")
			return ctrl.Result{}, err
		}

		if err = r.Create(ctx, dep); err != nil {
			//log.Error(err, "Failed to create new Deployment",
			//"Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// The CRD API defines that the Nginx type have a NginxSpec.Size field
	// to set the quantity of Deployment instances to the desired state on the cluster.
	// Therefore, the following code will ensure the Deployment size is the same as defined
	// via the Size spec of the Custom Resource which we are reconciling.
	size := nginx.Spec.Size

	if *found.Spec.Replicas != size {
		found.Spec.Replicas = &size
		if err = r.Update(ctx, found); err != nil {
			//log.Error(err, "Failed to update Deployment",
			//"Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)

			// Re-fetch the nginx Custom Resource before updating the status
			// so that we have the latest state of the resource on the cluster and we will avoid
			// raising the error "the object has been modified, please apply
			// your changes to the latest version and try again" which would re-trigger the reconciliation
			if err := r.Get(ctx, req.NamespacedName, nginx); err != nil {
				//log.Error(err, "Failed to re-fetch nginx")
				return ctrl.Result{}, err
			}

			// The following implementation will update the status
			meta.SetStatusCondition(&nginx.Status.Conditions, metav1.Condition{Type: typeAvailableNginx,
				Status: metav1.ConditionFalse, Reason: "Resizing",
				Message: fmt.Sprintf("Failed to update the size for the custom resource (%s): (%s)", nginx.Name, err)})

			if err := r.Status().Update(ctx, nginx); err != nil {
				//log.Error(err, "Failed to update Nginx status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, err
		}

		// Now, that we update the size we want to requeue the reconciliation
		// so that we can ensure that we have the latest state of the resource before
		// update. Also, it will help ensure the desired state on the cluster
		return ctrl.Result{Requeue: true}, nil
	}

	// The following implementation will update the status
	meta.SetStatusCondition(&nginx.Status.Conditions, metav1.Condition{Type: typeAvailableNginx,
		Status: metav1.ConditionTrue, Reason: "Reconciling",
		Message: fmt.Sprintf("Deployment for custom resource (%s) with %d replicas created successfully", nginx.Name, size)})

	if err := r.Status().Update(ctx, nginx); err != nil {
		//log.Error(err, "Failed to update Nginx status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NginxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&serverv1alpha1.Nginx{}).
		Owns(&appsv1.Deployment{}).
		Named("nginx").
		Complete(r)
}

// deploymentForNginx returns a Nginx Deployment object
func (r *NginxReconciler) deploymentForNginx(
	nginx *serverv1alpha1.Nginx) (*appsv1.Deployment, error) {
	replicas := nginx.Spec.Size
	version := nginx.Spec.Version
	image := "bitnami/nginx:" + version

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nginx.Name,
			Namespace: nginx.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "project"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "project"},
				},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &[]bool{true}[0],
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Image:           image,
						Name:            "nginx",
						ImagePullPolicy: corev1.PullIfNotPresent,
						// Ensure restrictive context for the container
						// More info: https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             &[]bool{true}[0],
							RunAsUser:                &[]int64{1001}[0],
							AllowPrivilegeEscalation: &[]bool{false}[0],
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{
									"ALL",
								},
							},
						},
						Ports: []corev1.ContainerPort{{
							ContainerPort: 11211,
							Name:          "nginx",
						}},
						Command: []string{"nginx", "-g", "daemon off;"},
					}},
				},
			},
		},
	}

	// Set the ownerRef for the Deployment
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/
	if err := ctrl.SetControllerReference(nginx, dep, r.Scheme); err != nil {
		return nil, err
	}
	return dep, nil
}
