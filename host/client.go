// host/client.go — представление подключённого OpenWRT-клиента на стороне хоста.
package main

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ClientConn — одно подключённое OpenWRT-устройство.
type ClientConn struct {
	hub        *Hub
	conn       *websocket.Conn
	RemoteAddr string
	Name       string

	send      chan []byte
	closeOnce sync.Once
}

func (c *ClientConn) writeLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *ClientConn) readLoop() {
	defer func() {
		if c.Name != "" {
			c.hub.unregisterClient(c)
		}
		c.close()
	}()

	// hello
	c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, raw, err := c.conn.ReadMessage()
	if err != nil {
		log.Printf("client %s: нет hello: %v", c.RemoteAddr, err)
		return
	}
	m, err := decode(raw)
	if err != nil || m.Type != "hello" || m.Name == "" {
		log.Printf("client %s: некорректное hello: %s", c.RemoteAddr, string(raw))
		return
	}
	c.Name = m.Name
	c.conn.SetReadDeadline(time.Time{})
	c.hub.registerClient(c)

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		m, err := decode(raw)
		if err != nil {
			log.Printf("client %s: битый JSON: %v", c.Name, err)
			continue
		}
		c.handle(m)
	}
}

func (c *ClientConn) handle(m Msg) {
	switch m.Type {
	case "output":
		c.hub.sendToClientPanels(c.Name, Msg{
			Type:   "output",
			Client: c.Name,
			Data:   m.Data,
		})
	case "file_download_start", "file_download_chunk", "file_download_end",
		"file_upload_start", "file_upload_chunk", "file_upload_end":
		m.Client = c.Name
		c.hub.sendToClientPanels(c.Name, m)
	case "error":
		log.Printf("client %s ошибка: %s", c.Name, m.Message)
		c.hub.sendToClientPanels(c.Name, Msg{
			Type:    "error",
			Client:  c.Name,
			Message: m.Message,
		})
	default:
		log.Printf("client %s: неизвестный тип %q", c.Name, m.Type)
	}
}

// Send — отправить сообщение клиенту (неблокирующе).
func (c *ClientConn) Send(m Msg) {
	b := encode(m)
	if b == nil {
		return
	}
	select {
	case c.send <- b:
	default:
		log.Printf("client %s: очередь отправки переполнена, дропаю", c.Name)
	}
}

func (c *ClientConn) close() {
	c.closeOnce.Do(func() {
		close(c.send)
		c.conn.Close()
	})
}
