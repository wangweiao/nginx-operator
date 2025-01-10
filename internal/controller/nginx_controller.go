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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
	log := log.FromContext(ctx)

	// Fetch the Nginx instance
	nginx := &serverv1alpha1.Nginx{}
	err := r.Get(ctx, req.NamespacedName, nginx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("nginx resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get nginx")
		return ctrl.Result{}, err
	}

	// Check if the deployment already exists, if not create a new one
	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: nginx.Name, Namespace: nginx.Namespace}, found)
	if err != nil && apierrors.IsNotFound(err) {
		dep, err := r.deploymentForNginx(nginx)
		if err != nil {
			log.Error(err, "Failed to define new Deployment resource for Nginx")
			return ctrl.Result{}, err
		}

		log.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err = r.Create(ctx, dep); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: time.Minute}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// Update image if necessary
	currentImage := found.Spec.Template.Spec.Containers[0].Image
	desiredImage := "bitnami/nginx:" + nginx.Spec.Version
	if currentImage != desiredImage {
		log.Info("Image version mismatch, updating deployment image", "currentImage", currentImage, "desiredImage", desiredImage)
		found.Spec.Template.Spec.Containers[0].Image = desiredImage
		if err = r.Update(ctx, found); err != nil {
			log.Error(err, "Failed to update Deployment image", "Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Update size if necessary
	size := nginx.Spec.Size
	if *found.Spec.Replicas != size {
		log.Info("Replica size mismatch, updating deployment size", "currentSize", *found.Spec.Replicas, "desiredSize", size)
		found.Spec.Replicas = &size
		if err = r.Update(ctx, found); err != nil {
			log.Error(err, "Failed to update Deployment size", "Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
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
