package k8s

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeResources holds resource capacity and allocation for a single node.
type NodeResources struct {
	Name                string
	Role                string // infrabox-role label value (e.g. "api", "vm-worker")
	CPUAllocatable      int64  // millicores
	MemoryAllocatable   int64  // bytes
	CPURequests         int64  // millicores (all pods)
	MemoryRequests      int64  // bytes (all pods)
	VMCPURequests       int64  // millicores (managed-by=infrabox pods only)
	VMMemoryRequests    int64  // bytes (managed-by=infrabox pods only)
}

// NamespaceResources holds resource usage for a single user namespace.
type NamespaceResources struct {
	Namespace     string
	Owner         string
	VMCount       int
	CPURequests   int64 // millicores
	MemoryRequests int64 // bytes
	CPUQuota      int64 // millicores (from ResourceQuota)
	MemoryQuota   int64 // bytes
}

// ClusterResources holds the full cluster resource picture.
type ClusterResources struct {
	Nodes      []NodeResources
	Namespaces []NamespaceResources
}

// GetUserResources returns resource usage for a single user namespace.
func (c *Client) GetUserResources(ctx context.Context, namespace string) (*NamespaceResources, error) {
	res := &NamespaceResources{Namespace: namespace}

	// Get pods in namespace
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=infrabox",
	})
	if err != nil {
		return res, nil // namespace may not exist yet
	}

	vmNames := map[string]bool{}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		// Count unique VMs by app label
		if app := pod.Labels["app"]; strings.HasPrefix(app, "vm-") {
			vmNames[app] = true
		}
		for _, c := range pod.Spec.Containers {
			if cpu := c.Resources.Requests.Cpu(); cpu != nil {
				res.CPURequests += cpu.MilliValue()
			}
			if mem := c.Resources.Requests.Memory(); mem != nil {
				res.MemoryRequests += mem.Value()
			}
		}
	}
	res.VMCount = len(vmNames)

	// Get ResourceQuota
	quota, err := c.Clientset.CoreV1().ResourceQuotas(namespace).Get(ctx, "user-quota", metav1.GetOptions{})
	if err == nil {
		if cpu := quota.Spec.Hard["requests.cpu"]; cpu.MilliValue() > 0 {
			res.CPUQuota = cpu.MilliValue()
		}
		if mem := quota.Spec.Hard["requests.memory"]; mem.Value() > 0 {
			res.MemoryQuota = mem.Value()
		}
	}

	return res, nil
}

// GetClusterResources returns cluster-wide resource information.
func (c *Client) GetClusterResources(ctx context.Context, baseNamespace string) (*ClusterResources, error) {
	result := &ClusterResources{}

	// 1. Get all nodes
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	nodeMap := make(map[string]*NodeResources, len(nodes.Items))
	for _, node := range nodes.Items {
		nr := &NodeResources{
			Name:              node.Name,
			Role:              node.Labels["infrabox-role"],
			CPUAllocatable:    node.Status.Allocatable.Cpu().MilliValue(),
			MemoryAllocatable: node.Status.Allocatable.Memory().Value(),
		}
		nodeMap[node.Name] = nr
	}

	// 2. Get all pods across infrabox namespaces to calculate per-node usage
	allPods, err := c.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	for _, pod := range allPods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		nr, ok := nodeMap[pod.Spec.NodeName]
		if !ok {
			continue
		}
		isVM := pod.Labels["managed-by"] == "infrabox"
		for _, c := range pod.Spec.Containers {
			cpuVal := int64(0)
			memVal := int64(0)
			if cpu := c.Resources.Requests.Cpu(); cpu != nil {
				cpuVal = cpu.MilliValue()
			}
			if mem := c.Resources.Requests.Memory(); mem != nil {
				memVal = mem.Value()
			}
			nr.CPURequests += cpuVal
			nr.MemoryRequests += memVal
			if isVM {
				nr.VMCPURequests += cpuVal
				nr.VMMemoryRequests += memVal
			}
		}
	}

	for _, nr := range nodeMap {
		result.Nodes = append(result.Nodes, *nr)
	}

	// 3. Get infrabox user namespaces
	namespaces, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=infrabox",
	})
	if err != nil {
		return nil, err
	}

	for _, ns := range namespaces.Items {
		var owner string
		if ns.Name == baseNamespace {
			owner = "(admin)"
		} else if strings.HasPrefix(ns.Name, baseNamespace+"-") {
			owner = strings.TrimPrefix(ns.Name, baseNamespace+"-")
		} else {
			continue
		}
		nsRes, err := c.GetUserResources(ctx, ns.Name)
		if err != nil {
			continue
		}
		nsRes.Owner = owner
		result.Namespaces = append(result.Namespaces, *nsRes)
	}

	return result, nil
}
