package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/servicemsg"
)

// The service messages (task 75): the count and the list with the device's name and its rooms
// from the metadata store, and a stream that pushes the list to an open Status page whenever it
// changes. Confirming a sticky message is a setValue over lite-rpc, which the store reads back
// (openccu-lite task 82, B-46); a message's since outlives a restart through the state store's lc.

// ServiceMessageView is one message as the page shows it.
type ServiceMessageView struct {
	servicemsg.Message
	// Name and Enums come from the metadata store's device object, where there is one; the page
	// says "<type> <address>" otherwise, as the CCU WebUI does
	Name  string   `json:"name,omitempty"`
	Enums []string `json:"enums,omitempty"`
	// EnumNames are the names of the nodes Enums name, in the same order (occulited B-66: the
	// page showed "roomgarage" for the room "Garage"); a path that no longer resolves gives its
	// last id
	EnumNames []string `json:"enum_names,omitempty"`
	// Image is the device type's small picture at the CCU's URL (occulited task 39), "" when the
	// catalogue has none
	Image string `json:"image,omitempty"`
}

func (a *SystemAPI) serviceMessageView() map[string]any {
	snap := a.ServiceMessages.List()
	list := make([]ServiceMessageView, 0, len(snap.Messages))
	for _, m := range snap.Messages {
		v := ServiceMessageView{Message: m}
		if a.DeviceImage != nil {
			v.Image = a.DeviceImage(m.Type)
		}
		if a.Names != nil {
			if name, enums, ok := a.Names(m.Interface + "." + m.Address); ok {
				v.Name, v.Enums = name, enums
				for _, e := range enums {
					v.EnumNames = append(v.EnumNames, a.enumName(e))
				}
			}
		}
		list = append(list, v)
	}
	body := map[string]any{"count": snap.Count, "messages": list, "swept": snap.Swept, "errors": snap.Errors}
	if a.RPC != nil {
		body["feed"] = a.RPC.View()
	}
	return body
}

// enumName is a node's name by its path, or its last id where the store has no name for it.
func (a *SystemAPI) enumName(path string) string {
	if a.NodeName != nil {
		if n, ok := a.NodeName(path); ok && n != "" {
			return n
		}
	}
	return path[strings.LastIndexByte(path, '/')+1:]
}

func (a *SystemAPI) serviceMessages(w http.ResponseWriter, _ *http.Request) {
	if a.ServiceMessages == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no service-message store"})
		return
	}
	writeJSON(w, 200, a.serviceMessageView())
}

// serviceMessageWatchers fans the store's OnChange out to the open streams.
type serviceMessageWatchers struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func (s *serviceMessageWatchers) Changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *serviceMessageWatchers) add() chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[chan struct{}]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *serviceMessageWatchers) remove(ch chan struct{}) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
}

// ServiceMessageWatchers is what the store's OnChange should call; the streams hang off it.
var ServiceMessageWatchers = &serviceMessageWatchers{}

// serviceMessagesStream sends the whole view as one SSE event: at once, on every change, and
// a comment every 30 s so the connection is known to be alive.
func (a *SystemAPI) serviceMessagesStream(w http.ResponseWriter, r *http.Request) {
	if a.ServiceMessages == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "no service-message store"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	ch := ServiceMessageWatchers.add()
	defer ServiceMessageWatchers.remove(ch)
	send := func() {
		b, _ := json.Marshal(a.serviceMessageView())
		fmt.Fprintf(w, "event: messages\ndata: %s\n\n", b)
		flusher.Flush()
	}
	send()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			send()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
