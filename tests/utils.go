/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2017 Red Hat, Inc.
 *
 */

package tests

import (
	"bytes"
	"flag"
	"fmt"

	"github.com/ghodss/yaml"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"

	extproviderv1alpha1 "kubevirt.io/cluster-api-provider-external/pkg/apis/providerconfig/v1alpha1"
	"kubevirt.io/cluster-api-provider-external/pkg/external/machinesetup"
	"kubevirt.io/node-recovery/pkg/client"
)

const (
	PodSSHExecName  = "ssh-executor"
	PodFakeIpmiName = "fakeipmi"
)

const (
	NamespaceTest                       = "test-namespace"
	NamespaceClusterApiExternalProvider = "cluster-api-provider-external"
)

const ServiceFakeIpmiPort = 6230

const (
	ConfigMapMachineSetupName = "machine-setup"
	ConfigMapMachineSetupFile = "machine_setup_configs.yaml"
)

const ClusterName = "test-cluster"

const (
	MachineName  = "test-machine"
	MachineLabel = "test"
)

const (
	FencingConfigName     = "fake-ipmilan"
	FencingContainerName  = "fence-provision-manager"
	FencingContainerImage = "docker.io/kubevirt/fence-provision-manager:latest"
	FencingSecretName     = "ipmi-secret"
	FencingUsername       = "admin"
	FencingPassword       = "password"
)

const NodeUser = "vagrant"

const TestingServiceAccount = "testing-cluster-admin"

var (
	ContainersPrefix = "docker.io/kubevirt"
	ContainersTag    = "devel"
)

var (
	NonMasterNode   = ""
	NonMasterNodeIP = ""
)

func init() {
	flag.StringVar(&ContainersPrefix, "container-prefix", "docker.io/kubevirt", "Set the repository prefix for all images")
	flag.StringVar(&ContainersTag, "container-tag", "latest", "Set the image tag or digest to use")
}

func AfterTestSuitCleanup() error {
	err := removeNamespace(NamespaceTest)
	if err != nil {
		return err
	}

	err = removeCluster(ClusterName, NamespaceClusterApiExternalProvider)
	if err != nil {
		return err
	}

	err = removeMachine(MachineName, NamespaceClusterApiExternalProvider)
	if err != nil {
		return err
	}

	err = removeSecret(FencingSecretName, NamespaceClusterApiExternalProvider)
	if err != nil {
		return err
	}
	return err
}

func BeforeTestSuitSetup() error {
	err := createNamespace(NamespaceTest)
	if err != nil {
		return err
	}

	err = createCluster(ClusterName, NamespaceClusterApiExternalProvider)
	if err != nil {
		return err
	}

	err = createMachine(
		MachineName,
		NamespaceClusterApiExternalProvider,
		MachineLabel,
		[]extproviderv1alpha1.MachineRole{extproviderv1alpha1.NodeRole},
	)
	if err != nil {
		return err
	}

	err = createSecret(
		FencingSecretName,
		NamespaceClusterApiExternalProvider,
		map[string]string{"username": FencingUsername, "password": FencingPassword},
	)
	if err != nil {
		return err
	}

	err = getNonMasterNode()
	if err != nil {
		return err
	}

	return updateNodeAnnotation(NonMasterNode, MachineName, NamespaceClusterApiExternalProvider)
}

func createNamespace(name string) error {
	kubeClient := client.NewKubeClientSet()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	_, err := kubeClient.CoreV1().Namespaces().Create(ns)
	return err
}

func removeNamespace(name string) error {
	kubeClient := client.NewKubeClientSet()

	err := kubeClient.CoreV1().Namespaces().Delete(name, &metav1.DeleteOptions{})
	return err
}

func createMachine(name string, namespace string, label string, roles []extproviderv1alpha1.MachineRole) error {
	extProviderConfig := &extproviderv1alpha1.ExternalMachineProviderConfig{
		Label: label,
		Roles: roles,
	}

	codec, err := extproviderv1alpha1.NewCodec()
	if err != nil {
		return err
	}

	providerConfig, err := codec.EncodeToProviderConfig(extProviderConfig)
	if err != nil {
		return err
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: clusterv1.MachineSpec{
			ProviderConfig: *providerConfig,
		},
	}
	clusterAPIClient := client.NewClusterAPIClientSet()
	_, err = clusterAPIClient.ClusterV1alpha1().Machines(namespace).Create(machine)
	return err
}

