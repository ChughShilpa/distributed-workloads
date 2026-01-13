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
	"testing"

	trainerv1alpha1 "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kueue/apis/kueue/v1beta1"

	. "github.com/opendatahub-io/distributed-workloads/tests/common"
	. "github.com/opendatahub-io/distributed-workloads/tests/common/support"
)

var (
	sleepNamespaceName = "test-trainer-upgrade-sleep"
	sleepTrainJobName  = "trainjob-upgrade-sleep"
)

func TestSetupSleepTrainJob(t *testing.T) {
	Tags(t, PreUpgrade)
	test := With(t)

	// Create a namespace
	createOrGetUpgradeTestNamespace(test, sleepNamespaceName)

	// Create Kueue resources
	resourceFlavor := CreateKueueResourceFlavor(test, v1beta1.ResourceFlavorSpec{})
	defer test.Client().Kueue().KueueV1beta1().ResourceFlavors().Delete(test.Ctx(), resourceFlavor.Name, metav1.DeleteOptions{})
	cqSpec := v1beta1.ClusterQueueSpec{
		NamespaceSelector: &metav1.LabelSelector{},
		ResourceGroups: []v1beta1.ResourceGroup{
			{
				CoveredResources: []corev1.ResourceName{corev1.ResourceName("cpu"), corev1.ResourceName("memory")},
				Flavors: []v1beta1.FlavorQuotas{
					{
						Name: v1beta1.ResourceFlavorReference(resourceFlavor.Name),
						Resources: []v1beta1.ResourceQuota{
							{
								Name:         corev1.ResourceCPU,
								NominalQuota: resource.MustParse("8"),
							},
							{
								Name:         corev1.ResourceMemory,
								NominalQuota: resource.MustParse("18Gi"),
							},
						},
					},
				},
			},
		},
	}

	clusterQueue := CreateKueueClusterQueue(test, cqSpec)
	defer test.Client().Kueue().KueueV1beta1().ClusterQueues().Delete(test.Ctx(), clusterQueue.Name, metav1.DeleteOptions{})
	localQueue := CreateKueueLocalQueue(test, sleepNamespaceName, clusterQueue.Name, AsDefaultQueue)

	// Create sleep TrainJob
	createSleepTrainJob(test, sleepNamespaceName, localQueue)

	// Make sure the TrainJob is running (not suspended), waiting for Trainer upgrade
	test.Eventually(TrainJob(test, sleepNamespaceName, sleepTrainJobName), TestTimeoutShort).
		Should(WithTransform(TrainJobConditionSuspended, Equal(metav1.ConditionFalse)))
	test.T().Logf("TrainJob %s/%s is running", sleepNamespaceName, sleepTrainJobName)
}

func TestVerifySleepTrainJob(t *testing.T) {
	Tags(t, PostUpgrade)
	test := With(t)
	namespace := GetNamespaceWithName(test, sleepNamespaceName)

	// Cleanup namespace in the end
	defer DeleteTestNamespace(test, namespace)

	// TrainJob should be still running (not suspended and not completed)
	test.Expect(TrainJob(test, sleepNamespaceName, sleepTrainJobName)(test)).
		Should(WithTransform(TrainJobConditionSuspended, Equal(metav1.ConditionFalse)))

	// Pod job should be running without restart
	test.Expect(GetPods(test, sleepNamespaceName, metav1.ListOptions{})).
		Should(
			And(
				HaveLen(1),
				ContainElement(WithTransform(sleepPodRestartCount, BeNumerically("==", 0))),
			),
		)
	test.T().Logf("TrainJob %s/%s is still running after upgrade with no pod restarts", sleepNamespaceName, sleepTrainJobName)
}

func createSleepTrainJob(test Test, namespace string, localQueue *v1beta1.LocalQueue) *trainerv1alpha1.TrainJob {
	// Does TrainJob already exist?
	_, err := test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Get(test.Ctx(), sleepTrainJobName, metav1.GetOptions{})
	if err == nil {
		// If yes then delete it and wait until there are no TrainJobs in the namespace
		err := test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Delete(test.Ctx(), sleepTrainJobName, metav1.DeleteOptions{})
		test.Expect(err).NotTo(HaveOccurred())
		test.Eventually(TrainJobs(test, namespace), TestTimeoutShort).Should(BeEmpty())
	} else if !errors.IsNotFound(err) {
		test.T().Fatalf("Error retrieving TrainJob with name `%s`: %v", sleepTrainJobName, err)
	}

	trainJob := &trainerv1alpha1.TrainJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: sleepTrainJobName,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": localQueue.Name,
			},
		},
		Spec: trainerv1alpha1.TrainJobSpec{
			RuntimeRef: trainerv1alpha1.RuntimeRef{
				Name: defaultClusterTrainingRuntime,
			},
			Trainer: &trainerv1alpha1.Trainer{
				Image:   Ptr(GetSleepImage()),
				Command: []string{"sleep"},
				Args:    []string{"24h"},
			},
		},
	}

	trainJob, err = test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Create(test.Ctx(), trainJob, metav1.CreateOptions{})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Created TrainJob %s/%s successfully", trainJob.Namespace, trainJob.Name)

	return trainJob
}

func sleepPodRestartCount(pod corev1.Pod) int {
	return int(pod.Status.ContainerStatuses[0].RestartCount)
}

