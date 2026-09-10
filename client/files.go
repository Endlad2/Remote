// client/files.go — передача файлов на стороне OpenWRT.
//
// Скачивание (клиент -> панель): читаем файл, шлём чанками по 32 КБ в base64.
// Загрузка (панель -> клиент): собираем чанки в память, пишем файл по завершении.
package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const fileChunkSize = 32 * 1024 // 32 КБ

// FileManager — состояние передачи файлов.
type FileManager struct {
	send func(Msg)

	mu       sync.Mutex
	upload   []byte
	upPath   string
	upSize   int64
	upSeq    int
}

func NewFileManager(send func(Msg)) *FileManager {
	return &FileManager{send: send}
}

// Download — прочитать файл на клиенте и отправить панели чанками.
func (fm *FileManager) Download(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("открыть %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if st.IsDir() {
		return fmt.Errorf("%s — это директория", path)
	}

	fm.send(Msg{Type: "file_download_start", Path: path, Size: st.Size()})

	buf := make([]byte, fileChunkSize)
	seq := 0
	for {
		n, err := f.Read(buf)
		if n > 0 {
			fm.send(Msg{
				Type: "file_download_chunk",
				Seq:  seq,
				Data: base64.StdEncoding.EncodeToString(buf[:n]),
			})
			seq++
		}
		if err != nil {
			break
		}
	}

	fm.send(Msg{Type: "file_download_end", Path: path, Size: st.Size()})
	return nil
}

// UploadStart — начало приёма файла от панели.
func (fm *FileManager) UploadStart(path string, size int64) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.upload = make([]byte, 0, size)
	fm.upPath = path
	fm.upSize = size
	fm.upSeq = 0
}

// UploadChunk — принять чанк base64.
func (fm *FileManager) UploadChunk(seq int, dataB64 string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.upPath == "" {
		return
	}
	// проверяем порядок (если пришёл не тот — всё равно кладём, но предупредим)
	if seq != fm.upSeq {
		// мягко: просто логируем через send error
		fm.send(Msg{Type: "error", Message: fmt.Sprintf("файл %s: чанк %d вне порядка (ожидался %d)", fm.upPath, seq, fm.upSeq)})
	}
	fm.upSeq = seq + 1

	b, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		fm.send(Msg{Type: "error", Message: "битый base64 в чанке: " + err.Error()})
		return
	}
	fm.upload = append(fm.upload, b...)
}

// UploadEnd — завершить приём и записать файл на диск.
func (fm *FileManager) UploadEnd() error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fm.upPath == "" {
		return fmt.Errorf("upload не был начат")
	}

	// создаём директорию, если надо
	dir := filepath.Dir(fm.upPath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}

	if err := os.WriteFile(fm.upPath, fm.upload, 0644); err != nil {
		return fmt.Errorf("записать %s: %w", fm.upPath, err)
	}

	fm.send(Msg{Type: "file_upload_end", Path: fm.upPath, Size: int64(len(fm.upload))})

	fm.upload = nil
	fm.upPath = ""
	fm.upSize = 0
	fm.upSeq = 0
	return nil
}
