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
)

var (
	upgradeNamespaceName      = "test-trainer-upgrade"
	upgradeResourceFlavorName = "rf-trainer-upgrade"
	upgradeClusterQueueName   = "cq-trainer-upgrade"
	upgradeLocalQueueName     = "lq-trainer-upgrade"
	upgradeTrainJobName       = "trainjob-upgrade"
)

func TestSetupTrainJob(t *testing.T) {
	Tags(t, PreUpgrade)
	test := With(t)

	createOrGetUpgradeTestNamespace(test, upgradeNamespaceName)

	// Create a ConfigMap with training dataset and configuration
	fashionMnist := readFile(test, "resources/fashion_mnist.py")
	downloadFashionMnist := readFile(test, "resources/download_fashion_mnist.py")
	requirementsFile := readFile(test, "resources/requirements.txt")

	configData := map[string][]byte{
		"fashion_mnist.py":          fashionMnist,
		"download_fashion_mnist.py": downloadFashionMnist,
		"requirements.txt":          requirementsFile,
	}

	config := CreateConfigMap(test, upgradeNamespaceName, configData)

	// Create Kueue resources with StopPolicy set to Hold (suspended)
	resourceFlavor := kueueacv1beta1.ResourceFlavor(upgradeResourceFlavorName)
	appliedResourceFlavor, err := test.Client().Kueue().KueueV1beta1().ResourceFlavors().Apply(test.Ctx(), resourceFlavor, metav1.ApplyOptions{FieldManager: "setup-TrainJob", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Applied Kueue ResourceFlavor %s successfully", appliedResourceFlavor.Name)

	clusterQueue := kueueacv1beta1.ClusterQueue(upgradeClusterQueueName).WithSpec(
		kueueacv1beta1.ClusterQueueSpec().
			WithNamespaceSelector(metav1.LabelSelector{}).
			WithResourceGroups(
				kueueacv1beta1.ResourceGroup().WithCoveredResources(
					corev1.ResourceName("cpu"), corev1.ResourceName("memory"),
				).WithFlavors(
					kueueacv1beta1.FlavorQuotas().
						WithName(kueuev1beta1.ResourceFlavorReference(upgradeResourceFlavorName)).
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

	localQueue := kueueacv1beta1.LocalQueue(upgradeLocalQueueName, upgradeNamespaceName).
		WithAnnotations(map[string]string{"kueue.x-k8s.io/default-queue": "true"}).
		WithSpec(
			kueueacv1beta1.LocalQueueSpec().WithClusterQueue(kueuev1beta1.ClusterQueueReference(upgradeClusterQueueName)),
		)
	appliedLocalQueue, err := test.Client().Kueue().KueueV1beta1().LocalQueues(upgradeNamespaceName).Apply(test.Ctx(), localQueue, metav1.ApplyOptions{FieldManager: "setup-TrainJob", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Applied Kueue LocalQueue %s/%s successfully", appliedLocalQueue.Namespace, appliedLocalQueue.Name)

	// Create training TrainJob
	trainJob := createUpgradeTrainJob(test, upgradeNamespaceName, appliedLocalQueue.Name, *config)

	// Make sure the TrainJob is suspended, waiting for ClusterQueue to be enabled after upgrade
	test.Eventually(TrainJob(test, trainJob.Namespace, upgradeTrainJobName), TestTimeoutShort).
		Should(WithTransform(TrainJobConditionSuspended, Equal(metav1.ConditionTrue)))
	test.T().Logf("TrainJob %s/%s is suspended, waiting for ClusterQueue to be enabled after upgrade", trainJob.Namespace, upgradeTrainJobName)
}

func TestRunTrainJob(t *testing.T) {
	Tags(t, PostUpgrade)
	test := With(t)
	namespace := GetNamespaceWithName(test, upgradeNamespaceName)

	// Cleanup everything in the end
	defer test.Client().Kueue().KueueV1beta1().ResourceFlavors().Delete(test.Ctx(), upgradeResourceFlavorName, metav1.DeleteOptions{})
	defer test.Client().Kueue().KueueV1beta1().ClusterQueues().Delete(test.Ctx(), upgradeClusterQueueName, metav1.DeleteOptions{})
	defer DeleteTestNamespace(test, namespace)

	// Enable ClusterQueue to process waiting TrainJob
	clusterQueue := kueueacv1beta1.ClusterQueue(upgradeClusterQueueName).WithSpec(kueueacv1beta1.ClusterQueueSpec().WithStopPolicy(kueuev1beta1.None))
	_, err := test.Client().Kueue().KueueV1beta1().ClusterQueues().Apply(test.Ctx(), clusterQueue, metav1.ApplyOptions{FieldManager: "application/apply-patch", Force: true})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Enabled ClusterQueue %s by setting StopPolicy to None", upgradeClusterQueueName)

	// TrainJob should be started now (not suspended)
	test.Eventually(TrainJob(test, upgradeNamespaceName, upgradeTrainJobName), TestTimeoutLong).
		Should(WithTransform(TrainJobConditionSuspended, Equal(metav1.ConditionFalse)))
	test.T().Logf("TrainJob %s/%s is now running", upgradeNamespaceName, upgradeTrainJobName)

	// Make sure the TrainJob completes successfully
	test.Eventually(TrainJob(test, upgradeNamespaceName, upgradeTrainJobName), TestTimeoutLong).
		Should(WithTransform(TrainJobConditionComplete, Equal(metav1.ConditionTrue)))
	test.T().Logf("TrainJob %s/%s completed successfully after upgrade", upgradeNamespaceName, upgradeTrainJobName)
}

func createUpgradeTrainJob(test Test, namespace, localQueueName string, config corev1.ConfigMap) *trainerv1alpha1.TrainJob {
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

	storageBucketEndpoint, storageBucketEndpointExists := GetStorageBucketDefaultEndpoint()
	storageBucketAccessKeyId, storageBucketAccessKeyIdExists := GetStorageBucketAccessKeyId()
	storageBucketSecretKey, storageBucketSecretKeyExists := GetStorageBucketSecretKey()
	storageBucketName, storageBucketNameExists := GetStorageBucketName()
	storageBucketFashionMnistDir, storageBucketFashionMnistDirExists := GetStorageBucketFashionMnistDir()

	trainJob := &trainerv1alpha1.TrainJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: upgradeTrainJobName,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": localQueueName,
			},
		},
		Spec: trainerv1alpha1.TrainJobSpec{
			RuntimeRef: trainerv1alpha1.RuntimeRef{
				Name: defaultClusterTrainingRuntime,
			},
			Trainer: &trainerv1alpha1.Trainer{
				Command: []string{"/bin/bash", "-c"},
				Args: []string{
					`mkdir -p /tmp/lib /tmp/datasets/fashion_mnist && export PYTHONPATH=$PYTHONPATH:/tmp/lib && \
					pip install --no-cache-dir -r /mnt/files/requirements.txt --target=/tmp/lib --verbose && \
					echo "Downloading Fashion MNIST dataset..." && \
					python3 /mnt/files/download_fashion_mnist.py --dataset_path "/tmp/datasets/fashion_mnist" && \
					echo -e "\n\n Dataset downloaded to /tmp/datasets/fashion_mnist" && ls -R /tmp/datasets/fashion_mnist && \
					echo -e "\n\n Starting training..." && \
					torchrun --nproc_per_node 2 /mnt/files/fashion_mnist.py`,
				},
				Env: []corev1.EnvVar{
					{
						Name:  "DATASET_PATH",
						Value: "/tmp/datasets/fashion_mnist",
					},
					{
						Name:  "LIB_PATH",
						Value: "/tmp/lib",
					},
					{
						Name:  "PIP_INDEX_URL",
						Value: GetPipIndexURL(),
					},
					{
						Name:  "PIP_TRUSTED_HOST",
						Value: GetPipTrustedHost(),
					},
				},
				ResourcesPerNode: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("6Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("6Gi"),
					},
				},
			},
			PodSpecOverrides: []trainerv1alpha1.PodSpecOverride{
				{
					TargetJobs: []trainerv1alpha1.PodSpecOverrideTargetJob{
						{Name: "node"},
					},
					Containers: []trainerv1alpha1.ContainerOverride{
						{
							Name: "trainer",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      config.Name,
									MountPath: "/mnt/files",
								},
								{
									Name:      "tmp-volume",
									MountPath: "/tmp",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: config.Name,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: config.Name,
									},
								},
							},
						},
						{
							Name: "tmp-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	// Use storage bucket to download the Fashion MNIST datasets if required environment variables are provided
	if storageBucketEndpointExists && storageBucketAccessKeyIdExists && storageBucketSecretKeyExists && storageBucketNameExists && storageBucketFashionMnistDirExists {
		storageBucketEnvVars := []corev1.EnvVar{
			{
				Name:  "AWS_DEFAULT_ENDPOINT",
				Value: storageBucketEndpoint,
			},
			{
				Name:  "AWS_ACCESS_KEY_ID",
				Value: storageBucketAccessKeyId,
			},
			{
				Name:  "AWS_SECRET_ACCESS_KEY",
				Value: storageBucketSecretKey,
			},
			{
				Name:  "AWS_STORAGE_BUCKET",
				Value: storageBucketName,
			},
			{
				Name:  "AWS_STORAGE_BUCKET_FASHION_MNIST_DIR",
				Value: storageBucketFashionMnistDir,
			},
		}

		// Append the list of environment variables for the trainer container
		trainJob.Spec.Trainer.Env = append(trainJob.Spec.Trainer.Env, storageBucketEnvVars...)
	} else {
		test.T().Logf("Skipped usage of S3 storage bucket, because required environment variables aren't provided!\nRequired environment variables : AWS_DEFAULT_ENDPOINT, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_STORAGE_BUCKET, AWS_STORAGE_BUCKET_FASHION_MNIST_DIR")
	}

	trainJob, err = test.Client().Trainer().TrainerV1alpha1().TrainJobs(namespace).Create(test.Ctx(), trainJob, metav1.CreateOptions{})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Created TrainJob %s/%s successfully", trainJob.Namespace, trainJob.Name)

	return trainJob
}

func createOrGetUpgradeTestNamespace(test Test, name string, options ...Option[*corev1.Namespace]) (namespace *corev1.Namespace) {
	// Verify that the namespace really exists and return it, create it if doesn't exist yet
	namespace, err := test.Client().Core().CoreV1().Namespaces().Get(test.Ctx(), name, metav1.GetOptions{})
	if err == nil {
		return
	} else if errors.IsNotFound(err) {
		test.T().Logf("%s namespace doesn't exists. Creating ...", name)
		return CreateTestNamespaceWithName(test, name, options...)
	} else {
		test.T().Fatalf("Error retrieving namespace with name `%s`: %v", name, err)
	}
	return
}