func removeMachine(name string, namespace string) error {
	clusterAPIClient := client.NewClusterAPIClientSet()
	err := clusterAPIClient.ClusterV1alpha1().Machines(namespace).Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		return err
	}

	_, err = clusterAPIClient.ClusterV1alpha1().Machines(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	_, err = clusterAPIClient.ClusterV1alpha1().Machines(namespace).Patch(
		name,
		types.JSONPatchType,
		[]byte("[{ \"op\": \"remove\", \"path\": \"/metadata/finalizers\" }]"),
	)
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

func createCluster(name string, namespace string) error {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: clusterv1.ClusterSpec{},
	}
	clusterAPIClient := client.NewClusterAPIClientSet()
	_, err := clusterAPIClient.ClusterV1alpha1().Clusters(namespace).Create(cluster)
	return err
}

func removeCluster(name string, namespace string) error {
	clusterAPIClient := client.NewClusterAPIClientSet()
	err := clusterAPIClient.ClusterV1alpha1().Clusters(namespace).Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		return err
	}

	_, err = clusterAPIClient.ClusterV1alpha1().Clusters(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	_, err = clusterAPIClient.ClusterV1alpha1().Clusters(namespace).Patch(
		name,
		types.JSONPatchType,
		[]byte("[{ \"op\": \"remove\", \"path\": \"/metadata/finalizers\" }]"),
	)
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

func createSecret(name string, namespace string, data map[string]string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		StringData: map[string]string{},
	}
	for k, v := range data {
		secret.StringData[k] = v
	}

	kubeClient := client.NewKubeClientSet()
	_, err := kubeClient.CoreV1().Secrets(namespace).Create(secret)
	return err
}

func removeSecret(name string, namespace string) error {
	kubeClient := client.NewKubeClientSet()
	err := kubeClient.CoreV1().Secrets(namespace).Delete(name, &metav1.DeleteOptions{})
	return err
}

// UpdateMachineSetupConfigMap updates machine-setup configuration
func UpdateMachineSetupConfigMap(
	machineName string,
	label string,
	roles []extproviderv1alpha1.MachineRole,
	ips map[string]string,
	ports map[string]string,
	secret string,
) error {
	kubeClient := client.NewKubeClientSet()
	configMap, err := kubeClient.CoreV1().ConfigMaps(NamespaceClusterApiExternalProvider).Get(ConfigMapMachineSetupName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	machineSetupConfigs := &machinesetup.MachineConfigList{
		Items: []machinesetup.MachineConfig{
			{
				MachineParams: []machinesetup.MachineParams{
					{
						Label: label,
						Roles: roles,
					},
				},
				Config: &machinesetup.Config{
					FencingConfig: &extproviderv1alpha1.FencingConfig{
						ObjectMeta: metav1.ObjectMeta{
							Name: FencingConfigName,
						},
						Container: &corev1.Container{
							Name:    FencingContainerName,
							Image:   FencingContainerImage,
							Command: []string{"fence-provision-manager"},
							Args: []string{
								"ansible",
								"--agent-type",
								"ipmilan",
								"--playbook-path",
								"/home/non-root/ansible/provision.yml",
							},
							ImagePullPolicy: corev1.PullIfNotPresent,
						},
						CheckArgs:  []string{"--action", "discover"},
						CreateArgs: []string{"--action", "provision"},
						DeleteArgs: []string{"--action", "deprovision"},
						Secret:     secret,
						DynamicConfig: []extproviderv1alpha1.DynamicConfigElement{
							{
								Field:  "ip",
								Values: ips,
							},
							{
								Field:  "ipport",
								Values: ports,
							},
							{
								Field: "lanplus",
								Values: map[string]string{
									MachineName: "",
								},
							},
						},
					},
				},
			},
		},
	}
	updatedData, err := yaml.Marshal(machineSetupConfigs)
	if err != nil {
		return err
	}
	configMap.Data[ConfigMapMachineSetupFile] = string(updatedData)
	_, err = kubeClient.CoreV1().ConfigMaps(NamespaceClusterApiExternalProvider).Update(configMap)
	return err
}

func updateNodeAnnotation(nodeName string, machineName string, machineNamespace string) error {
	kubeClient := client.NewKubeClientSet()
	node, err := kubeClient.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	node.Annotations["machine"] = fmt.Sprintf("%s/%s", machineNamespace, machineName)
	_, err = kubeClient.CoreV1().Nodes().Update(node)
	return err
}

func getNodeByLabelSelector(selector string) (*corev1.Node, error) {
	kubeClient := client.NewKubeClientSet()
	nodes, err := kubeClient.CoreV1().Nodes().List(metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, err
	}

	if len(nodes.Items) == 0 {
		return nil, fmt.Errorf("environment does not have non-master nodes")
	}
	return &nodes.Items[0], nil
}

func getNonMasterNode() error {
	node, err := getNodeByLabelSelector("!node-role.kubernetes.io/master")
	if err != nil {
		return err
	}

	NonMasterNode = node.Name
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			NonMasterNodeIP = a.Address
			break
		}
	}
	return nil
}

