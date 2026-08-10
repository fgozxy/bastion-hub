package agent

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"nodepanel/shared/proto"
)

// handleAgentUpdate downloads the latest agent binary for this host's arch from
// the panel's /dl/ endpoint, atomically replaces the running binary, then exits
// so systemd (Restart=always) relaunches the new version. The result is sent
// before exit; the reply is routed to the master by the request ID.
func (a *Agent) handleAgentUpdate(id string) {
	exePath, err := os.Executable()
	if err != nil {
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "resolve executable: " + err.Error()})
		return
	}
	dir := filepath.Dir(exePath)

	url := strings.TrimRight(a.cfg.Server, "/") + "/dl/nodepanel-agent-linux-" + agentArch()
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "download: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "download status " + resp.Status})
		return
	}

	tmp := filepath.Join(dir, ".nodepanel-agent.new")
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "open temp: " + err.Error()})
		return
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "write: " + err.Error()})
		return
	}
	out.Close()
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "chmod: " + err.Error()})
		return
	}

	// Atomic replace (same filesystem → rename works; running binary keeps its inode).
	if err := os.Rename(tmp, exePath); err != nil {
		_ = os.Remove(tmp)
		a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{Err: "replace: " + err.Error()})
		return
	}

	a.sendEnv(proto.MsgAgentUpdateResult, id, proto.AgentUpdateResult{OK: true, Version: AgentVersion})

	// Give the reply time to flush, then exit so systemd relaunches the new binary.
	go func() {
		time.Sleep(time.Second)
		os.Exit(0)
	}()
}

// agentArch maps runtime.GOARCH to the binary suffix served at /dl/.
func agentArch() string {
	if runtime.GOARCH == "arm" {
		return "arm-7"
	}
	return runtime.GOARCH
}
