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

package trainer

import (
	"strings"
	"testing"

	trainerv1alpha1 "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	. "github.com/opendatahub-io/distributed-workloads/tests/common"
	. "github.com/opendatahub-io/distributed-workloads/tests/common/support"
)

var (
	runtimeNamespaceName       = "test-trainer-upgrade-runtime"
	customRuntimeName          = "custom-sleep-runtime"
	runtimesConfigMapNamespace = "default"
	runtimesConfigMapName      = "all-trainingruntimes-upgrade"
	runtimesConfigMapKey       = "runtime-names"
)

func TestSetupTrainingRuntime(t *testing.T) {
	Tags(t, PreUpgrade)
	test := With(t)

	// Create namespace
	CreateOrGetTestNamespaceWithName(test, runtimeNamespaceName)

	// Create custom TrainingRuntime
	createCustomTrainingRuntime(test, runtimeNamespaceName)

	// Verify the TrainingRuntime exists
	runtime, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes(runtimeNamespaceName).Get(
		test.Ctx(), customRuntimeName, metav1.GetOptions{})
	test.Expect(err).NotTo(HaveOccurred())
	test.Expect(runtime.Name).To(Equal(customRuntimeName))
	test.T().Logf("Custom TrainingRuntime %s/%s created successfully", runtimeNamespaceName, customRuntimeName)
}

func TestVerifyTrainingRuntime(t *testing.T) {
	Tags(t, PostUpgrade)
	test := With(t)

	namespace := GetNamespaceWithName(test, runtimeNamespaceName)
	defer DeleteTestNamespace(test, namespace)

	// Verify TrainingRuntime still exists after upgrade by listing all runtimes
	runtimes, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes(runtimeNamespaceName).List(
		test.Ctx(), metav1.ListOptions{})
	test.Expect(err).NotTo(HaveOccurred(), "Failed to list TrainingRuntimes")

	var runtimeNames []string
	for _, runtime := range runtimes.Items {
		runtimeNames = append(runtimeNames, runtime.Name)
	}

	test.Expect(runtimeNames).To(ContainElement(customRuntimeName),
		"Custom TrainingRuntime should exist after upgrade. Found runtimes: %v", runtimeNames)
	test.T().Logf("TrainingRuntime %s/%s is preserved after upgrade", runtimeNamespaceName, customRuntimeName)
}

