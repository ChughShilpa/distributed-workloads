/*
Copyright 2023.

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

package fms

import (
	"fmt"
	"testing"

	kftov1 "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	. "github.com/onsi/gomega"
	. "github.com/project-codeflare/codeflare-common/support"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"

	"github.com/opendatahub-io/distributed-workloads/tests/kfto"
)

func TestPytorchjobWithSFTtrainerFinetuning(t *testing.T) {
	runPytorchjobWithSFTtrainer(t, "resources/config.json", 0)
}

func TestPytorchjobWithSFTtrainerLoRa(t *testing.T) {
	runPytorchjobWithSFTtrainer(t, "resources/config_lora.json", 0)
}
func TestPytorchjobWithSFTtrainerQLoRa(t *testing.T) {
	runPytorchjobWithSFTtrainer(t, "resources/config_qlora.json", 1)
}

func runPytorchjobWithSFTtrainer(t *testing.T, modelConfigFile string, numGpus int) {
	test := With(t)

	// Create a namespace
	namespace := test.CreateOrGetTestNamespace().Name

	// Create a ConfigMap with training dataset and configuration
	configData := map[string][]byte{
		"config.json":                   ReadFile(test, modelConfigFile),
		"twitter_complaints_small.json": ReadFile(test, "resources/twitter_complaints_small.json"),
	}
	config := CreateConfigMap(test, namespace, configData)

	// Create Kueue resources
	resourceFlavor := CreateKueueResourceFlavor(test, kueuev1beta1.ResourceFlavorSpec{})
	defer test.Client().Kueue().KueueV1beta1().ResourceFlavors().Delete(test.Ctx(), resourceFlavor.Name, metav1.DeleteOptions{})
	cqSpec := kueuev1beta1.ClusterQueueSpec{
		NamespaceSelector: &metav1.LabelSelector{},
		ResourceGroups: []kueuev1beta1.ResourceGroup{
			{
				CoveredResources: []corev1.ResourceName{corev1.ResourceName("cpu"), corev1.ResourceName("memory"), corev1.ResourceName("nvidia.com/gpu")},
				Flavors: []kueuev1beta1.FlavorQuotas{
					{
						Name: kueuev1beta1.ResourceFlavorReference(resourceFlavor.Name),
						Resources: []kueuev1beta1.ResourceQuota{
							{
								Name:         corev1.ResourceCPU,
								NominalQuota: resource.MustParse("8"),
							},
							{
								Name:         corev1.ResourceMemory,
								NominalQuota: resource.MustParse("12Gi"),
							},
							{
								Name:         corev1.ResourceName("nvidia.com/gpu"),
								NominalQuota: resource.MustParse(fmt.Sprint(numGpus)),
							},
						},
					},
				},
			},
		},
	}
	clusterQueue := CreateKueueClusterQueue(test, cqSpec)
	defer test.Client().Kueue().KueueV1beta1().ClusterQueues().Delete(test.Ctx(), clusterQueue.Name, metav1.DeleteOptions{})
	localQueue := CreateKueueLocalQueue(test, namespace, clusterQueue.Name, AsDefaultQueue)
	defer test.Client().Kueue().KueueV1beta1().LocalQueues(namespace).Delete(test.Ctx(), localQueue.Name, metav1.DeleteOptions{})

	// Create PVC for trained model
	outputPvc := CreatePersistentVolumeClaim(test, namespace, "10Gi", corev1.ReadWriteOnce)
	defer test.Client().Core().CoreV1().PersistentVolumeClaims(namespace).Delete(test.Ctx(), outputPvc.Name, metav1.DeleteOptions{})

	// Create training PyTorch job
	tuningJob := createPyTorchJob(test, namespace, localQueue.Name, *config, numGpus, outputPvc.Name)
	defer test.Client().Kubeflow().KubeflowV1().PyTorchJobs(namespace).Delete(test.Ctx(), tuningJob.Name, *metav1.NewDeleteOptions(0))

	// Make sure the Kueue Workload is admitted
	test.Eventually(KueueWorkloads(test, namespace), TestTimeoutLong).
		Should(
			And(
				HaveLen(1),
				ContainElement(WithTransform(KueueWorkloadAdmitted, BeTrueBecause("Workload failed to be admitted"))),
			),
		)

	// Make sure the PyTorch job is running
	test.Eventually(PyTorchJob(test, namespace, tuningJob.Name), TestTimeoutLong).
		Should(WithTransform(PyTorchJobConditionRunning, Equal(corev1.ConditionTrue)))

	// Make sure the PyTorch job succeed
	test.Eventually(PyTorchJob(test, namespace, tuningJob.Name), TestTimeoutLong).Should(WithTransform(PyTorchJobConditionSucceeded, Equal(corev1.ConditionTrue)))
	test.T().Logf("PytorchJob %s/%s ran successfully", tuningJob.Namespace, tuningJob.Name)

	_, bucketEndpointSet := GetStorageBucketDefaultEndpoint()
	if bucketEndpointSet {
		uploadToS3(test, namespace, outputPvc.Name, "model")
	}
}

func TestPytorchjobUsingKueueQuota(t *testing.T) {
	test := With(t)

	// Create a namespace
	namespace := test.CreateOrGetTestNamespace().Name

	// Create a ConfigMap with training dataset and configuration
	configData := map[string][]byte{
		"config.json":                   ReadFile(test, "resources/config.json"),
		"twitter_complaints_small.json": ReadFile(test, "resources/twitter_complaints_small.json"),
	}
	config := CreateConfigMap(test, namespace, configData)

	// Create limited Kueue resources to run just one Pytorchjob at a time
	resourceFlavor := CreateKueueResourceFlavor(test, kueuev1beta1.ResourceFlavorSpec{})
	defer test.Client().Kueue().KueueV1beta1().ResourceFlavors().Delete(test.Ctx(), resourceFlavor.Name, metav1.DeleteOptions{})
	cqSpec := kueuev1beta1.ClusterQueueSpec{
		NamespaceSelector: &metav1.LabelSelector{},
		ResourceGroups: []kueuev1beta1.ResourceGroup{
			{
				CoveredResources: []corev1.ResourceName{corev1.ResourceName("cpu"), corev1.ResourceName("memory"), corev1.ResourceName("nvidia.com/gpu")},
				Flavors: []kueuev1beta1.FlavorQuotas{
					{
						Name: kueuev1beta1.ResourceFlavorReference(resourceFlavor.Name),
						Resources: []kueuev1beta1.ResourceQuota{
							{
								Name:         corev1.ResourceCPU,
								NominalQuota: resource.MustParse("3"),
							},
							{
								Name:         corev1.ResourceMemory,
								NominalQuota: resource.MustParse("8Gi"),
							},
							{
								Name:         corev1.ResourceName("nvidia.com/gpu"),
								NominalQuota: resource.MustParse("0"),
							},
						},
					},
				},
			},
		},
	}
	clusterQueue := CreateKueueClusterQueue(test, cqSpec)
	defer test.Client().Kueue().KueueV1beta1().ClusterQueues().Delete(test.Ctx(), clusterQueue.Name, metav1.DeleteOptions{})
	localQueue := CreateKueueLocalQueue(test, namespace, clusterQueue.Name, AsDefaultQueue)

	// Create first PVC for trained model
	outputPvc := CreatePersistentVolumeClaim(test, namespace, "10Gi", corev1.ReadWriteOnce)
	defer test.Client().Core().CoreV1().PersistentVolumeClaims(namespace).Delete(test.Ctx(), outputPvc.Name, metav1.DeleteOptions{})

	// Create first training PyTorch job
	tuningJob := createPyTorchJob(test, namespace, localQueue.Name, *config, 0, outputPvc.Name)

	// Make sure the PyTorch job is running
	test.Eventually(PyTorchJob(test, namespace, tuningJob.Name), TestTimeoutLong).
		Should(WithTransform(PyTorchJobConditionRunning, Equal(corev1.ConditionTrue)))

	// Create second PVC for trained model
	secondOutputPvc := CreatePersistentVolumeClaim(test, namespace, "10Gi", corev1.ReadWriteOnce)
	defer test.Client().Core().CoreV1().PersistentVolumeClaims(namespace).Delete(test.Ctx(), outputPvc.Name, metav1.DeleteOptions{})

	// Create second training PyTorch job
	secondTuningJob := createPyTorchJob(test, namespace, localQueue.Name, *config, 0, secondOutputPvc.Name)

	// Make sure the second PyTorch job is suspended, waiting for first job to finish
	test.Eventually(PyTorchJob(test, namespace, secondTuningJob.Name), TestTimeoutShort).
		Should(WithTransform(PyTorchJobConditionSuspended, Equal(corev1.ConditionTrue)))

	// Make sure the first PyTorch job succeed
	test.Eventually(PyTorchJob(test, namespace, tuningJob.Name), TestTimeoutLong).Should(WithTransform(PyTorchJobConditionSucceeded, Equal(corev1.ConditionTrue)))
	test.T().Logf("PytorchJob %s/%s ran successfully", tuningJob.Namespace, tuningJob.Name)

	// Second PyTorch job should be started now
	test.Eventually(PyTorchJob(test, namespace, secondTuningJob.Name), TestTimeoutShort).
		Should(WithTransform(PyTorchJobConditionRunning, Equal(corev1.ConditionTrue)))

	// Make sure the second PyTorch job succeed
	test.Eventually(PyTorchJob(test, namespace, secondTuningJob.Name), TestTimeoutLong).Should(WithTransform(PyTorchJobConditionSucceeded, Equal(corev1.ConditionTrue)))
	test.T().Logf("PytorchJob %s/%s ran successfully", secondTuningJob.Namespace, secondTuningJob.Name)
}

func createPyTorchJob(test Test, namespace, localQueueName string, config corev1.ConfigMap, numGpus int, outputPvcName string) *kftov1.PyTorchJob {
	tuningJob := &kftov1.PyTorchJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "PyTorchJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "kfto-sft-",
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": localQueueName,
			},
		},
		Spec: kftov1.PyTorchJobSpec{
			PyTorchReplicaSpecs: map[kftov1.ReplicaType]*kftov1.ReplicaSpec{
				"Master": {
					Replicas:      Ptr(int32(1)),
					RestartPolicy: "OnFailure",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Tolerations: []corev1.Toleration{
								{
									Key:      "nvidia.com/gpu",
									Operator: corev1.TolerationOpExists,
								},
							},
							InitContainers: []corev1.Container{
								{
									Name:            "copy-model",
									Image:           kfto.GetBloomModelImage(),
									ImagePullPolicy: corev1.PullIfNotPresent,
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "tmp-volume",
											MountPath: "/tmp",
										},
									},
									Command: []string{"/bin/sh", "-c"},
									Args:    []string{"mkdir /tmp/model; cp -r /models/bloom-560m /tmp/model"},
								},
							},
							Containers: []corev1.Container{
								{
									Name:            "pytorch",
									Image:           GetFmsHfTuningImage(test),
									ImagePullPolicy: corev1.PullIfNotPresent,
									Env: []corev1.EnvVar{
										{
											Name:  "SFT_TRAINER_CONFIG_JSON_PATH",
											Value: "/etc/config/config.json",
										},
										{
											Name:  "HF_HOME",
											Value: "/tmp/huggingface",
										},
										{
											Name:  "HOME",
											Value: "/tmp/triton-home",
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "config-volume",
											MountPath: "/etc/config",
										},
										{
											Name:      "tmp-volume",
											MountPath: "/tmp",
										},
										{
											Name:      "output-volume",
											MountPath: "/mnt/output",
										},
									},
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("2"),
											corev1.ResourceMemory: resource.MustParse("7Gi"),
											"nvidia.com/gpu":      resource.MustParse(fmt.Sprint(numGpus)),
										},
										Limits: corev1.ResourceList{
											corev1.ResourceCPU:    resource.MustParse("2"),
											corev1.ResourceMemory: resource.MustParse("7Gi"),
											"nvidia.com/gpu":      resource.MustParse(fmt.Sprint(numGpus)),
										},
									},
									SecurityContext: &corev1.SecurityContext{
										RunAsNonRoot:           Ptr(true),
										ReadOnlyRootFilesystem: Ptr(true),
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "config-volume",
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
								{
									Name: "output-volume",
									VolumeSource: corev1.VolumeSource{
										PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
											ClaimName: outputPvcName,
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

	tuningJob, err := test.Client().Kubeflow().KubeflowV1().PyTorchJobs(namespace).Create(test.Ctx(), tuningJob, metav1.CreateOptions{})
	test.Expect(err).NotTo(HaveOccurred())
	test.T().Logf("Created PytorchJob %s/%s successfully", tuningJob.Namespace, tuningJob.Name)

	return tuningJob
}
