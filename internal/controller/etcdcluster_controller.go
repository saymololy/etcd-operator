/*
Copyright 2024.

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
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	etcdaenixiov1alpha1 "github.com/aenix-io/etcd-operator/api/v1alpha1"
)

// EtcdClusterReconciler reconciles a EtcdCluster object
type EtcdClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=etcd.aenix.io,resources=etcdclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=etcd.aenix.io,resources=etcdclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=etcd.aenix.io,resources=etcdclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;watch;delete;patch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;create;delete;update;patch;list;watch
//+kubebuilder:rbac:groups="apps",resources=statefulsets,verbs=get;create;delete;update;patch;list;watch

// Reconcile checks CR and current cluster state and performs actions to transform current state to desired.
func (r *EtcdClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(2).Info("reconciling object", "namespaced_name", req.NamespacedName)
	instance := &etcdaenixiov1alpha1.EtcdCluster{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(2).Info("object not found", "namespaced_name", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		// Error retrieving object, requeue
		return reconcile.Result{}, err
	}
	// If object is being deleted, skipping reconciliation
	if !instance.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}

	// 3. mark CR as initialized
	if len(instance.Status.Conditions) == 0 {
		instance.Status.Conditions = append(instance.Status.Conditions, metav1.Condition{
			Type:               etcdaenixiov1alpha1.EtcdConditionInitialized,
			Status:             "False",
			ObservedGeneration: instance.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "InitializationStarted",
			Message:            "Cluster initialization has started",
		})
	}
	defer func() {
		if err := r.Status().Update(ctx, instance); err != nil && !errors.IsConflict(err) {
			logger.Error(err, "unable to update cluster")
		}
	}()

	if err := r.ensureClusterObjects(ctx, instance); err != nil {
		return ctrl.Result{}, fmt.Errorf("cannot create Cluster auxiliary objects: %w", err)
	}

	if initIdx := slices.IndexFunc(instance.Status.Conditions, func(condition metav1.Condition) bool {
		return condition.Type == etcdaenixiov1alpha1.EtcdConditionInitialized
	}); initIdx != -1 {
		instance.Status.Conditions[initIdx].Status = "True"
		instance.Status.Conditions[initIdx].LastTransitionTime = metav1.Now()
		instance.Status.Conditions[initIdx].Reason = "InitializationComplete"
		instance.Status.Conditions[initIdx].Message = "Cluster initialization is complete"
	} else {
		instance.Status.Conditions = append(instance.Status.Conditions, metav1.Condition{
			Type:               etcdaenixiov1alpha1.EtcdConditionInitialized,
			Status:             "True",
			ObservedGeneration: instance.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "InitializationComplete",
			Message:            "Cluster initialization is complete",
		})
	}

	// at this point we should have cluster that can be bootstrapped. We should check if the cluster is ready

	// 4. ping cluster to check quorum and number of replica)
	// 5. if cluster is ready, change configmap ETCD_INITIAL_CLUSTER_STATE to existing
	// 6. mark CR as ready or not ready

	return ctrl.Result{}, nil
}

// ensureClusterObjects creates or updates all objects owned by cluster CR
func (r *EtcdClusterReconciler) ensureClusterObjects(ctx context.Context, cluster *etcdaenixiov1alpha1.EtcdCluster) error {
	// 1. create or update configmap <name>-cluster-state
	if err := r.ensureClusterStateConfigMap(ctx, cluster); err != nil {
		return err
	}
	if err := r.ensureClusterService(ctx, cluster); err != nil {
		return err
	}
	// 2. create or update statefulset
	if err := r.ensureClusterStatefulSet(ctx, cluster); err != nil {
		return err
	}

	return nil
}

func (r *EtcdClusterReconciler) ensureClusterService(ctx context.Context, cluster *etcdaenixiov1alpha1.EtcdCluster) error {
	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      cluster.Name,
	}, svc)
	// Service exists, skip creation
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("cannot get cluster service: %w", err)
	}

	svc = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "etcd",
				"app.kubernetes.io/instance":   cluster.Name,
				"app.kubernetes.io/managed-by": "etcd-operator",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "peer", TargetPort: intstr.FromInt32(2380), Port: 2380, Protocol: corev1.ProtocolTCP},
				{Name: "client", TargetPort: intstr.FromInt32(2379), Port: 2379, Protocol: corev1.ProtocolTCP},
			},
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "None",
			Selector: map[string]string{
				"app.kubernetes.io/name":       "etcd",
				"app.kubernetes.io/instance":   cluster.Name,
				"app.kubernetes.io/managed-by": "etcd-operator",
			},
			PublishNotReadyAddresses: true,
		},
	}
	if err = ctrl.SetControllerReference(cluster, svc, r.Scheme); err != nil {
		return fmt.Errorf("cannot set controller reference: %w", err)
	}
	if err = r.Create(ctx, svc); err != nil {
		return fmt.Errorf("cannot create cluster service: %w", err)
	}
	return nil
}

// ensureClusterStateConfigMap creates or updates cluster state configmap.
func (r *EtcdClusterReconciler) ensureClusterStateConfigMap(
	ctx context.Context, cluster *etcdaenixiov1alpha1.EtcdCluster) error {
	configMap := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      r.getClusterStateConfigMapName(cluster),
	}, configMap)
	// configmap exists, skip editing.
	if err == nil {
		return nil
	}

	// configmap does not exist, create with cluster state "new"
	if errors.IsNotFound(err) {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      r.getClusterStateConfigMapName(cluster),
			},
			Data: map[string]string{
				"ETCD_INITIAL_CLUSTER_STATE": "new",
			},
		}
		if err := ctrl.SetControllerReference(cluster, configMap, r.Scheme); err != nil {
			return fmt.Errorf("cannot set controller reference: %w", err)
		}
		if err := r.Create(ctx, configMap); err != nil {
			return fmt.Errorf("cannot create cluster state configmap: %w", err)
		}
		return nil
	}
	return fmt.Errorf("cannot get cluster state configmap: %w", err)
}

func (r *EtcdClusterReconciler) ensureClusterStatefulSet(
	ctx context.Context, cluster *etcdaenixiov1alpha1.EtcdCluster) error {
	statefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      cluster.Name,
	}, statefulSet)

	// statefulset does not exist, create new one
	notFound := false
	if errors.IsNotFound(err) {
		notFound = true
		// prepare initial cluster members
		initialCluster := ""
		for i := uint(0); i < cluster.Spec.Replicas; i++ {
			if i > 0 {
				initialCluster += ","
			}
			initialCluster += fmt.Sprintf("%s-%d=https://%s-%d.%s.%s.svc:2380",
				cluster.Name, i,
				cluster.Name, i, cluster.Name, cluster.Namespace,
			)
		}

		statefulSet = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			},
			Spec: appsv1.StatefulSetSpec{
				// initialize static fields that cannot be changed across updates.
				Replicas:            new(int32),
				ServiceName:         cluster.Name,
				PodManagementPolicy: appsv1.ParallelPodManagement,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name":       "etcd",
						"app.kubernetes.io/instance":   cluster.Name,
						"app.kubernetes.io/managed-by": "etcd-operator",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/name":       "etcd",
							"app.kubernetes.io/instance":   cluster.Name,
							"app.kubernetes.io/managed-by": "etcd-operator",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "etcd",
								Image: "quay.io/coreos/etcd:v3.5.12",
								Command: []string{
									"etcd",
									"--name=$(POD_NAME)",
									"--listen-peer-urls=https://0.0.0.0:2380",
									// for first version disable TLS for client access
									"--listen-client-urls=http://0.0.0.0:2379",
									"--initial-advertise-peer-urls=https://$(POD_NAME)." + cluster.Name + ".$(POD_NAMESPACE).svc:2380",
									"--data-dir=/var/run/etcd/default.etcd",
									"--initial-cluster=" + initialCluster,
									fmt.Sprintf("--initial-cluster-token=%s-%s", cluster.Name, cluster.Namespace),
									"--auto-tls",
									"--peer-auto-tls",
									"--advertise-client-urls=http://$(POD_NAME)." + cluster.Name + ".$(POD_NAMESPACE).svc:2379",
								},
								Ports: []corev1.ContainerPort{
									{Name: "peer", ContainerPort: 2380},
									{Name: "client", ContainerPort: 2379},
								},
								EnvFrom: []corev1.EnvFromSource{
									{
										ConfigMapRef: &corev1.ConfigMapEnvSource{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: r.getClusterStateConfigMapName(cluster),
											},
										},
									},
								},
								Env: []corev1.EnvVar{
									{
										Name: "POD_NAME",
										ValueFrom: &corev1.EnvVarSource{
											FieldRef: &corev1.ObjectFieldSelector{
												FieldPath: "metadata.name",
											},
										},
									},
									{
										Name: "POD_NAMESPACE",
										ValueFrom: &corev1.EnvVarSource{
											FieldRef: &corev1.ObjectFieldSelector{
												FieldPath: "metadata.namespace",
											},
										},
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "data",
										ReadOnly:  false,
										MountPath: "/var/run/etcd",
									},
								},
								LivenessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromInt32(2379),
										},
									},
									InitialDelaySeconds: 5,
									PeriodSeconds:       5,
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromInt32(2379),
										},
									},
									InitialDelaySeconds: 5,
									PeriodSeconds:       5,
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "data",
								VolumeSource: corev1.VolumeSource{
									// TODO: implement PVC
									EmptyDir: &corev1.EmptyDirVolumeSource{
										SizeLimit: &cluster.Spec.Storage.Size,
									},
								},
							},
						},
					},
				},
			},
		}
		*statefulSet.Spec.Replicas = int32(cluster.Spec.Replicas)
		if err := ctrl.SetControllerReference(cluster, statefulSet, r.Scheme); err != nil {
			return fmt.Errorf("cannot set controller reference: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("cannot get cluster statefulset: %w", err)
	}

	// resize is not currently supported
	//statefulSet.Spec.Replicas = proto.Int32(int32(cluster.Spec.Replicas))
	statefulSet.Spec.Template.Spec.Volumes[0].VolumeSource.EmptyDir.SizeLimit = &cluster.Spec.Storage.Size

	if notFound {
		if err := r.Create(ctx, statefulSet); err != nil {
			return fmt.Errorf("cannot create statefulset: %w", err)
		}
	} else {
		if err := r.Update(ctx, statefulSet); err != nil {
			return fmt.Errorf("cannot update statefulset: %w", err)
		}
	}

	return nil
}

func (r *EtcdClusterReconciler) getClusterStateConfigMapName(cluster *etcdaenixiov1alpha1.EtcdCluster) string {
	return cluster.Name + "-cluster-state"
}

// SetupWithManager sets up the controller with the Manager.
func (r *EtcdClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&etcdaenixiov1alpha1.EtcdCluster{}).
		Complete(r)
}