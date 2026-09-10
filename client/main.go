// client/main.go — клиент для OpenWRT.
//
// Устанавливает ИСХОДЯЩЕЕ WebSocket-соединение к хосту на VPS.
// Порт-форвардинг на роутере не нужен: роутер может быть за NAT,
// достаточно выхода в интернет.
//
// Держит постоянный PTY-шелл (ash/sh), стримит вывод построчно,
// обрабатывает SIGINT (CTRL+C) и передаёт файлы.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Дефолт — твой VPS.
const defaultHost = "ws://31.77.148.203:8080/ws/client"

// Msg — зеркало host/messages.go.
type Msg struct {
	Type string `json:"type"`

	Data   string `json:"data,omitempty"`
	Signal string `json:"signal,omitempty"`

	Name    string `json:"name,omitempty"`
	OS      string `json:"os,omitempty"`
	Version string `json:"version,omitempty"`

	Client    string `json:"client,omitempty"`
	Connected bool   `json:"connected,omitempty"`

	Path string `json:"path,omitempty"`
	Size int64  `json:"size,omitempty"`
	Seq  int    `json:"seq,omitempty"`

	Message string `json:"message,omitempty"`
}

func main() {
	host := flag.String("host", defaultHost, "URL хоста, напр. ws://31.77.148.203:8080/ws/client")
	name := flag.String("name", "", "имя устройства (по умолчанию hostname)")
	shell := flag.String("shell", "/bin/ash", "путь к шеллу")
	token := flag.String("token", "", "зарезервировано под auth")
	flag.Parse()

	_ = token // пока не используется

	if *name == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "openwrt"
		}
		*name = h
	}

	// выбираем шелл с fallback
	sh := pickShell(*shell)
	log.Printf("OWRT Console client: имя=%q shell=%q host=%q", *name, sh, *host)

	// корректное завершение по SIGTERM/SIGINT самого клиента
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("получен сигнал завершения, выходим")
		os.Exit(0)
	}()

	// бесконечный цикл реконнекта
	delay := time.Second
	const maxDelay = 30 * time.Second

	for {
		err := runOnce(*host, *name, sh)
		if err != nil {
			log.Printf("сессия завершилась: %v", err)
		}
		// добавляем джиттер, чтобы не долбить хост синхронно
		jitter := time.Duration(rand.Intn(500)) * time.Millisecond
		wait := delay + jitter
		log.Printf("переподключение через %s", wait)
		time.Sleep(wait)

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// runOnce — одна сессия: коннект, hello, шелл, цикл сообщений.
func runOnce(host, name, shell string) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	conn, _, err := dialer.Dial(host, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Printf("подключился к %s", host)

	// единственный писатель в сокет
	out := make(chan Msg, 256)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case m, ok := <-out:
				if !ok {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				b, _ := json.Marshal(m)
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	send := func(m Msg) {
		select {
		case out <- m:
		default:
			log.Printf("очередь отправки переполнена, дропаю %s", m.Type)
		}
	}

	// hello
	send(Msg{Type: "hello", Name: name, OS: "openwrt", Version: "1.0"})

	// поднимаем PTY-шелл
	sh, err := NewShell(shell, func(line string) {
		send(Msg{Type: "output", Data: line})
	})
	if err != nil {
		send(Msg{Type: "error", Message: "не могу запустить шелл: " + err.Error()})
		return err
	}
	defer sh.Close()

	// файловый менеджер
	fm := NewFileManager(func(m Msg) { send(m) })

	// цикл чтения от хоста
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var m Msg
		if err := json.Unmarshal(raw, &m); err != nil {
			log.Printf("битый JSON от хоста: %v", err)
			continue
		}
		switch m.Type {
		case "input":
			if err := sh.Write(m.Data); err != nil {
				log.Printf("запись в PTY: %v", err)
			}
		case "signal":
			switch m.Signal {
			case "SIGINT":
				if err := sh.SignalINT(); err != nil {
					log.Printf("SIGINT: %v", err)
				}
			default:
				log.Printf("неизвестный сигнал %q", m.Signal)
			}
		case "file_download":
			if err := fm.Download(m.Path); err != nil {
				send(Msg{Type: "error", Message: err.Error()})
			}
		case "file_upload_start":
			fm.UploadStart(m.Path, m.Size)
		case "file_upload_chunk":
			fm.UploadChunk(m.Seq, m.Data)
		case "file_upload_end":
			if err := fm.UploadEnd(); err != nil {
				send(Msg{Type: "error", Message: err.Error()})
			}
		default:
			log.Printf("неизвестный тип от хоста: %q", m.Type)
		}
	}
}

// pickShell — выбирает существующий шелл из кандидатов.
func pickShell(preferred string) string {
	candidates := []string{preferred, "/bin/ash", "/bin/sh", "/usr/bin/sh"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return "/bin/sh" // в крайнем случае
}
