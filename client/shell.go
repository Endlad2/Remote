// client/shell.go — постоянный PTY-шелл на стороне OpenWRT.
//
// Открывает /dev/ptmx через github.com/creack/pty, запускает ash/sh,
// читает вывод построчно и отдаёт его через callback.
// SIGINT шлётся в process group PTY — шелл при этом остаётся жив.
package main

import (
	"bufio"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// Shell — обёртка над PTY-процессом.
type Shell struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	onLine func(string)

	writeMu sync.Mutex
	closed  bool
	closeMu sync.Mutex
}

// NewShell запускает шелл в PTY и стартует горутину чтения.
// onLine вызывается для каждой прочитанной строки (включая \n).
func NewShell(shellPath string, onLine func(string)) (*Shell, error) {
	cmd := exec.Command(shellPath)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PS1=\\w # ",
	)
	// своя process group — чтобы SIGINT бил по всей группе
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	s := &Shell{
		cmd:    cmd,
		ptmx:   ptmx,
		onLine: onLine,
	}
	go s.readLoop()
	return s, nil
}

// readLoop читает PTY и режет на строки.
//
// ВАЖНО: вывод шелла идёт и с эхо, и с приглашением — всё это попадает в
// панель. Панель НЕ должна дублировать эхо: см. примечание в README.
func (s *Shell) readLoop() {
	reader := bufio.NewReader(s.ptmx)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 && s.onLine != nil {
			s.onLine(line)
		}
		if err != nil {
			return
		}
	}
}

// Write отправляет данные в stdin PTY.
func (s *Shell) Write(data string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.ptmx.Write([]byte(data))
	return err
}

// SignalINT шлёт SIGINT в process group PTY.
//
// Шелл остаётся жив: SIGINT прилетает всей группе, но сам шелл его
// игнорирует/перехватывает в интерактивном режиме, а дочерний процесс
// (например, sleep, ping) — умирает.
func (s *Shell) SignalINT() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(s.cmd.Process.Pid)
	if err != nil {
		// fallback: бьём по самому процессу
		return s.cmd.Process.Signal(syscall.SIGINT)
	}
	// отрицательный pid в kill = вся группа
	return syscall.Kill(-pgid, syscall.SIGINT)
}

// Close закрывает PTY и убивает процесс.
func (s *Shell) Close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.ptmx != nil {
		s.ptmx.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
}
