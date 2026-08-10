// Package container implements container inventory, ops (update/restart/start/
// stop), and display-name endpoints.
package container

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

type Service struct {
	Hub   *agenthub.Hub
	Store *store.Store
}

var containerActions = map[string]struct{}{
	"update":  {},
	"restart": {},
	"start":   {},
	"stop":    {},
	"rebuild": {},
	"upgrade": {},
	"delete":  {},
}

// List GET /api/containers — latest container inventory across all nodes.
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListContainers(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	if list == nil {
		list = []store.Container{}
	}
	httpx.OK(w, list)
}

// SetName PUT /api/containers/name {node_id, name, display_name}
func (s *Service) SetName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID      string `json:"node_id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" || body.Name == "" {
		httpx.Err(w, 400, "node_id and name required")
		return
	}
	if err := s.Store.SetContainerName(r.Context(), body.NodeID, body.Name, body.DisplayName); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// Action POST /api/containers/action {node_id, ids:[], action, label}
// ids may be empty only for update, where it means all running containers.
func (s *Service) Action(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID   string   `json:"node_id"`
		IDs      []string `json:"ids"`
		Action   string   `json:"action"`
		Label    string   `json:"label"`
		NewImage string   `json:"new_image"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" || body.Action == "" {
		httpx.Err(w, 400, "node_id and action required")
		return
	}
	if err := validateContainerAction(body.Action, body.IDs); err != nil {
		httpx.Err(w, 400, err.Error())
		return
	}
	res, err := s.dispatch(body.NodeID, body.Action, body.IDs, body.Label, body.NewImage, 10*time.Minute)
	if err != nil {
		s.auditContainerAction(r.Context(), body.NodeID, body.Action, len(body.IDs), 0, 1)
		httpx.Err(w, err.code, err.msg)
		return
	}
	s.auditContainerAction(r.Context(), body.NodeID, body.Action, len(body.IDs), len(res.Updated), containerResultFailureCount(res))
	httpx.OK(w, res)
}