func createCustomTrainingRuntime(test Test, namespace string) *trainerv1alpha1.TrainingRuntime {
	// Check if runtime already exists
	_, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes(namespace).Get(
		test.Ctx(), customRuntimeName, metav1.GetOptions{})
	if err == nil {
		// Delete existing runtime
		err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes(namespace).Delete(
			test.Ctx(), customRuntimeName, metav1.DeleteOptions{})
		test.Expect(err).NotTo(HaveOccurred())
		test.Eventually(func() bool {
			_, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes(namespace).Get(
				test.Ctx(), customRuntimeName, metav1.GetOptions{})
			return errors.IsNotFound(err)
		}, TestTimeoutShort).Should(BeTrue())
	} else if !errors.IsNotFound(err) {
		test.T().Fatalf("Error retrieving TrainingRuntime: %v", err)
	}

	trainingRuntime := &trainerv1alpha1.TrainingRuntime{
		ObjectMeta: metav1.ObjectMeta{
			Name:      customRuntimeName,
			Namespace: namespace,
		},
		Spec: trainerv1alpha1.TrainingRuntimeSpec{
			Template: trainerv1alpha1.JobSetTemplateSpec{
				Spec: jobsetv1alpha2.JobSetSpec{
					ReplicatedJobs: []jobsetv1alpha2.ReplicatedJob{
						{
							Name:     "node",
							Replicas: 1,
							Template: batchv1.JobTemplateSpec{
								Spec: batchv1.JobSpec{
									BackoffLimit: Ptr(int32(0)),
									Template: corev1.PodTemplateSpec{
										Spec: corev1.PodSpec{
											RestartPolicy: corev1.RestartPolicyNever,
											Containers: []corev1.Container{
												{
													Name:            "trainer",
													Image:           GetSleepImage(),
													ImagePullPolicy: corev1.PullIfNotPresent,
													Command:         []string{"sleep"},
													Args:            []string{"24h"},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	runtime, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes(namespace).Create(
		test.Ctx(), trainingRuntime, metav1.CreateOptions{})
	test.Expect(err).NotTo(HaveOccurred(), "Failed to create TrainingRuntime")
	test.T().Logf("Created custom TrainingRuntime %s/%s", runtime.Namespace, runtime.Name)

	return runtime
}

// TestSetupAllTrainingRuntimes lists all TrainingRuntimes across the cluster
// and stores their names in a ConfigMap for post-upgrade verification.
func TestSetupAllTrainingRuntimes(t *testing.T) {
	Tags(t, PreUpgrade)
	test := With(t)

	// List all TrainingRuntimes across all namespaces
	runtimes, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes("").List(
		test.Ctx(), metav1.ListOptions{})
	test.Expect(err).NotTo(HaveOccurred(), "Failed to list all TrainingRuntimes")

	var runtimeFullNames []string
	for _, runtime := range runtimes.Items {
		runtimeFullNames = append(runtimeFullNames, runtime.Namespace+"/"+runtime.Name)
	}

	storeRuntimeNamesInConfigMap(test, runtimeFullNames)

	if len(runtimeFullNames) == 0 {
		test.T().Log("No TrainingRuntimes found in cluster before upgrade")
	} else {
		test.T().Logf("Stored %d TrainingRuntimes in ConfigMap: %v", len(runtimeFullNames), runtimeFullNames)
	}
}

// TestVerifyAllTrainingRuntimes verifies all pre-upgrade TrainingRuntimes still exist.
func TestVerifyAllTrainingRuntimes(t *testing.T) {
	Tags(t, PostUpgrade)
	test := With(t)

	preUpgradeNames := getRuntimeNamesFromConfigMap(test)

	defer func() {
		_ = test.Client().Core().CoreV1().ConfigMaps(runtimesConfigMapNamespace).Delete(
			test.Ctx(), runtimesConfigMapName, metav1.DeleteOptions{})
	}()

	if len(preUpgradeNames) == 0 {
		test.T().Log("No TrainingRuntimes existed before upgrade, skipping preservation check")
		return
	}
	test.T().Logf("Pre-upgrade TrainingRuntimes: %v", preUpgradeNames)

	runtimes, err := test.Client().Trainer().TrainerV1alpha1().TrainingRuntimes("").List(
		test.Ctx(), metav1.ListOptions{})
	test.Expect(err).NotTo(HaveOccurred(), "Failed to list all TrainingRuntimes")

	var currentNames []string
	for _, runtime := range runtimes.Items {
		currentNames = append(currentNames, runtime.Namespace+"/"+runtime.Name)
	}
	test.T().Logf("Post-upgrade TrainingRuntimes: %v", currentNames)

	for _, preUpgradeName := range preUpgradeNames {
		test.Expect(currentNames).To(ContainElement(preUpgradeName),
			"TrainingRuntime %s should exist after upgrade. Current runtimes: %v", preUpgradeName, currentNames)
	}
	test.T().Logf("All %d TrainingRuntimes preserved after upgrade", len(preUpgradeNames))
}

func storeRuntimeNamesInConfigMap(test Test, names []string) {
	// Delete existing ConfigMap if present
	_ = test.Client().Core().CoreV1().ConfigMaps(runtimesConfigMapNamespace).Delete(
		test.Ctx(), runtimesConfigMapName, metav1.DeleteOptions{})

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runtimesConfigMapName,
			Namespace: runtimesConfigMapNamespace,
		},
		Data: map[string]string{
			runtimesConfigMapKey: strings.Join(names, ","),
		},
	}

	_, err := test.Client().Core().CoreV1().ConfigMaps(runtimesConfigMapNamespace).Create(
		test.Ctx(), configMap, metav1.CreateOptions{})
	test.Expect(err).NotTo(HaveOccurred(), "Failed to create ConfigMap for all runtime names")
}

func getRuntimeNamesFromConfigMap(test Test) []string {
	configMap, err := test.Client().Core().CoreV1().ConfigMaps(runtimesConfigMapNamespace).Get(
		test.Ctx(), runtimesConfigMapName, metav1.GetOptions{})
	test.Expect(err).NotTo(HaveOccurred(), "Failed to get ConfigMap with all runtime names")

	namesStr := configMap.Data[runtimesConfigMapKey]
	if namesStr == "" {
		return []string{}
	}
	return strings.Split(namesStr, ",")
}
