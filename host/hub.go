// host/hub.go — центр маршрутизации.
//
// Держит реестр клиентов (OpenWRT) по имени и реестр панелей (браузеров).
// Маршрутизирует:
//   панель -> клиент:   input, signal, file_*
//   клиент -> панель:   output, file_*, client_status
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024 * 16,
	WriteBufferSize: 1024 * 16,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub — общий центр.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*ClientConn // имя -> клиент
	panels  map[*PanelConn]bool    // активные панели
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[string]*ClientConn),
		panels:  make(map[*PanelConn]bool),
	}
}

// Run — заглушка (архитектура без центрального канала).
func (h *Hub) Run() {
	<-make(chan struct{})
}

// ---------- регистрация ----------

func (h *Hub) registerClient(c *ClientConn) {
	h.mu.Lock()
	if old, ok := h.clients[c.Name]; ok {
		log.Printf("клиент %q переподключился, закрываю старый", c.Name)
		old.close()
	}
	h.clients[c.Name] = c
	h.mu.Unlock()
	log.Printf("клиент зарегистрирован: %q (%s)", c.Name, c.RemoteAddr)
	h.broadcastClientList()
	h.notifyClientStatus(c.Name, true)
}

func (h *Hub) unregisterClient(c *ClientConn) {
	h.mu.Lock()
	if cur, ok := h.clients[c.Name]; ok && cur == c {
		delete(h.clients, c.Name)
	}
	h.mu.Unlock()
	log.Printf("клиент отключён: %q", c.Name)
	h.broadcastClientList()
	h.notifyClientStatus(c.Name, false)
}

func (h *Hub) registerPanel(p *PanelConn) {
	h.mu.Lock()
	h.panels[p] = true
	h.mu.Unlock()
	p.send(Msg{Type: "clients", Clients: h.listClients()})
}

func (h *Hub) unregisterPanel(p *PanelConn) {
	h.mu.Lock()
	delete(h.panels, p)
	h.mu.Unlock()
}

// ---------- запросы ----------

func (h *Hub) listClients() []ClientInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ClientInfo, 0, len(h.clients))
	for name := range h.clients {
		out = append(out, ClientInfo{Name: name, Connected: true})
	}
	return out
}

func (h *Hub) getClient(name string) *ClientConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[name]
}

// ---------- рассылки ----------

func (h *Hub) broadcastClientList() {
	list := h.listClients()
	h.mu.RLock()
	panels := make([]*PanelConn, 0, len(h.panels))
	for p := range h.panels {
		panels = append(panels, p)
	}
	h.mu.RUnlock()
	for _, p := range panels {
		p.send(Msg{Type: "clients", Clients: list})
	}
}

func (h *Hub) notifyClientStatus(name string, connected bool) {
	h.mu.RLock()
	panels := make([]*PanelConn, 0, len(h.panels))
	for p := range h.panels {
		if p.Target == name {
			panels = append(panels, p)
		}
	}
	h.mu.RUnlock()
	for _, p := range panels {
		p.send(Msg{Type: "client_status", Client: name, Connected: connected})
	}
}

func (h *Hub) sendToClientPanels(clientName string, m Msg) {
	h.mu.RLock()
	panels := make([]*PanelConn, 0, len(h.panels))
	for p := range h.panels {
		if p.Target == clientName {
			panels = append(panels, p)
		}
	}
	h.mu.RUnlock()
	for _, p := range panels {
		p.send(m)
	}
}

// ---------- WS-хендлеры ----------

func (h *Hub) HandleClientWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade client ws: %v", err)
		return
	}
	c := &ClientConn{
		hub:        h,
		conn:       conn,
		RemoteAddr: r.RemoteAddr,
		send:       make(chan []byte, 256),
	}
	go c.writeLoop()
	c.readLoop()
}

func (h *Hub) HandlePanelWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade panel ws: %v", err)
		return
	}
	p := &PanelConn{
		hub:  h,
		conn: conn,
		send: make(chan []byte, 256),
	}
	h.registerPanel(p)
	go p.writeLoop()
	p.readLoop()
	h.unregisterPanel(p)
}

// ---------- helpers ----------

func encode(m Msg) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		log.Printf("marshal msg: %v", err)
		return nil
	}
	return b
}

func decode(b []byte) (Msg, error) {
	var m Msg
	err := json.Unmarshal(b, &m)
	return m, err
}
