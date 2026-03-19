package k8s

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
)

var pipeGVR = schema.GroupVersionResource{
	Group:   "sshpiper.com",
	Version: "v1beta1",
	Resource: "pipes",
}

type VMConfig struct {
	Name               string
	Namespace          string
	SSHPiperNamespace  string
	StorageClass       string
	BaseImage          string
	IngressClass       string
	IngressHost        string
	UserPubKey         string
	UpstreamSecretName string
	AuthURL            string // e.g. "https://auth.infrabox.abejatech.com" - if set, adds oauth2-proxy auth annotations
}

// CreateVM creates all K8s resources for a VM.
func (c *Client) CreateVM(ctx context.Context, cfg VMConfig) error {
	if err := c.createDeployment(ctx, cfg); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}
	if err := c.createPVC(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("pvc: %w", err)
	}
	if err := c.createService(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		c.deletePVC(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("service: %w", err)
	}
	if err := c.createIngress(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		c.deletePVC(ctx, cfg.Namespace, cfg.Name)
		c.deleteService(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("ingress: %w", err)
	}
	if err := c.createPipe(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		c.deletePVC(ctx, cfg.Namespace, cfg.Name)
		c.deleteService(ctx, cfg.Namespace, cfg.Name)
		c.deleteIngress(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("pipe: %w", err)
	}
	return nil
}

// DeleteVM deletes all K8s resources for a VM.
func (c *Client) DeleteVM(ctx context.Context, namespace, sshPiperNS, name string) error {
	c.deletePipe(ctx, sshPiperNS, name)
	c.deleteIngress(ctx, namespace, name)
	c.deleteService(ctx, namespace, name)
	c.deleteDeployment(ctx, namespace, name)
	c.deletePVC(ctx, namespace, name)
	return nil
}

// RestartVM deletes the Pod so the Deployment recreates it.
func (c *Client) RestartVM(ctx context.Context, namespace, name string) error {
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=vm-" + name,
	})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if err := c.Clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// WaitForPodReady waits until the VM pod is ready (up to timeoutSec seconds).
func (c *Client) WaitForPodReady(ctx context.Context, namespace, name string, timeoutSec int64) error {
	watcher, err := c.Clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector:  "app=vm-" + name,
		TimeoutSeconds: &timeoutSec,
	})
	if err != nil {
		return err
	}
	defer watcher.Stop()
	for event := range watcher.ResultChan() {
		pod, ok := event.Object.(*corev1.Pod)
		if !ok {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return nil
			}
		}
	}
	return fmt.Errorf("timeout waiting for pod ready")
}

// --- private helpers ---

func (c *Client) createDeployment(ctx context.Context, cfg VMConfig) error {
	_, err := c.Clientset.AppsV1().Deployments(cfg.Namespace).Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-" + cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{MatchLabels: vmLabels(cfg.Name)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: vmLabels(cfg.Name)},
				Spec: corev1.PodSpec{
					// initContainer: PVCマウント後のパーミッション修正とupstream公開鍵の設定
					InitContainers: []corev1.Container{
						{
							Name:            "setup-ssh",
							Image:           cfg.BaseImage,
							ImagePullPolicy: corev1.PullNever,
							Command: []string{"bash", "-c", `
								chown ubuntu:ubuntu /home/ubuntu && chmod 750 /home/ubuntu &&
								mkdir -p /home/ubuntu/.ssh && chmod 700 /home/ubuntu/.ssh &&
								chown ubuntu:ubuntu /home/ubuntu/.ssh &&
								ssh-keygen -y -f /run/secrets/upstream-key/ssh-privatekey > /home/ubuntu/.ssh/authorized_keys &&
								chmod 600 /home/ubuntu/.ssh/authorized_keys &&
								chown ubuntu:ubuntu /home/ubuntu/.ssh/authorized_keys
							`},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "home", MountPath: "/home/ubuntu"},
								{Name: "upstream-key", MountPath: "/run/secrets/upstream-key", ReadOnly: true},
							},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2000m"),
								corev1.ResourceMemory: resource.MustParse("8Gi"),
							},
						},
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "vm",
							Image:           cfg.BaseImage,
							ImagePullPolicy: corev1.PullNever,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 22},
								{ContainerPort: 8000},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "home", MountPath: "/home/ubuntu"},
							},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2000m"),
								corev1.ResourceMemory: resource.MustParse("8Gi"),
							},
						},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "home",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "pvc-" + cfg.Name,
								},
							},
						},
						{
							Name: "upstream-key",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName:  cfg.UpstreamSecretName,
									DefaultMode: pointer.Int32(0400),
								},
							},
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	return err
}

