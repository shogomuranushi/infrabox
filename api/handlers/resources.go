package handlers

import (
	"net/http"
	"sort"

	k8sclient "github.com/shogomuranushi/infra-box/api/k8s"
)

// --- Response types ---

type ResourceUsageResponse struct {
	Used  int64  `json:"used"`
	Quota int64  `json:"quota"`
	Unit  string `json:"unit"`
}

type UserResourcesResponse struct {
	VMCount int                   `json:"vm_count"`
	CPU     ResourceUsageResponse `json:"cpu"`
	Memory  ResourceUsageResponse `json:"memory"`
}

type NodeResourceResponse struct {
	Name   string             `json:"name"`
	Role   string             `json:"role"`
	CPU    NodeResourceDetail `json:"cpu"`
	Memory NodeResourceDetail `json:"memory"`
}

type NodeResourceDetail struct {
	Allocatable int64 `json:"allocatable"`
	Requests    int64 `json:"requests"`
	VMRequests  int64 `json:"vm_requests"` // managed-by=infrabox pods only
}

type NamespaceResourceResponse struct {
	Namespace string                `json:"namespace"`
	Owner     string                `json:"owner"`
	VMCount   int                   `json:"vm_count"`
	CPU       ResourceUsageResponse `json:"cpu"`
	Memory    ResourceUsageResponse `json:"memory"`
}

type ClusterTotals struct {
	Nodes       int   `json:"nodes"`
	TotalCPU    int64 `json:"total_cpu"`
	UsedCPU     int64 `json:"used_cpu"`
	TotalMemory int64 `json:"total_memory"`
	UsedMemory  int64 `json:"used_memory"`
	TotalVMs    int   `json:"total_vms"`
}

type AdminResourcesResponse struct {
	Nodes      []NodeResourceResponse      `json:"nodes"`
	Namespaces []NamespaceResourceResponse `json:"namespaces"`
	Totals     ClusterTotals               `json:"totals"`
}

// GetResources returns resource usage for the current user.
// GET /v1/resources
func (h *Handler) GetResources(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	namespace := k8sclient.UserNamespace(h.cfg.VMNamespace, user)

	res, err := h.k8s.GetUserResources(r.Context(), namespace)
	if err != nil {
		jsonError(w, "failed to get resources", http.StatusInternalServerError)
		return
	}

	jsonOK(w, UserResourcesResponse{
		VMCount: res.VMCount,
		CPU: ResourceUsageResponse{
			Used:  res.CPURequests,
			Quota: res.CPUQuota,
			Unit:  "millicores",
		},
		Memory: ResourceUsageResponse{
			Used:  res.MemoryRequests,
			Quota: res.MemoryQuota,
			Unit:  "bytes",
		},
	})
}

// GetAdminResources returns cluster-wide resource information.
// GET /v1/admin/resources
func (h *Handler) GetAdminResources(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		jsonError(w, "admin only", http.StatusForbidden)
		return
	}

	res, err := h.k8s.GetClusterResources(r.Context(), h.cfg.VMNamespace)
	if err != nil {
		jsonError(w, "failed to get cluster resources", http.StatusInternalServerError)
		return
	}

	resp := AdminResourcesResponse{}

	// Nodes
	for _, n := range res.Nodes {
		resp.Nodes = append(resp.Nodes, NodeResourceResponse{
			Name:   n.Name,
			Role:   n.Role,
			CPU:    NodeResourceDetail{Allocatable: n.CPUAllocatable, Requests: n.CPURequests, VMRequests: n.VMCPURequests},
			Memory: NodeResourceDetail{Allocatable: n.MemoryAllocatable, Requests: n.MemoryRequests, VMRequests: n.VMMemoryRequests},
		})
		resp.Totals.TotalCPU += n.CPUAllocatable
		resp.Totals.UsedCPU += n.CPURequests
		resp.Totals.TotalMemory += n.MemoryAllocatable
		resp.Totals.UsedMemory += n.MemoryRequests
	}
	resp.Totals.Nodes = len(res.Nodes)

	// Sort nodes by name
	sort.Slice(resp.Nodes, func(i, j int) bool {
		return resp.Nodes[i].Name < resp.Nodes[j].Name
	})

	// Namespaces (sorted by CPU usage descending)
	for _, ns := range res.Namespaces {
		resp.Namespaces = append(resp.Namespaces, NamespaceResourceResponse{
			Namespace: ns.Namespace,
			Owner:     ns.Owner,
			VMCount:   ns.VMCount,
			CPU: ResourceUsageResponse{
				Used:  ns.CPURequests,
				Quota: ns.CPUQuota,
				Unit:  "millicores",
			},
			Memory: ResourceUsageResponse{
				Used:  ns.MemoryRequests,
				Quota: ns.MemoryQuota,
				Unit:  "bytes",
			},
		})
		resp.Totals.TotalVMs += ns.VMCount
	}
	sort.Slice(resp.Namespaces, func(i, j int) bool {
		return resp.Namespaces[i].CPU.Used > resp.Namespaces[j].CPU.Used
	})

	jsonOK(w, resp)
}