func createPod(
	name string,
	namespace string,
	command []string,
	args []string,
	tolerations []corev1.Toleration,
	nodeName string,
) (*corev1.Pod, error) {
	kubeClient := client.NewKubeClientSet()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name + rand.String(5),
			Labels: map[string]string{name: ""},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    name,
					Image:   GetImageName(name),
					Command: command,
					Args:    args,
				},
			},
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			Tolerations: tolerations,
		},
	}
	return kubeClient.CoreV1().Pods(namespace).Create(pod)
}

// RemovePod removes pod from the namespace
func RemovePod(name string, namespace string) error {
	kubeClient := client.NewKubeClientSet()
	err := kubeClient.CoreV1().Pods(namespace).Delete(name, &metav1.DeleteOptions{})
	return err
}

// CreateSSHExecPod will create new pod on the specific node to execute ssh commands
func CreateSSHExecPod(nodeName string) (*corev1.Pod, error) {
	tolerations := []corev1.Toleration{
		{
			Effect: corev1.TaintEffectNoSchedule,
			Key:    "node-role.kubernetes.io/master",
		},
	}
	return createPod(
		PodSSHExecName,
		NamespaceTest,
		[]string{"sleep"},
		[]string{"3600"},
		tolerations,
		nodeName,
	)
}

// CreateFakeIpmiPod will create new pod on the specific node with fake IPMI server
func CreateFakeIpmiPod(nodeName string) (*corev1.Pod, error) {
	tolerations := []corev1.Toleration{
		{
			Effect: corev1.TaintEffectNoSchedule,
			Key:    "node-role.kubernetes.io/master",
		},
	}
	return createPod(
		PodFakeIpmiName,
		NamespaceTest,
		[]string{"/usr/bin/fakeipmi.par"},
		[]string{"6230"},
		tolerations,
		nodeName,
	)
}

// CreateFakeIpmiService will create service to talk with fake IPMI service
func CreateFakeIpmiService(port int, targetPort int, protocol corev1.Protocol) (*corev1.Service, error) {
	kubeClient := client.NewKubeClientSet()
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: PodFakeIpmiName,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:       int32(port),
					TargetPort: intstr.FromInt(targetPort),
					Protocol:   protocol,
				},
			},
			Selector: map[string]string{PodFakeIpmiName: ""},
		},
	}

	return kubeClient.CoreV1().Services(NamespaceTest).Create(service)
}

// RemoveService removes service from the specific namespace
func RemoveService(name string, namespace string) error {
	kubeClient := client.NewKubeClientSet()
	err := kubeClient.CoreV1().Services(namespace).Delete(name, &metav1.DeleteOptions{})
	return err
}

// GetImageName generates image name from container prefix and tag
func GetImageName(name string) string {
	return fmt.Sprintf("%s/%s:%s", ContainersPrefix, name, ContainersTag)
}

// GetNamespace returns namespace object by name
func GetNamespace(name string) (*corev1.Namespace, error) {
	kubeClient := client.NewKubeClientSet()
	return kubeClient.CoreV1().Namespaces().Get(name, metav1.GetOptions{})
}

// GetMasterNode returns master node object
func GetMasterNode() (*corev1.Node, error) {
	return getNodeByLabelSelector("node-role.kubernetes.io/master")
}

// ExecuteCommandOnPod will execute give command on the pod, similar to kubectl exec
func ExecuteCommandOnPod(pod *corev1.Pod, containerName string, command []string) (stdout, stderr string, err error) {
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	kubeClient := client.NewKubeClientSet()
	req := kubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		Param("container", containerName)

	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     false,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	config := client.GetRESTConfig()

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", err
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
		Tty:    false,
	})

	if err != nil {
		return "", "", err
	}

	return stdoutBuf.String(), stderrBuf.String(), nil
}

// RunSSHCommand will execute ssh command via ssh-executor pod
func RunSSHCommand(sshExecutor *corev1.Pod, host string, user string, command []string) (stdout, stderr string, err error) {
	cmd := []string{"ssh.sh", fmt.Sprintf("%s@%s", user, host)}
	cmd = append(cmd, command...)
	return ExecuteCommandOnPod(sshExecutor, sshExecutor.Spec.Containers[0].Name, cmd)
}

// IsOpenShift returns true if we have OpenShift environment
func IsOpenShift() bool {
	kubeClient := client.NewKubeClientSet()
	result := kubeClient.RESTClient().Get().AbsPath("/version/openshift").Do()

	return result.Error() == nil
}
