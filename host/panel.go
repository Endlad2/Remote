// host/panel.go — представление подключённой панели (браузера).
package main

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PanelConn — одно подключение браузера.
type PanelConn struct {
	hub    *Hub
	conn   *websocket.Conn
	Target string

	send      chan []byte
	closeOnce sync.Once
}

func (p *PanelConn) writeLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		p.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-p.send:
			p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				p.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := p.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (p *PanelConn) readLoop() {
	defer p.close()
	for {
		_, raw, err := p.conn.ReadMessage()
		if err != nil {
			return
		}
		m, err := decode(raw)
		if err != nil {
			log.Printf("panel: битый JSON: %v", err)
			continue
		}
		p.handle(m)
	}
}

func (p *PanelConn) handle(m Msg) {
	switch m.Type {
	case "list_clients":
		p.send(Msg{Type: "clients", Clients: p.hub.listClients()})

	case "select":
		p.Target = m.Client
		log.Printf("panel выбрала клиента %q", m.Client)
		_, ok := p.hub.clients[m.Client]
		p.send(Msg{Type: "client_status", Client: m.Client, Connected: ok})

	case "input", "signal", "file_download", "file_upload_start", "file_upload_chunk", "file_upload_end":
		if p.Target == "" {
			p.send(Msg{Type: "error", Message: "клиент не выбран"})
			return
		}
		c := p.hub.getClient(p.Target)
		if c == nil {
			p.send(Msg{Type: "error", Message: "клиент не подключён: " + p.Target})
			return
		}
		c.Send(m)

	default:
		log.Printf("panel: неизвестный тип %q", m.Type)
	}
}

func (p *PanelConn) send(m Msg) {
	b := encode(m)
	if b == nil {
		return
	}
	select {
	case p.send <- b:
	default:
		log.Printf("panel: очередь переполнена, дропаю")
	}
}

func (p *PanelConn) close() {
	p.closeOnce.Do(func() {
		close(p.send)
		p.conn.Close()
	})
}