func (c *Client) createPVC(ctx context.Context, cfg VMConfig) error {
	storageSize, _ := resource.ParseQuantity("20Gi")
	_, err := c.Clientset.CoreV1().PersistentVolumeClaims(cfg.Namespace).Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pvc-" + cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &cfg.StorageClass,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}, metav1.CreateOptions{})
	return err
}

func (c *Client) createService(ctx context.Context, cfg VMConfig) error {
	_, err := c.Clientset.CoreV1().Services(cfg.Namespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-" + cfg.Name + "-svc",
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name),
		},
		Spec: corev1.ServiceSpec{
			Selector: vmLabels(cfg.Name),
			Ports: []corev1.ServicePort{
				{Name: "ssh", Port: 22, TargetPort: intstr.FromInt(22)},
				{Name: "http", Port: 8000, TargetPort: intstr.FromInt(8000)},
			},
		},
	}, metav1.CreateOptions{})
	return err
}

func (c *Client) createIngress(ctx context.Context, cfg VMConfig) error {
	pathType := networkingv1.PathTypePrefix
	_, err := c.Clientset.NetworkingV1().Ingresses(cfg.Namespace).Create(ctx, &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-" + cfg.Name + "-ingress",
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name),
			Annotations: ingressAnnotations(cfg),
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &cfg.IngressClass,
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{cfg.IngressHost}, SecretName: "tls-vm-" + cfg.Name},
			},
			Rules: []networkingv1.IngressRule{
				{
					Host: cfg.IngressHost,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: "vm-" + cfg.Name + "-svc",
											Port: networkingv1.ServiceBackendPort{Number: 8000},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	return err
}

func (c *Client) createPipe(ctx context.Context, cfg VMConfig) error {
	pipe := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "sshpiper.com/v1beta1",
			"kind":       "Pipe",
			"metadata": map[string]interface{}{
				"name":      "vm-" + cfg.Name,
				"namespace": cfg.SSHPiperNamespace,
			},
			"spec": map[string]interface{}{
				"from": []interface{}{
					map[string]interface{}{
						"username":             cfg.Name,
						"authorized_keys_data": cfg.UserPubKey,
					},
				},
				"to": map[string]interface{}{
					"host":           fmt.Sprintf("vm-%s-svc.%s.svc.cluster.local:22", cfg.Name, cfg.Namespace),
					"username":       "ubuntu",
					"ignore_hostkey": true,
					"private_key_secret": map[string]interface{}{
						"name": cfg.UpstreamSecretName,
					},
				},
			},
		},
	}
	_, err := c.DynamicClient.Resource(pipeGVR).Namespace(cfg.SSHPiperNamespace).Create(ctx, pipe, metav1.CreateOptions{})
	return err
}

func (c *Client) deleteDeployment(ctx context.Context, namespace, name string) {
	c.Clientset.AppsV1().Deployments(namespace).Delete(ctx, "vm-"+name, metav1.DeleteOptions{})
}

func (c *Client) deletePVC(ctx context.Context, namespace, name string) {
	c.Clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, "pvc-"+name, metav1.DeleteOptions{})
}

func (c *Client) deleteService(ctx context.Context, namespace, name string) {
	c.Clientset.CoreV1().Services(namespace).Delete(ctx, "vm-"+name+"-svc", metav1.DeleteOptions{})
}

func (c *Client) deleteIngress(ctx context.Context, namespace, name string) {
	c.Clientset.NetworkingV1().Ingresses(namespace).Delete(ctx, "vm-"+name+"-ingress", metav1.DeleteOptions{})
}

func (c *Client) deletePipe(ctx context.Context, namespace, name string) {
	c.DynamicClient.Resource(pipeGVR).Namespace(namespace).Delete(ctx, "vm-"+name, metav1.DeleteOptions{})
}

func vmLabels(name string) map[string]string {
	return map[string]string{"app": "vm-" + name, "managed-by": "infrabox"}
}

func ingressAnnotations(cfg VMConfig) map[string]string {
	ann := map[string]string{
		"cert-manager.io/cluster-issuer": "letsencrypt",
	}
	if cfg.AuthURL != "" {
		ann["nginx.ingress.kubernetes.io/auth-url"] = cfg.AuthURL + "/oauth2/auth"
		ann["nginx.ingress.kubernetes.io/auth-signin"] = cfg.AuthURL + "/oauth2/start?rd=https%3A%2F%2F$host$escaped_request_uri"
		ann["nginx.ingress.kubernetes.io/auth-response-headers"] = "X-Auth-Request-Email,X-Auth-Request-User"
	}
	return ann
}
