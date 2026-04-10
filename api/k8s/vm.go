package k8s

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
)

type VMConfig struct {
	Name         string
	Namespace    string
	StorageClass string
	BaseImage    string
	IngressClass string
	IngressHost  string
	AuthURL                 string            // e.g. "https://auth.infrabox.example.com" - if set, adds oauth2-proxy auth annotations
	AuthEnabled             bool              // whether oauth2-proxy auth is active for this VM (default true)
	Owner                   string            // user who owns this VM
	NodeSelector            map[string]string // optional: schedule VM pods on specific nodes
	RcloneDriveClientID     string            // optional: OAuth client ID for rclone Google Drive sync
	RcloneDriveClientSecret string            // optional: OAuth client secret for rclone Google Drive sync
	SetupScript             string            // optional: shell script to run on first boot
}

// sanitizeOwner converts an owner string (typically email) to a value safe for
// use in Kubernetes namespace names and label values by replacing invalid characters.
func sanitizeOwner(owner string) string {
	s := strings.ToLower(owner)
	return strings.NewReplacer("@", "-", ".", "-", "_", "-").Replace(s)
}

// UserNamespace returns the per-user namespace name.
func UserNamespace(baseNamespace, owner string) string {
	if owner == "" {
		return baseNamespace
	}
	return baseNamespace + "-" + sanitizeOwner(owner)
}

// EnsureUserNamespace creates the per-user namespace, ResourceQuota, and
// default-deny NetworkPolicy if they don't exist.
func (c *Client) EnsureUserNamespace(ctx context.Context, namespace, cpuQuota, memoryQuota string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"managed-by": "infrabox",
			},
		},
	}
	_, err := c.Clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", namespace, err)
	}

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-quota",
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by": "infrabox",
			},
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				"requests.cpu":    resource.MustParse(cpuQuota),
				"requests.memory": resource.MustParse(memoryQuota),
			},
		},
	}
	_, err = c.Clientset.CoreV1().ResourceQuotas(namespace).Create(ctx, quota, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create resource quota in %s: %w", namespace, err)
	}
	if errors.IsAlreadyExists(err) {
		_, err = c.Clientset.CoreV1().ResourceQuotas(namespace).Update(ctx, quota, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("update resource quota in %s: %w", namespace, err)
		}
	}

	if err := c.ensureVMNetworkPolicy(ctx, namespace); err != nil {
		return fmt.Errorf("ensure network policy in %s: %w", namespace, err)
	}
	return nil
}

