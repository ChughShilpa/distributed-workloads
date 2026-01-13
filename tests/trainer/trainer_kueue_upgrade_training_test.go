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
	kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueueacv1beta1 "sigs.k8s.io/kueue/client-go/applyconfiguration/kueue/v1beta1"

	. "github.com/opendatahub-io/distributed-workloads/tests/common"
	. "github.com/opendatahub-io/distributed-workloads/tests/common/support"
	trainerutils "github.com/opendatahub-io/distributed-workloads/tests/trainer/utils"
)

var (
	upgradeNamespaceName = "test-trainer-upgrade"
	resourceFlavorName   = "rf-trainer-upgrade"
	clusterQueueName     = "cq-trainer-upgrade"
	localQueueName       = "lq-trainer-upgrade"
	upgradeTrainJobName  = "trainjob-upgrade"
)

func TestSetupUpgradeTrainJob(t *testing.T) {
	Tags(t, PreUpgrade)
	test := With(t)
	setupKueue(test)

	// Create a namespace with Kueue label
	CreateOrGetTestNamespaceWithName(test, upgradeNamespaceName, WithKueueManaged())
	test.T().Logf("Created/retrieved namespace with kueue label: %s", upgradeNamespaceName)

	// Create Kueue resources with StopPolicy
	resourceFlavor := kueueacv1beta1.ResourceFlavor(resourceFlavorName)
	appliedResourceFlavor, err := test.Client().Kueue().KueueV1beta1().ResourceFlavors().Apply(test.Ctx(), resourceFlavor, metav1.ApplyOptions{FieldManager: "setup-TrainJob", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Applied Kueue ResourceFlavor %s successfully", appliedResourceFlavor.Name)

	clusterQueue := kueueacv1beta1.ClusterQueue(clusterQueueName).WithSpec(
		kueueacv1beta1.ClusterQueueSpec().
			WithNamespaceSelector(metav1.LabelSelector{}).
			WithResourceGroups(
				kueueacv1beta1.ResourceGroup().WithCoveredResources(
					corev1.ResourceName("cpu"), corev1.ResourceName("memory"),
				).WithFlavors(
					kueueacv1beta1.FlavorQuotas().
						WithName(kueuev1beta1.ResourceFlavorReference(resourceFlavorName)).
						WithResources(
							kueueacv1beta1.ResourceQuota().WithName(corev1.ResourceCPU).WithNominalQuota(resource.MustParse("8")),
							kueueacv1beta1.ResourceQuota().WithName(corev1.ResourceMemory).WithNominalQuota(resource.MustParse("18Gi")),
						),
				),
			).
			WithStopPolicy(kueuev1beta1.Hold),
	)
	appliedClusterQueue, err := test.Client().Kueue().KueueV1beta1().ClusterQueues().Apply(test.Ctx(), clusterQueue, metav1.ApplyOptions{FieldManager: "setup-TrainJob", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Applied Kueue ClusterQueue %s with StopPolicy=Hold successfully", appliedClusterQueue.Name)

	localQueue := kueueacv1beta1.LocalQueue(localQueueName, upgradeNamespaceName).
		WithAnnotations(map[string]string{"kueue.x-k8s.io/default-queue": "true"}).
		WithSpec(
			kueueacv1beta1.LocalQueueSpec().WithClusterQueue(kueuev1beta1.ClusterQueueReference(clusterQueueName)),
		)
	appliedLocalQueue, err := test.Client().Kueue().KueueV1beta1().LocalQueues(upgradeNamespaceName).Apply(test.Ctx(), localQueue, metav1.ApplyOptions{FieldManager: "setup-TrainJob", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Applied Kueue LocalQueue %s/%s successfully", appliedLocalQueue.Namespace, appliedLocalQueue.Name)

	// Create TrainJob
	trainJob := createUpgradeTrainJob(test, upgradeNamespaceName, appliedLocalQueue.Name)

	// Make sure the TrainJob is suspended, waiting for ClusterQueue to be enabled
	test.Eventually(TrainJob(test, trainJob.Namespace, upgradeTrainJobName), TestTimeoutShort).
		Should(WithTransform(TrainJobConditionSuspended, Equal(metav1.ConditionTrue)))
	test.T().Logf("TrainJob %s/%s is suspended, waiting for ClusterQueue to be enabled after upgrade", trainJob.Namespace, upgradeTrainJobName)
}

func TestRunUpgradeTrainJob(t *testing.T) {
	Tags(t, PostUpgrade)
	test := With(t)
	setupKueue(test)
	namespace := GetNamespaceWithName(test, upgradeNamespaceName)

	// Cleanup everything in the end
	defer test.Client().Kueue().KueueV1beta1().ResourceFlavors().Delete(test.Ctx(), resourceFlavorName, metav1.DeleteOptions{})
	defer test.Client().Kueue().KueueV1beta1().ClusterQueues().Delete(test.Ctx(), clusterQueueName, metav1.DeleteOptions{})
	defer DeleteTestNamespace(test, namespace)

	// Enable ClusterQueue to process waiting TrainJob
	clusterQueue := kueueacv1beta1.ClusterQueue(clusterQueueName).WithSpec(kueueacv1beta1.ClusterQueueSpec().WithStopPolicy(kueuev1beta1.None))
	_, err := test.Client().Kueue().KueueV1beta1().ClusterQueues().Apply(test.Ctx(), clusterQueue, metav1.ApplyOptions{FieldManager: "application/apply-patch", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Enabled ClusterQueue %s by setting StopPolicy to None", clusterQueueName)

	// TrainJob should be started now
	test.Eventually(TrainJob(test, upgradeNamespaceName, upgradeTrainJobName), TestTimeoutLong).
		Should(WithTransform(TrainJobConditionSuspended, Equal(metav1.ConditionFalse)))
	test.T().Logf("TrainJob %s/%s is now running", upgradeNamespaceName, upgradeTrainJobName)

	// Make sure the TrainJob completes successfully
	test.Eventually(TrainJob(test, upgradeNamespaceName, upgradeTrainJobName), TestTimeoutLong).
		Should(WithTransform(TrainJobConditionComplete, Equal(metav1.ConditionTrue)))
	test.T().Logf("TrainJob %s/%s completed successfully after upgrade", upgradeNamespaceName, upgradeTrainJobName)
}

func createUpgradeTrainJob(test Test, namespace, localQueueName string) *trainerv1alpha1.TrainJob {
	// Does TrainJob already exist?
	_, err := test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Get(test.Ctx(), upgradeTrainJobName, metav1.GetOptions{})
	if err == nil {
		// If yes then delete it and wait until there are no TrainJobs in the namespace
		err := test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Delete(test.Ctx(), upgradeTrainJobName, metav1.DeleteOptions{})
		test.Expect(err).NotTo(HaveOccurred())
		test.Eventually(TrainJobs(test, namespace), TestTimeoutShort).Should(BeEmpty())
	} else if !errors.IsNotFound(err) {
		test.T().Fatalf("Error retrieving TrainJob with name `%s`: %v", upgradeTrainJobName, err)
	}

	trainJob := &trainerv1alpha1.TrainJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: upgradeTrainJobName,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": localQueueName,
			},
		},
		Spec: trainerv1alpha1.TrainJobSpec{
			RuntimeRef: trainerv1alpha1.RuntimeRef{
				Name: trainerutils.DefaultClusterTrainingRuntime,
			},
			Trainer: &trainerv1alpha1.Trainer{
				Command: []string{
					"python",
					"-c",
					"import torch; print(f'PyTorch version: {torch.__version__}'); import time; time.sleep(5); print('Training completed successfully')",
				},
			},
		},
	}

	trainJob, err = test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Create(test.Ctx(), trainJob, metav1.CreateOptions{})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Created TrainJob %s/%s successfully", trainJob.Namespace, trainJob.Name)

	return trainJob
}
