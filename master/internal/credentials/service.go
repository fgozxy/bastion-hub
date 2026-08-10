// Package credentials implements SSH key credential management endpoints.
package credentials

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/credutil"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

type Service struct {
	Store *store.Store
	Hub   *agenthub.Hub
}

// List GET /api/credentials
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCredentials(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, list)
}

// Create POST /api/credentials {name, pub_key, priv_key, node_id}
// Private key is required; public key is optional (derived from the private key
// when omitted, so its fingerprint/kind can still be computed and displayed).
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		PubKey  string `json:"pub_key"`
		PrivKey string `json:"priv_key"`
		NodeID  string `json:"node_id"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || strings.TrimSpace(body.PrivKey) == "" {
		httpx.Err(w, 400, "priv_key is required")
		return
	}
	pub := strings.TrimSpace(body.PubKey)
	if pub == "" {
		derived, err := credutil.DerivePubFromPriv(body.PrivKey)
		if err != nil {
			httpx.Err(w, 400, "无法从私钥推导公钥："+err.Error()+"（加密私钥请同时粘贴公钥）")
			return
		}
		pub = derived
	}
	if body.Name == "" {
		body.Name = "uploaded-key"
	}
	c := &store.Credential{
		Name: body.Name, PubKey: pub, PrivKey: body.PrivKey,
		Fingerprint: credutil.Fingerprint(pub), Kind: credutil.Kind(pub),
		Source: "manual", NodeID: body.NodeID,
	}
	if err := s.Store.CreateCredential(r.Context(), c); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), "admin", "credential.create", c.Name)
	httpx.OK(w, c)
}

// Bind POST /api/credentials/{id}/bind {node_id}
func (s *Service) Bind(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
	}
	_ = httpx.ReadJSON(r, &body)
	if err := s.Store.BindCredentialNode(r.Context(), id, body.NodeID); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// Delete DELETE /api/credentials/{id}
func (s *Service) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = s.Store.DeleteCredential(r.Context(), id)
	httpx.OK(w, map[string]string{"ok": "1"})
}

// Test POST /api/credentials/{id}/test
// REAL-ssh-tests the credential's private key against the node it is bound to:
// the master ships the key to that node's agent, which dials its own sshd and
// attempts a genuine public-key login. Requires a saved private key and a bound
// node; returns works / user / port / note.
func (s *Service) Test(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	creds, err := s.Store.ListCredentials(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	var cred *store.Credential
	for i := range creds {
		if creds[i].ID == id {
			cred = &creds[i]
			break
		}
	}
	if cred == nil {
		httpx.Err(w, 404, "credential not found")
		return
	}
	if strings.TrimSpace(cred.PrivKey) == "" {
		httpx.Err(w, 400, "该凭证未保存私钥，无法测试")
		return
	}
	if strings.TrimSpace(cred.NodeID) == "" {
		httpx.Err(w, 400, "该凭证未绑定节点，请先在「绑定节点」选择目标")
		return
	}
	port := 0
	if nodes, err := s.Store.ListNodes(r.Context()); err == nil {
		for _, n := range nodes {
			if n.ID == cred.NodeID {
				port, _ = strconv.Atoi(strings.TrimSpace(n.SshPort))
				break
			}
		}
	}
	reqID := "credtest:" + id + ":" + time.Now().Format("150405")
	env, _ := proto.Encode(proto.MsgTestSSH, reqID, proto.TestSSHRequest{
		PrivKey: cred.PrivKey, PubKey: cred.PubKey, Port: port,
	})
	msg, err := s.Hub.RequestOne(cred.NodeID, env, 30*time.Second)
	if err != nil {
		httpx.Err(w, 502, "agent unavailable: "+err.Error())
		return
	}
	var res proto.TestSSHResult
	if len(msg.Data) > 0 {
		_ = json.Unmarshal(msg.Data, &res)
	}
	s.Store.Audit(r.Context(), "admin", "credential.test", cred.Name)
	httpx.OK(w, res)
}

// ScanFromNode POST /api/credentials/scan/{nodeID}
// Asks the agent to list existing public keys; returns them for selection.
func (s *Service) ScanFromNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeID")
	reqID := "scan:" + nodeID + ":" + time.Now().Format("150405")
	env, _ := proto.Encode(proto.MsgScanSSH, reqID, nil)
	msg, err := s.Hub.RequestOne(nodeID, env, 30*time.Second)
	if err != nil {
		httpx.Err(w, 502, "agent unavailable: "+err.Error())
		return
	}
	var data proto.SSHKeysData
	if len(msg.Data) > 0 {
		_ = json.Unmarshal(msg.Data, &data)
	}
	httpx.OK(w, data.Keys)
}

// ScanFromNodes POST /api/credentials/scan-multi {node_ids:[...]}
// Scans several nodes concurrently and returns per-node results so the UI can
// multi-select / 全选 nodes and scan them in one go. Each entry carries the
// node name, any agent error, and the discovered keys.
func (s *Service) ScanFromNodes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || len(body.NodeIDs) == 0 {
		httpx.Err(w, 400, "node_ids required")
		return
	}

	nameOf := map[string]string{}
	portOf := map[string]string{}
	if nodes, err := s.Store.ListNodes(r.Context()); err == nil {
		for _, n := range nodes {
			nameOf[n.ID] = n.Name
			portOf[n.ID] = n.SshPort
		}
	}

	type nodeResult struct {
		NodeID          string         `json:"node_id"`
		Name            string         `json:"name,omitempty"`
		OK              bool           `json:"ok"`
		Error           string         `json:"error,omitempty"`
		Keys            []proto.SSHKey `json:"keys"`
		Keypairs        []proto.SSHKey `json:"keypairs,omitempty"`
		SshPort         int            `json:"ssh_port,omitempty"`
		SshDetectedPort int            `json:"ssh_detected_port,omitempty"`
		SshReachable    bool           `json:"ssh_reachable"`
		SshBanner       string         `json:"ssh_banner,omitempty"`
	}
	results := make([]nodeResult, len(body.NodeIDs))
	var wg sync.WaitGroup
	for i, id := range body.NodeIDs {
		wg.Add(1)
		go func(i int, nodeID string) {
			defer wg.Done()
			res := nodeResult{NodeID: nodeID, Name: nameOf[nodeID], Keys: []proto.SSHKey{}}
			port, _ := strconv.Atoi(strings.TrimSpace(portOf[nodeID]))
			reqID := "scan:" + nodeID + ":" + time.Now().Format("150405")
			env, _ := proto.Encode(proto.MsgScanSSH, reqID, proto.ScanSSHRequest{Port: port})
			msg, err := s.Hub.RequestOne(nodeID, env, 40*time.Second)
			if err != nil {
				res.Error = "agent unavailable: " + err.Error()
				results[i] = res
				return
			}
			var data proto.SSHKeysData
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &data)
			}
			res.OK = true
			if data.Keys != nil {
				res.Keys = data.Keys
			}
			if data.Keypairs != nil {
				res.Keypairs = data.Keypairs
			}
			res.SshPort = data.SshPort
			res.SshDetectedPort = data.SshDetectedPort
			res.SshReachable = data.SshReachable
			res.SshBanner = data.SshBanner
			results[i] = res
		}(i, id)
	}
	wg.Wait()
	httpx.OK(w, results)
}

// ImportFromNode POST /api/credentials/import {node_id, keys:[{name,pub_key,path}]}
func (s *Service) ImportFromNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string          `json:"node_id"`
		Keys   []proto.SSHKey  `json:"keys"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil {
		httpx.Err(w, 400, "invalid body")
		return
	}
	for _, k := range body.Keys {
		c := &store.Credential{
			Name: k.Name, PubKey: k.PubKey, PrivKey: k.PrivKey, Fingerprint: credutil.Fingerprint(k.PubKey),
			Kind: credutil.Kind(k.PubKey), Source: "scan:" + body.NodeID, NodeID: body.NodeID,
		}
		_ = s.Store.CreateCredential(r.Context(), c)
	}
	s.Store.Audit(context.Background(), "admin", "credential.import_scan", body.NodeID)
	httpx.OK(w, map[string]int{"imported": len(body.Keys)})
}
