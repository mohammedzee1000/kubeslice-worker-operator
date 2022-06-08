/*
 *  Copyright (c) 2022 Avesha, Inc. All rights reserved.
 *
 *  SPDX-License-Identifier: Apache-2.0
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *  http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 */

package slice

import (
	"context"
	"fmt"

	spokev1alpha1 "github.com/kubeslice/apis-ent/pkg/worker/v1alpha1"
	kubeslicev1beta1 "github.com/kubeslice/worker-operator/api/v1beta1"
	"github.com/kubeslice/worker-operator/internal/logger"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *SliceReconciler) reconcileNamespaceResourceUsage(ctx context.Context, slice *kubeslicev1beta1.Slice, currentTime, configUpdatedOn int64) (ctrl.Result, error) {
	log := logger.FromContext(ctx).WithValues("type", "resource_usage")
	// Get the list of existing namespaces that are part of slice
	namespacesInSlice := &corev1.NamespaceList{}
	listOpts := []client.ListOption{
		client.MatchingLabels(map[string]string{
			ApplicationNamespaceSelectorLabelKey: slice.Name,
		}),
	}
	err := r.List(ctx, namespacesInSlice, listOpts...)
	if err != nil {
		log.Error(err, "Failed to list namespaces")
		return ctrl.Result{}, err
	}
	log.Info("reconciling", "namespacesInSlice", namespacesInSlice)

	clientset, err := metricsv.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		log.Error(err, "error creating client set")
	}
	var cpuAllNS, memAllNs int64
	for _, namespace := range namespacesInSlice.Items {
		// metrics of all the pods of a namespace
		podMetricsList, err := clientset.MetricsV1beta1().PodMetricses(namespace.Name).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return ctrl.Result{}, err
		}
		cpu, mem := getCPUandMemoryMetricsofNs(podMetricsList.Items)
		cpuAllNS += cpu
		memAllNs += mem
	}
	log.Info("CPU usage of all namespaces", "cpu", cpuAllNS)
	log.Info("Memory usage of all namespaces", "mem", memAllNs)

	if cpuAllNS == 0 && memAllNs == 0 { // no current usage
		return ctrl.Result{}, nil
	}
	updateResourceUsage := false
	if slice.Status.SliceConfig.WorkerSliceResourceQuotaStatus == nil {
		slice.Status.SliceConfig.WorkerSliceResourceQuotaStatus = &spokev1alpha1.WorkerSliceResourceQuotaStatus{}
		updateResourceUsage = true
	} else if checkToUpdateControllerSliceResourceQuota(slice.Status.SliceConfig.WorkerSliceResourceQuotaStatus.
		ClusterResourceQuotaStatus.ResourcesUsage, cpuAllNS, memAllNs) {
		updateResourceUsage = true
	}
	if updateResourceUsage {
		allNsResourceUsage := []spokev1alpha1.NamespaceResourceQuotaStatus{}
		for _, namespace := range namespacesInSlice.Items {
			// metrics of all the pods of a namespace
			podMetricsList, _ := clientset.MetricsV1beta1().PodMetricses(namespace.Name).List(context.TODO(), metav1.ListOptions{})
			cpu, mem := getCPUandMemoryMetricsofNs(podMetricsList.Items)
			memAsQuantity := resource.NewMilliQuantity(cpu, resource.BinarySI)
			cpuAsQuantity := resource.NewMilliQuantity(mem, resource.DecimalSI)
			allNsResourceUsage = append(allNsResourceUsage, spokev1alpha1.NamespaceResourceQuotaStatus{
				ResourceUsage: spokev1alpha1.Resource{
					Cpu:    *cpuAsQuantity,
					Memory: *memAsQuantity,
				},
				Namespace: namespace.Name,
			})
			fmt.Println("cpu", cpuAsQuantity)
			fmt.Println("mem", memAsQuantity)
		}
		cpuAllNSQuantity := resource.NewMilliQuantity(cpuAllNS, resource.BinarySI)
		memAllNsQuantity := resource.NewMilliQuantity(memAllNs, resource.DecimalSI)

		slice.Status.SliceConfig.WorkerSliceResourceQuotaStatus.ClusterResourceQuotaStatus =
			spokev1alpha1.ClusterResourceQuotaStatus{
				NamespaceResourceQuotaStatus: allNsResourceUsage,
				ResourcesUsage: spokev1alpha1.Resource{
					Memory: *memAllNsQuantity,
					Cpu:    *cpuAllNSQuantity,
				},
			}

		r.HubClient.UpdateResourceUsage(ctx, slice.Name, *slice.Status.SliceConfig.WorkerSliceResourceQuotaStatus)
		slice.Status.ConfigUpdatedOn = currentTime
		r.Status().Update(ctx, slice)
	}
	return ctrl.Result{}, nil
}

func checkToUpdateControllerSliceResourceQuota(sliceUsage spokev1alpha1.Resource, cpu, mem int64) bool {
	cpuUsage, _ := sliceUsage.Cpu.AsInt64()
	memUsage, _ := sliceUsage.Memory.AsInt64()
	if calculatePercentageDiff(cpuUsage, cpu) > 5 || calculatePercentageDiff(memUsage, cpu) > 5 {
		return true
	}
	return false
}

func getCPUandMemoryMetricsofNs(podMetricsList []v1beta1.PodMetrics) (int64, int64) {
	var nsTotalCPU, nsTotalMem int64
	for _, podMetrics := range podMetricsList {
		for _, container := range podMetrics.Containers {
			usage := container.Usage
			nowCpu := usage.Cpu().MilliValue()
			nowMem, _ := usage.Memory().AsInt64()
			nsTotalCPU += nowCpu
			nsTotalMem += nowMem
		}
	}
	return nsTotalCPU, nsTotalMem
}

func calculatePercentageDiff(a, b int64) int64 {
	return ((b - a) * 100) / a
}
