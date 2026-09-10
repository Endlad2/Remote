// host/main.go — HTTP-сервер хоста (крутится на VPS).
//
// Отдаёт веб-панель на "/" и держит два WebSocket-эндпоинта:
//   /ws/client  — сюда подключаются OpenWRT-устройства (исходящим соединением)
//   /ws/panel   — сюда подключается браузер (панель)
//
// REST API нет: команды, вывод и файлы ходят только по WebSocket.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	addr := flag.String("addr", ":8080", "адрес HTTP-сервера, напр. :8080")
	logPath := flag.String("log", "host.log", "файл лога на хосте")
	panelDir := flag.String("panel", "web", "папка со статикой панели")
	flag.Parse()

	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("не могу открыть лог-файл %s: %v", *logPath, err)
	}
	defer logFile.Close()
	log.SetOutput(&multiWriter{writers: []writerIface{os.Stdout, logFile}})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("OWRT Console host запускается на %s", *addr)

	hub := NewHub()
	go hub.Run()

	mux := http.NewServeMux()

	absPanel, _ := filepath.Abs(*panelDir)
	log.Printf("панель: %s", absPanel)
	mux.Handle("/", http.FileServer(http.Dir(absPanel)))

	mux.HandleFunc("/ws/client", hub.HandleClientWS)
	mux.HandleFunc("/ws/panel", hub.HandlePanelWS)

	log.Printf("готов. открой http://<host>%s/", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("HTTP-сервер упал: %v", err)
	}
}

type writerIface interface {
	Write(p []byte) (int, error)
}

type multiWriter struct {
	writers []writerIface
}

func (m *multiWriter) Write(p []byte) (int, error) {
	for _, w := range m.writers {
		if _, err := w.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