// Update POST /api/container/update {node_id, label} — legacy "update all".
func (s *Service) Update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string `json:"node_id"`
		Label  string `json:"label"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.NodeID == "" {
		httpx.Err(w, 400, "node_id required")
		return
	}
	res, err := s.dispatch(body.NodeID, "update", nil, body.Label, "", 10*time.Minute)
	if err != nil {
		s.auditContainerAction(r.Context(), body.NodeID, "update", 0, 0, 1)
		httpx.Err(w, err.code, err.msg)
		return
	}
	s.auditContainerAction(r.Context(), body.NodeID, "update", 0, len(res.Updated), containerResultFailureCount(res))
	httpx.OK(w, res)
}

type httpErr struct {
	code int
	msg  string
}

func errf(code int, msg string) *httpErr { return &httpErr{code: code, msg: msg} }

func validateContainerAction(action string, ids []string) error {
	if _, ok := containerActions[action]; !ok {
		return fmt.Errorf("unsupported container action %q", action)
	}
	if action != "update" && len(ids) == 0 {
		return fmt.Errorf("ids required for %s", action)
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("container ids must not be empty")
		}
	}
	return nil
}

// dispatch sends a container op to a node's agent and waits for the result.
func (s *Service) dispatch(nodeID, action string, ids []string, label, newImage string, timeout time.Duration) (proto.ContainerResult, *httpErr) {
	if err := validateContainerAction(action, ids); err != nil {
		return proto.ContainerResult{}, errf(http.StatusBadRequest, err.Error())
	}
	if !s.Hub.Online(nodeID) {
		return proto.ContainerResult{}, errf(409, "node offline")
	}
	reqID := "cop:" + nodeID + ":" + time.Now().Format("150405.000000")
	ch := s.Hub.Subscribe(reqID)
	env, encodeErr := proto.Encode(proto.MsgContainerOp, reqID, proto.ContainerOpRequest{Action: action, IDs: ids, Label: label, NewImage: newImage})
	if encodeErr != nil {
		s.Hub.Unsubscribe(reqID)
		return proto.ContainerResult{}, errf(http.StatusInternalServerError, encodeErr.Error())
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		s.Hub.Unsubscribe(reqID)
		return proto.ContainerResult{}, errf(502, err.Error())
	}
	defer s.Hub.Unsubscribe(reqID)
	select {
	case msg, ok := <-ch:
		if !ok {
			return proto.ContainerResult{}, errf(504, "agent disconnected")
		}
		res, decodeErr := decodeContainerResult(msg.Data)
		if decodeErr != nil {
			return proto.ContainerResult{}, decodeErr
		}
		if invalidatesScanCache(action) {
			if len(ids) == 0 {
				_ = s.Store.InvalidateContainerScan(context.Background(), nodeID)
			} else {
				refs := append(append([]string{}, ids...), res.Updated...)
				_ = s.Store.InvalidateContainerScanContainers(context.Background(), nodeID, refs)
			}
		}
		return res, nil
	case <-time.After(timeout):
		return proto.ContainerResult{}, errf(504, "operation timed out")
	}
}

func decodeContainerResult(data json.RawMessage) (proto.ContainerResult, *httpErr) {
	var res proto.ContainerResult
	if len(data) == 0 {
		return res, errf(http.StatusBadGateway, "agent returned an empty container result")
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return res, errf(http.StatusBadGateway, "invalid agent container result: "+err.Error())
	}
	if res.Err != "" {
		return res, errf(http.StatusBadGateway, res.Err)
	}
	for name, detail := range res.Details {
		lower := strings.ToLower(strings.TrimSpace(detail))
		if !strings.HasPrefix(lower, "error:") && !strings.HasPrefix(lower, "failed:") {
			continue
		}
		if res.Failed == nil {
			res.Failed = make(map[string]string)
		}
		if _, exists := res.Failed[name]; !exists {
			res.Failed[name] = detail
		}
	}
	if len(res.Failed) > 0 {
		res.OK = false
	}
	return res, nil
}

func invalidatesScanCache(action string) bool {
	return action == "update" || action == "upgrade" || action == "rebuild"
}

func auditActor(ctx context.Context) string {
	if actor := auth.UserID(ctx); actor != "" {
		return actor
	}
	return "admin"
}

func (s *Service) auditContainerAction(ctx context.Context, nodeID, action string, targetCount, updated, failed int) {
	scope := "selected"
	if action == "update" && targetCount == 0 {
		scope = "all"
	}
	s.Store.Audit(ctx, auditActor(ctx), "container."+action,
		fmt.Sprintf("node=%s action=%s scope=%s target_count=%d updated=%d failed=%d", nodeID, action, scope, targetCount, updated, failed))
}

func containerResultFailureCount(res proto.ContainerResult) int {
	names := make(map[string]struct{}, len(res.Failed))
	for name := range res.Failed {
		names[name] = struct{}{}
	}
	for name, detail := range res.Details {
		lower := strings.ToLower(strings.TrimSpace(detail))
		if strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "failed:") {
			names[name] = struct{}{}
		}
	}
	if len(names) == 0 && !res.OK {
		return 1
	}
	return len(names)
}

type scanNodeIssue struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Reason   string `json:"reason"`
}

type scanCoverage struct {
	TotalNodes int             `json:"total_nodes"`
	Attempted  int             `json:"attempted"`
	Succeeded  int             `json:"succeeded"`
	Failed     []scanNodeIssue `json:"failed"`
	Skipped    []scanNodeIssue `json:"skipped"`
}

type scanUpdatesResponse struct {
	Items    []proto.ContainerScanItem `json:"items"`
	Coverage scanCoverage              `json:"coverage"`
}

// ScanUpdates POST /api/containers/scan-updates — asks every online node to
// assess its containers (update_type, registry-newer check, convertibility) and
// returns the aggregated report. Read-only on the nodes (no pull, no recreate).
func (s *Service) ScanUpdates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = scanUpdatesResponse{
			Items: []proto.ContainerScanItem{},
			Coverage: scanCoverage{
				TotalNodes: len(nodes),
				Failed:     []scanNodeIssue{},
				Skipped:    []scanNodeIssue{},
			},
		}
	)
	for _, n := range nodes {
		if !scanSupported(n.AgentVersion) {
			_ = s.Store.InvalidateContainerScan(ctx, n.ID)
			out.Coverage.Skipped = append(out.Coverage.Skipped, scanNodeIssue{
				NodeID: n.ID, NodeName: n.Name, Reason: "agent " + n.AgentVersion + " must be upgraded to 2.4.0 for read-only update scans",
			})
			continue
		}
		if n.Status != "online" || !s.Hub.Online(n.ID) {
			out.Coverage.Skipped = append(out.Coverage.Skipped, scanNodeIssue{
				NodeID: n.ID, NodeName: n.Name, Reason: "node offline",
			})
			continue
		}
		out.Coverage.Attempted++
		wg.Add(1)
		go func(n store.Node) {
			defer wg.Done()
			items, err := s.scanNode(ctx, n.ID, n.AgentVersion, 90*time.Second)
			if err != nil {
				cacheErr := s.Store.InvalidateContainerScan(context.Background(), n.ID)
				missingDocker := isMissingDockerSocketScanError(err.msg)
				reason := err.msg
				if cacheErr != nil {
					reason += "; scan cache clear failed: " + cacheErr.Error()
				}
				mu.Lock()
				if missingDocker && cacheErr == nil {
					out.Coverage.Skipped = append(out.Coverage.Skipped, scanNodeIssue{
						NodeID: n.ID, NodeName: n.Name, Reason: missingDockerSocketReason,
					})
				} else {
					out.Coverage.Failed = append(out.Coverage.Failed, scanNodeIssue{
						NodeID: n.ID, NodeName: n.Name, Reason: reason,
					})
				}
				mu.Unlock()
				return
			}
			if err := s.Store.UpdateContainerScan(ctx, n.ID, items); err != nil {
				cacheErr := s.Store.InvalidateContainerScan(context.Background(), n.ID)
				reason := "cache update failed: " + err.Error()
				if cacheErr != nil {
					reason += "; scan cache clear failed: " + cacheErr.Error()
				}
				mu.Lock()
				out.Coverage.Failed = append(out.Coverage.Failed, scanNodeIssue{
					NodeID: n.ID, NodeName: n.Name, Reason: reason,
				})
				mu.Unlock()
				return
			}
			mu.Lock()
			out.Coverage.Succeeded++
			for i := range items {
				items[i].NodeID = n.ID
			}
			out.Items = append(out.Items, items...)
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].NodeID == out.Items[j].NodeID {
			return out.Items[i].Name < out.Items[j].Name
		}
		return out.Items[i].NodeID < out.Items[j].NodeID
	})
	sort.Slice(out.Coverage.Failed, func(i, j int) bool { return out.Coverage.Failed[i].NodeID < out.Coverage.Failed[j].NodeID })
	sort.Slice(out.Coverage.Skipped, func(i, j int) bool { return out.Coverage.Skipped[i].NodeID < out.Coverage.Skipped[j].NodeID })
	s.Store.Audit(ctx, auditActor(ctx), "container.scan_updates",
		fmt.Sprintf("total_nodes=%d attempted=%d succeeded=%d failed=%d skipped=%d items=%d",
			out.Coverage.TotalNodes, out.Coverage.Attempted, out.Coverage.Succeeded,
			len(out.Coverage.Failed), len(out.Coverage.Skipped), len(out.Items)))
	httpx.OK(w, out)
}

const missingDockerSocketReason = "未安装或未启用 Docker（未找到 /var/run/docker.sock）"

func isMissingDockerSocketScanError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "/var/run/docker.sock") &&
		strings.Contains(message, "no such file or directory")
}

// scanNode sends MsgContainerScan to a node and waits for the report.
func (s *Service) scanNode(ctx context.Context, nodeID, agentVersion string, timeout time.Duration) ([]proto.ContainerScanItem, *httpErr) {
	if !s.Hub.Online(nodeID) {
		return nil, errf(409, "node offline")
	}
	reqID := "scan:" + nodeID + ":" + time.Now().Format("150405.000000")
	ch := s.Hub.Subscribe(reqID)
	env, _ := proto.Encode(proto.MsgContainerScan, reqID, nil)
	if err := s.Hub.Send(nodeID, env); err != nil {
		s.Hub.Unsubscribe(reqID)
		return nil, errf(502, err.Error())
	}
	defer s.Hub.Unsubscribe(reqID)
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil, errf(504, "agent disconnected")
		}
		return decodeContainerScanResult(msg.Data, agentVersion)
	case <-time.After(timeout):
		return nil, errf(504, "scan timed out")
	case <-ctx.Done():
		return nil, errf(504, ctx.Err().Error())
	}
}

func decodeContainerScanResult(data json.RawMessage, agentVersion string) ([]proto.ContainerScanItem, *httpErr) {
	if len(data) == 0 {
		return nil, errf(http.StatusBadGateway, "agent returned an empty scan result")
	}
	var wire struct {
		OK    *bool                     `json:"ok"`
		Err   string                    `json:"err,omitempty"`
		Items []proto.ContainerScanItem `json:"items"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, errf(http.StatusBadGateway, "invalid agent scan result: "+err.Error())
	}
	if wire.Err != "" {
		return nil, errf(http.StatusBadGateway, wire.Err)
	}
	if wire.OK != nil && !*wire.OK {
		return nil, errf(http.StatusBadGateway, "agent reported scan failure")
	}
	if wire.OK == nil && agentVersionAtLeast(agentVersion, 2, 4, 0) {
		return nil, errf(http.StatusBadGateway, "agent scan result is missing ok")
	}
	if wire.Items == nil {
		wire.Items = []proto.ContainerScanItem{}
	}
	return wire.Items, nil
}

// scanSupported requires the first agent release whose scan is guaranteed
// read-only. Earlier agents understand MsgContainerScan but may pull images as
// part of detection, so the master must never invoke their scanner.
func scanSupported(version string) bool {
	return agentVersionAtLeast(version, 2, 4, 0)
}

func agentVersionAtLeast(version string, wantMajor, wantMinor, wantPatch int) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	version = strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	got := [3]int{}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return false
		}
		got[i] = n
	}
	want := [3]int{wantMajor, wantMinor, wantPatch}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return true
}
