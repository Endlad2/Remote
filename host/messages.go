// host/messages.go — общий формат JSON-сообщений, ходящих по WebSocket.
package main

// Msg — универсальное сообщение. Поле Type определяет, какие поля заполнены.
type Msg struct {
	Type string `json:"type"`

	// input / output
	Data string `json:"data,omitempty"`

	// signal
	Signal string `json:"signal,omitempty"`

	// hello (client -> host)
	Name    string `json:"name,omitempty"`
	OS      string `json:"os,omitempty"`
	Version string `json:"version,omitempty"`

	// clients / select
	Clients []ClientInfo `json:"clients,omitempty"`
	Client  string       `json:"client,omitempty"`

	// client_status
	Connected bool `json:"connected,omitempty"`

	// files
	Path string `json:"path,omitempty"`
	Size int64  `json:"size,omitempty"`
	Seq  int    `json:"seq,omitempty"`

	// error
	Message string `json:"message,omitempty"`
}

// ClientInfo — информация о подключённом клиенте (для списка в панели).
type ClientInfo struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}
