// Package commands implements remote command execution over agents.
package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"nodepanel/master/internal/agenthub"
	"nodepanel/master/internal/auth"
	"nodepanel/master/internal/browserhub"
	"nodepanel/master/internal/httpx"
	"nodepanel/master/internal/store"
	"nodepanel/shared/proto"
)

const maxRunDuration = time.Hour

type Service struct {
	Store   *store.Store
	Hub     *agenthub.Hub
	Browser *browserhub.Hub
}

// Run POST /api/commands {node_ids, cmd, timeout}
func (s *Service) Run(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeIDs []string `json:"node_ids"`
		Cmd     string   `json:"cmd"`
		Timeout int      `json:"timeout"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Cmd == "" || len(body.NodeIDs) == 0 {
		httpx.Err(w, 400, "node_ids and cmd are required")
		return
	}
	ids, _ := json.Marshal(body.NodeIDs)
	c := &store.Command{
		NodeIDs: string(ids), Cmd: body.Cmd, Status: "running",
		Author: auth.UserID(r.Context()), CreatedAt: time.Now().Unix(),
	}
	if err := s.Store.CreateCommand(r.Context(), c); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), c.Author, "command.run", c.Cmd)

	for _, id := range body.NodeIDs {
		go s.execNode(c.ID, id, body.Cmd, body.Timeout)
	}
	httpx.OK(w, map[string]string{"id": c.ID})
}

func (s *Service) execNode(commandID, nodeID, cmd string, timeoutSec int) {
	reqID := commandID + ":" + nodeID
	ch := s.Hub.Subscribe(reqID)
	defer s.Hub.Unsubscribe(reqID)

	env, err := proto.Encode(proto.MsgExec, reqID, proto.ExecRequest{Cmd: cmd, Timeout: timeoutSec})
	if err != nil {
		return
	}
	if err := s.Hub.Send(nodeID, env); err != nil {
		_ = s.Store.AppendCommandLine(context.Background(), commandID, nodeID, 0, "stderr", "node offline or unreachable: "+err.Error())
		s.Browser.Broadcast(browserhub.NewOut("command.output", map[string]any{
			"command_id": commandID, "node_id": nodeID, "stream": "stderr", "data": "node offline\n",
		}))
		s.Browser.Broadcast(browserhub.NewOut("command.done", map[string]any{
			"command_id": commandID, "node_id": nodeID, "exit": -1,
		}))
		return
	}

	deadline := time.Now().Add(maxRunDuration)
	exit := 0
	seq := 0
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				goto done
			}
			var out proto.ExecOutput
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &out)
			}
			if out.Data != "" {
				seq++
				_ = s.Store.AppendCommandLine(context.Background(), commandID, nodeID, seq, out.Stream, out.Data)
				s.Browser.Broadcast(browserhub.NewOut("command.output", map[string]any{
					"command_id": commandID, "node_id": nodeID, "stream": out.Stream, "data": out.Data,
				}))
			}
			if out.Done {
				exit = out.Exit
				goto done
			}
		case <-time.After(remaining):
			goto done
		}
	}
done:
	s.Browser.Broadcast(browserhub.NewOut("command.done", map[string]any{
		"command_id": commandID, "node_id": nodeID, "exit": exit,
	}))
	s.markFinished(commandID, nodeID, exit)
}

// markFinished finalises the command once all targeted nodes have reported.
var (
	finMu     sync.Mutex
	finState  = map[string]*finishTracker{}
)

type finishTracker struct {
	total   int
	exits   map[string]int
}

func (s *Service) markFinished(commandID, nodeID string, exit int) {
	finMu.Lock()
	t, ok := finState[commandID]
	if !ok {
		// determine total from the stored node_ids
		c, err := s.Store.GetCommand(context.Background(), commandID)
		if err != nil || c == nil {
			finMu.Unlock()
			return
		}
		var ids []string
		_ = json.Unmarshal([]byte(c.NodeIDs), &ids)
		t = &finishTracker{total: len(ids), exits: map[string]int{}}
		finState[commandID] = t
	}
	t.exits[nodeID] = exit
	complete := len(t.exits) >= t.total
	agg := 0
	for _, e := range t.exits {
		if e != 0 {
			agg = e
		}
	}
	if complete {
		delete(finState, commandID)
	}
	finMu.Unlock()

	if complete {
		status := "completed"
		if agg != 0 {
			status = "failed"
		}
		_ = s.Store.FinishCommand(context.Background(), commandID, status, agg)
		s.Browser.Broadcast(browserhub.NewOut("command.finished", map[string]any{
			"command_id": commandID, "status": status, "exit": agg,
		}))
	}
}

// List GET /api/commands
func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListCommands(r.Context(), 100)
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, list)
}

// Get GET /api/commands/{id}
func (s *Service) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := s.Store.GetCommand(r.Context(), id)
	if err != nil {
		httpx.Err(w, 404, "not found")
		return
	}
	lines, _ := s.Store.GetCommandLines(r.Context(), id)
	httpx.OK(w, map[string]any{"command": c, "lines": lines})
}

// ListSaved GET /api/commands/saved — reusable "常用命令" scripts.
func (s *Service) ListSaved(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListSavedCommands(r.Context())
	if err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	if list == nil {
		list = []store.SavedCommand{}
	}
	httpx.OK(w, list)
}

// CreateSaved POST /api/commands/saved {name, script}
func (s *Service) CreateSaved(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Script string `json:"script"`
	}
	if err := httpx.ReadJSON(r, &body); err != nil || body.Name == "" || body.Script == "" {
		httpx.Err(w, 400, "name and script are required")
		return
	}
	c := &store.SavedCommand{ID: uuid.NewString(), Name: body.Name, Script: body.Script}
	if err := s.Store.CreateSavedCommand(r.Context(), c); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	s.Store.Audit(r.Context(), auth.UserID(r.Context()), "command.save", body.Name)
	httpx.OK(w, c)
}

// DeleteSaved DELETE /api/commands/saved/{id} (builtins are protected)
func (s *Service) DeleteSaved(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteSavedCommand(r.Context(), id); err != nil {
		httpx.InternalErr(w, err.Error())
		return
	}
	httpx.OK(w, map[string]string{"ok": "1"})
}

// SeedBuiltins writes the built-in saved commands into the store (idempotent).
func SeedBuiltins(ctx context.Context, st *store.Store) error {
	for _, b := range builtinSavedCommands() {
		if err := st.CreateSavedCommand(ctx, &store.SavedCommand{
			ID: b.ID, Name: b.Name, Script: b.Script, Builtin: true,
		}); err != nil {
			return err
		}
	}
	return nil
}
