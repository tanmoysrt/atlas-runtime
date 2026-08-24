package main

import (
	"context"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Console reads Firecracker serial output into a ring buffer and broadcasts
// it to WebSocket clients. It also writes keyboard input back to the serial FIFO.
type Console struct {
	logPath          string
	ring             []byte
	position         int
	wrapped          bool
	mutex            sync.Mutex
	connections      map[*websocket.Conn]bool
	serialOutputPath string
	serialInputPath  string
	cancel           context.CancelFunc
	logFile          *os.File
}

// NewConsole creates a console logger with a 1 MiB ring buffer.
func NewConsole(logPath string) *Console {
	return &Console{
		logPath:     logPath,
		ring:        make([]byte, 1<<20),
		connections: make(map[*websocket.Conn]bool),
	}
}

// Attach opens the serial FIFOs and starts a background reader.
func (console *Console) Attach(serialOutputPath, serialInputPath string) error {
	console.serialOutputPath = serialOutputPath
	console.serialInputPath = serialInputPath

	file, err := os.OpenFile(console.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	console.logFile = file

	// Open the FIFO with O_NONBLOCK so we don't block if Firecracker hasn't opened the write end yet.
	fifo, err := os.OpenFile(serialOutputPath, os.O_RDONLY|syscall.O_NONBLOCK, 0644)
	if err != nil {
		file.Close()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	console.cancel = cancel
	go console.readLoop(ctx, fifo)
	return nil
}

// Detach stops the background reader and closes the log file.
func (console *Console) Detach() error {
	if console.cancel != nil {
		console.cancel()
		console.cancel = nil
	}
	if console.logFile != nil {
		console.logFile.Close()
		console.logFile = nil
	}
	return nil
}

func (console *Console) readLoop(ctx context.Context, file *os.File) {
	buffer := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			file.Close()
			return
		default:
		}

		bytesRead, err := file.Read(buffer)
		// O_NONBLOCK FIFO: Read returns EAGAIN when no data is ready and
		// EOF when no writer is open. Both are temporary, so we retry.
		if err == syscall.EAGAIN {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err != nil && err != io.EOF {
			file.Close()
			return
		}
		if bytesRead == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		console.write(buffer[:bytesRead])
	}
}

// write stores bytes in the ring buffer, appends to console.log, and broadcasts to WebSockets.
func (console *Console) write(data []byte) {
	// The caller reuses its read buffer, so copy the payload before the ring
	// loop consumes data.
	payload := append([]byte{}, data...)
	console.mutex.Lock()
	for len(data) > 0 {
		n := copy(console.ring[console.position:], data)
		console.position += n
		data = data[n:]
		if console.position >= len(console.ring) {
			console.position = 0
			console.wrapped = true
		}
	}
	connections := make([]*websocket.Conn, 0, len(console.connections))
	for connection := range console.connections {
		connections = append(connections, connection)
	}
	console.mutex.Unlock()

	if console.logFile != nil {
		console.logFile.Write(payload)
	}
	for _, connection := range connections {
		connection.WriteMessage(websocket.BinaryMessage, payload)
	}
}

// ReadRing returns the entire ring buffer content in chronological order.
func (console *Console) ReadRing() []byte {
	console.mutex.Lock()
	defer console.mutex.Unlock()
	if !console.wrapped {
		return append([]byte{}, console.ring[:console.position]...)
	}
	return append(console.ring[console.position:], console.ring[:console.position]...)
}

// AddConn registers a WebSocket client to receive future serial output.
func (console *Console) AddConn(connection *websocket.Conn) {
	console.mutex.Lock()
	console.connections[connection] = true
	console.mutex.Unlock()
}

// RemoveConn unregisters a WebSocket client.
func (console *Console) RemoveConn(connection *websocket.Conn) {
	console.mutex.Lock()
	delete(console.connections, connection)
	console.mutex.Unlock()
}

// WriteInput forwards user keystrokes from the WebSocket into the serial input FIFO.
func (console *Console) WriteInput(data []byte) error {
	file, err := os.OpenFile(console.serialInputPath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
