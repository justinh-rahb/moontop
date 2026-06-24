// Package moonraker provides a WebSocket JSON-RPC 2.0 client for the
// Moonraker 3D-printer management API.
//
// It handles connection, request/response correlation, and exposes
// channels for asynchronous server notifications (status updates,
// gcode responses).
package moonraker

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// JSON-RPC envelope types
// ---------------------------------------------------------------------------

// rpcRequest is the outgoing JSON-RPC 2.0 request envelope.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      int64  `json:"id"`
}

// rpcResponse is a JSON-RPC 2.0 response (matched to a request by ID).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError represents a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// rpcNotification is a server-pushed JSON-RPC notification (no id field).
type rpcNotification struct {
	JSONRPC string            `json:"jsonrpc"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// StatusUpdate carries a single diff from notify_status_update.
// Objects maps printer-object name → changed fields (arbitrary JSON).
type StatusUpdate struct {
	Objects   map[string]map[string]any
	Timestamp float64
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Client manages a WebSocket connection to a Moonraker instance.
type Client struct {
	conn *websocket.Conn
	host string

	// Auto-incrementing request ID.
	nextID atomic.Int64

	// Pending requests awaiting a response, keyed by request ID.
	mu       sync.Mutex
	pending  map[int64]chan *rpcResponse

	// Notification channels exposed to consumers.
	updates       chan StatusUpdate
	gcodeResponses chan string

	// Signals the read loop has exited.
	done chan struct{}
}

// New creates a new Client connected to the given host (host:port).
// It dials ws://<host>/websocket and starts the background read loop.
func New(host string) (*Client, error) {
	url := fmt.Sprintf("ws://%s/websocket", host)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}

	c := &Client{
		conn:           conn,
		host:           host,
		pending:        make(map[int64]chan *rpcResponse),
		updates:        make(chan StatusUpdate, 64),
		gcodeResponses: make(chan string, 64),
		done:           make(chan struct{}),
	}

	go c.readLoop()

	return c, nil
}

// Updates returns a read-only channel that emits parsed
// notify_status_update notifications from Moonraker.
func (c *Client) Updates() <-chan StatusUpdate {
	return c.updates
}

// GcodeResponses returns a read-only channel that emits
// notify_gcode_response strings from Moonraker.
func (c *Client) GcodeResponses() <-chan string {
	return c.gcodeResponses
}

// Close shuts down the WebSocket connection and waits for the read loop
// to exit.
func (c *Client) Close() error {
	err := c.conn.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
	if err != nil {
		// Best-effort; the connection may already be dead.
		_ = c.conn.Close()
	}

	// Wait for readLoop to finish.
	<-c.done
	return nil
}

// ---------------------------------------------------------------------------
// Request / response plumbing
// ---------------------------------------------------------------------------

// call sends a JSON-RPC request and blocks until the matching response
// arrives (or the connection dies).
func (c *Client) call(method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)

	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}

	// Register a channel for the response before sending, so we
	// can't miss it.
	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		c.removePending(id)
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		c.removePending(id)
		return nil, fmt.Errorf("write: %w", err)
	}

	// Wait for the response (or connection close).
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("connection closed while waiting for response to %s (id=%d)", method, id)
	}
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Read loop — runs in its own goroutine
// ---------------------------------------------------------------------------

func (c *Client) readLoop() {
	defer close(c.done)
	defer c.conn.Close()

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			log.Printf("moonraker: read error (disconnected): %v", err)
			// Fail all pending requests.
			c.mu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}

		c.dispatch(msg)
	}
}

// dispatch routes an incoming message to either a pending-request
// channel or a notification handler.
func (c *Client) dispatch(raw []byte) {
	// Peek at the message to decide if it's a response (has "id") or a
	// notification (has "method" but no "id").
	var probe struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		log.Printf("moonraker: unmarshal probe: %v", err)
		return
	}

	if probe.ID != nil {
		// It's a response to one of our requests.
		var resp rpcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			log.Printf("moonraker: unmarshal response: %v", err)
			return
		}

		c.mu.Lock()
		ch, ok := c.pending[*resp.ID]
		if ok {
			delete(c.pending, *resp.ID)
		}
		c.mu.Unlock()

		if ok {
			ch <- &resp
		} else {
			log.Printf("moonraker: unexpected response id=%d", *resp.ID)
		}
		return
	}

	// It's a notification.
	var notif rpcNotification
	if err := json.Unmarshal(raw, &notif); err != nil {
		log.Printf("moonraker: unmarshal notification: %v", err)
		return
	}

	switch notif.Method {
	case "notify_status_update":
		c.handleStatusUpdate(notif.Params)
	case "notify_gcode_response":
		c.handleGcodeResponse(notif.Params)
	default:
		// Ignore unknown notifications silently — Moonraker sends
		// several we don't care about yet (notify_proc_stat_update,
		// notify_history_changed, etc.).
	}
}

// handleStatusUpdate parses a notify_status_update params array.
// params[0] = object diffs, params[1] = timestamp.
func (c *Client) handleStatusUpdate(params []json.RawMessage) {
	if len(params) < 1 {
		return
	}

	var objects map[string]map[string]any
	if err := json.Unmarshal(params[0], &objects); err != nil {
		log.Printf("moonraker: parse status update objects: %v", err)
		return
	}

	var ts float64
	if len(params) >= 2 {
		_ = json.Unmarshal(params[1], &ts)
	}

	update := StatusUpdate{
		Objects:   objects,
		Timestamp: ts,
	}

	select {
	case c.updates <- update:
	default:
		// Drop if consumer is too slow — better than blocking the
		// read loop.
		log.Println("moonraker: status update channel full, dropping")
	}
}

// handleGcodeResponse parses a notify_gcode_response params array.
// params[0] = response string.
func (c *Client) handleGcodeResponse(params []json.RawMessage) {
	if len(params) < 1 {
		return
	}

	var msg string
	if err := json.Unmarshal(params[0], &msg); err != nil {
		log.Printf("moonraker: parse gcode response: %v", err)
		return
	}

	select {
	case c.gcodeResponses <- msg:
	default:
		log.Println("moonraker: gcode response channel full, dropping")
	}
}