// ensureVMNetworkPolicy applies a default-deny NetworkPolicy to the given
// namespace that:
//   - Denies all ingress not explicitly permitted.
//   - Allows ingress-nginx pods (namespace: ingress-nginx) to reach VM pods on
//     port 8000.
//   - Allows intra-namespace traffic between VM pods.
//   - Allows all egress (users need unrestricted internet access).
//
// Note: kubectl exec traffic flows through the Kubernetes API server and
// kubelet, NOT over the pod network, so no ingress rule is needed for exec.
func (c *Client) ensureVMNetworkPolicy(ctx context.Context, namespace string) error {
	ingressNginxNS := "ingress-nginx"
	port8000 := intstr.FromInt(8000)
	tcpProto := corev1.ProtocolTCP

	pol := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-default-deny",
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by": "infrabox",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Target every pod in this namespace.
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				// Allow ingress-nginx -> VM pods on port 8000 only.
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": ingressNginxNS,
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app.kubernetes.io/name": "ingress-nginx",
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProto, Port: &port8000},
					},
				},
				// Allow intra-namespace traffic (pods within the same user's namespace).
				{
					From: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{}},
					},
				},
			},
			// Allow all egress.
			Egress: []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}

	_, err := c.Clientset.NetworkingV1().NetworkPolicies(namespace).Create(ctx, pol, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	if errors.IsAlreadyExists(err) {
		existing, getErr := c.Clientset.NetworkingV1().NetworkPolicies(namespace).Get(ctx, pol.Name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		pol.ResourceVersion = existing.ResourceVersion
		_, err = c.Clientset.NetworkingV1().NetworkPolicies(namespace).Update(ctx, pol, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateVM creates all K8s resources for a VM.
func (c *Client) CreateVM(ctx context.Context, cfg VMConfig) error {
	if cfg.SetupScript != "" {
		if err := c.createSetupSecret(ctx, cfg); err != nil {
			return fmt.Errorf("setup secret: %w", err)
		}
	}
	if err := c.createDeployment(ctx, cfg); err != nil {
		c.deleteSetupSecret(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("deployment: %w", err)
	}
	if err := c.createPVC(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		c.deleteSetupSecret(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("pvc: %w", err)
	}
	if err := c.createService(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		c.deletePVC(ctx, cfg.Namespace, cfg.Name)
		c.deleteSetupSecret(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("service: %w", err)
	}
	if err := c.createIngress(ctx, cfg); err != nil {
		c.deleteDeployment(ctx, cfg.Namespace, cfg.Name)
		c.deletePVC(ctx, cfg.Namespace, cfg.Name)
		c.deleteService(ctx, cfg.Namespace, cfg.Name)
		c.deleteSetupSecret(ctx, cfg.Namespace, cfg.Name)
		return fmt.Errorf("ingress: %w", err)
	}
	return nil
}

// DeleteVM deletes all K8s resources for a VM.
func (c *Client) DeleteVM(ctx context.Context, namespace, name string) error {
	c.deleteIngress(ctx, namespace, name)
	c.deleteService(ctx, namespace, name)
	c.deleteDeployment(ctx, namespace, name)
	c.deletePVC(ctx, namespace, name)
	c.deleteSetupSecret(ctx, namespace, name)
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

func vmEnv(cfg VMConfig) []corev1.EnvVar {
	var env []corev1.EnvVar
	if cfg.RcloneDriveClientID != "" {
		env = append(env,
			corev1.EnvVar{Name: "RCLONE_DRIVE_CLIENT_ID", Value: cfg.RcloneDriveClientID},
			corev1.EnvVar{Name: "RCLONE_DRIVE_CLIENT_SECRET", Value: cfg.RcloneDriveClientSecret},
		)
	}
	return env
}

func (c *Client) createDeployment(ctx context.Context, cfg VMConfig) error {
	initContainers := []corev1.Container{
		{
			Name:            "fix-perms",
			Image:           cfg.BaseImage,
			ImagePullPolicy: corev1.PullAlways,
			Command: []string{"bash", "-c",
				"chown ubuntu:ubuntu /home/ubuntu && chmod 750 /home/ubuntu",
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "home", MountPath: "/home/ubuntu"},
			},
			Resources: initResources(),
		},
	}

	volumes := []corev1.Volume{
		{
			Name: "home",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "pvc-" + cfg.Name,
				},
			},
		},
	}

	// If a setup script is configured, add an init container to run it on first boot.
	// The script runs as the ubuntu user. A marker file prevents re-execution on restart.
	if cfg.SetupScript != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "setup-script",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  "setup-" + cfg.Name,
					DefaultMode: pointer.Int32(0o755),
				},
			},
		})
		initContainers = append(initContainers, corev1.Container{
			Name:            "run-setup",
			Image:           cfg.BaseImage,
			ImagePullPolicy: corev1.PullAlways,
			Command: []string{"bash", "-c",
				`if [ ! -f /home/ubuntu/.setup-done ]; then
  echo "Running setup script..."
  su -s /bin/bash ubuntu -c /opt/setup/setup.sh
  touch /home/ubuntu/.setup-done
  echo "Setup complete."
else
  echo "Setup already completed, skipping."
fi`,
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "home", MountPath: "/home/ubuntu"},
				{Name: "setup-script", MountPath: "/opt/setup", ReadOnly: true},
			},
			Resources: initResources(),
		})
	}

	_, err := c.Clientset.AppsV1().Deployments(cfg.Namespace).Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-" + cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name, cfg.Owner),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{MatchLabels: vmLabels(cfg.Name, cfg.Owner)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: vmLabels(cfg.Name, cfg.Owner)},
				Spec: corev1.PodSpec{
					NodeSelector:   cfg.NodeSelector,
					InitContainers: initContainers,
					Containers: []corev1.Container{
						{
							Name:            "vm",
							Image:           cfg.BaseImage,
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  pointer.Int64(1000),
								RunAsGroup: pointer.Int64(1000),
							},
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8000},
							},
							Env: vmEnv(cfg),
							VolumeMounts: []corev1.VolumeMount{
								{Name: "home", MountPath: "/home/ubuntu"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("400Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}, metav1.CreateOptions{})
	return err
}

func initResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("400Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}
}

func (c *Client) createPVC(ctx context.Context, cfg VMConfig) error {
	storageSize, _ := resource.ParseQuantity("8Gi")
	_, err := c.Clientset.CoreV1().PersistentVolumeClaims(cfg.Namespace).Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pvc-" + cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name, cfg.Owner),
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
			Labels:    vmLabels(cfg.Name, cfg.Owner),
		},
		Spec: corev1.ServiceSpec{
			Selector: vmLabels(cfg.Name, cfg.Owner),
			Ports: []corev1.ServicePort{
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
			Labels:    vmLabels(cfg.Name, cfg.Owner),
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

func (c *Client) createSetupSecret(ctx context.Context, cfg VMConfig) error {
	_, err := c.Clientset.CoreV1().Secrets(cfg.Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "setup-" + cfg.Name,
			Namespace: cfg.Namespace,
			Labels:    vmLabels(cfg.Name, cfg.Owner),
		},
		Data: map[string][]byte{
			"setup.sh": []byte(cfg.SetupScript),
		},
	}, metav1.CreateOptions{})
	return err
}

func (c *Client) deleteSetupSecret(ctx context.Context, namespace, name string) {
	c.Clientset.CoreV1().Secrets(namespace).Delete(ctx, "setup-"+name, metav1.DeleteOptions{})
}

func vmLabels(name string, owner string) map[string]string {
	labels := map[string]string{"app": "vm-" + name, "managed-by": "infrabox"}
	if owner != "" {
		labels["infrabox-owner"] = sanitizeOwner(owner)
	}
	return labels
}

func ingressAnnotations(cfg VMConfig) map[string]string {
	ann := map[string]string{
		"cert-manager.io/cluster-issuer": "letsencrypt",
	}
	if cfg.AuthURL != "" && cfg.AuthEnabled {
		ann["nginx.ingress.kubernetes.io/auth-url"] = cfg.AuthURL + "/oauth2/auth"
		ann["nginx.ingress.kubernetes.io/auth-signin"] = cfg.AuthURL + "/oauth2/start?rd=https://$host$escaped_request_uri"
		ann["nginx.ingress.kubernetes.io/auth-response-headers"] = "X-Auth-Request-Email,X-Auth-Request-User"
	}
	return ann
}

// UpdateVMAuth patches the VM's Ingress to enable or disable oauth2-proxy auth annotations.
func (c *Client) UpdateVMAuth(ctx context.Context, namespace, name, authURL string, enabled bool) error {
	ingressName := "vm-" + name + "-ingress"
	ingress, err := c.Clientset.NetworkingV1().Ingresses(namespace).Get(ctx, ingressName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get ingress: %w", err)
	}

	ann := ingress.Annotations
	if ann == nil {
		ann = map[string]string{}
	}

	if enabled && authURL != "" {
		ann["nginx.ingress.kubernetes.io/auth-url"] = authURL + "/oauth2/auth"
		ann["nginx.ingress.kubernetes.io/auth-signin"] = authURL + "/oauth2/start?rd=https://$host$escaped_request_uri"
		ann["nginx.ingress.kubernetes.io/auth-response-headers"] = "X-Auth-Request-Email,X-Auth-Request-User"
	} else {
		delete(ann, "nginx.ingress.kubernetes.io/auth-url")
		delete(ann, "nginx.ingress.kubernetes.io/auth-signin")
		delete(ann, "nginx.ingress.kubernetes.io/auth-response-headers")
	}

	ingress.Annotations = ann
	_, err = c.Clientset.NetworkingV1().Ingresses(namespace).Update(ctx, ingress, metav1.UpdateOptions{})
	return err
}
