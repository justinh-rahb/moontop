package moonraker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Typed API methods
// ---------------------------------------------------------------------------

// ListObjects returns the names of all printer objects registered in
// Klipper. Used to discover available heaters/sensors dynamically.
func (c *Client) ListObjects() ([]string, error) {
	result, err := c.call("printer.objects.list", nil)
	if err != nil {
		return nil, err
	}

	var body struct {
		Objects []string `json:"objects"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, err
	}

	return body.Objects, nil
}


// Subscribe subscribes to Moonraker printer object status updates.
//
// objects maps printer-object names to the list of fields to subscribe
// to. Use nil (or an empty slice) as the value to subscribe to all
// fields for that object.
//
// The map is remembered so Reconnect can replay it after a connection
// drop — callers don't need to re-subscribe themselves.
//
// Example:
//
//	err := client.Subscribe(map[string][]string{
//	    "extruder":   {"temperature", "target"},
//	    "heater_bed": nil,  // all fields
//	})
//
// The initial state snapshot is returned as a StatusUpdate.
func (c *Client) Subscribe(objects map[string][]string) (*StatusUpdate, error) {
	c.subMu.Lock()
	c.lastSubMap = objects
	c.subMu.Unlock()
	// Moonraker expects null (not an empty array) to mean "all fields",
	// so we convert []string{} / nil → JSON null.
	mapped := make(map[string]any, len(objects))
	for k, v := range objects {
		if len(v) == 0 {
			mapped[k] = nil
		} else {
			mapped[k] = v
		}
	}

	params := map[string]any{
		"objects": mapped,
	}

	result, err := c.call("printer.objects.subscribe", params)
	if err != nil {
		return nil, err
	}

	// The response contains {"eventtime": float, "status": {...}}.
	var body struct {
		EventTime float64                       `json:"eventtime"`
		Status    map[string]map[string]any     `json:"status"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, err
	}

	return &StatusUpdate{
		Objects:   body.Status,
		Timestamp: body.EventTime,
	}, nil
}

// QueryObjects queries the current state of the specified printer
// objects without subscribing to updates.
//
// See Subscribe for the objects parameter format.
func (c *Client) QueryObjects(objects map[string][]string) (*StatusUpdate, error) {
	mapped := make(map[string]any, len(objects))
	for k, v := range objects {
		if len(v) == 0 {
			mapped[k] = nil
		} else {
			mapped[k] = v
		}
	}

	params := map[string]any{
		"objects": mapped,
	}

	result, err := c.call("printer.objects.query", params)
	if err != nil {
		return nil, err
	}

	var body struct {
		EventTime float64                   `json:"eventtime"`
		Status    map[string]map[string]any `json:"status"`
	}
	if err := json.Unmarshal(result, &body); err != nil {
		return nil, err
	}

	return &StatusUpdate{
		Objects:   body.Status,
		Timestamp: body.EventTime,
	}, nil
}

// GcodeScript sends a G-Code script to Klipper for execution.
//
// Example:
//
//	err := client.GcodeScript("G28")           // home all axes
//	err := client.GcodeScript("M104 S200")     // set extruder temp
func (c *Client) GcodeScript(script string) error {
	params := map[string]any{
		"script": script,
	}

	_, err := c.call("printer.gcode.script", params)
	return err
}

// EmergencyStop sends an emergency stop command to Klipper (M112).
// This transitions the printer to a "shutdown" state immediately.
func (c *Client) EmergencyStop() error {
	_, err := c.call("printer.emergency_stop", nil)
	return err
}

// FileInfo describes a single gcode file from server.files.list.
type FileInfo struct {
	Path         string  `json:"path"`
	Size         int64   `json:"size"`
	ModifiedTime float64 `json:"modified"`
}

// ListFiles returns all gcode files in the "gcodes" root, recursively.
func (c *Client) ListFiles() ([]FileInfo, error) {
	params := map[string]any{"root": "gcodes"}
	result, err := c.call("server.files.list", params)
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	if err := json.Unmarshal(result, &files); err != nil {
		return nil, err
	}
	return files, nil
}

// StartPrint queues and begins a print of the named gcode file
// (path relative to the gcodes root).
func (c *Client) StartPrint(filename string) error {
	params := map[string]any{"filename": filename}
	_, err := c.call("printer.print.start", params)
	return err
}

// PausePrint pauses the currently running print.
func (c *Client) PausePrint() error {
	_, err := c.call("printer.print.pause", nil)
	return err
}

// ResumePrint resumes a paused print.
func (c *Client) ResumePrint() error {
	_, err := c.call("printer.print.resume", nil)
	return err
}

// CancelPrint cancels the currently running or paused print.
func (c *Client) CancelPrint() error {
	_, err := c.call("printer.print.cancel", nil)
	return err
}

// SetVelocityLimit issues a SET_VELOCITY_LIMIT gcode with whichever
// subset of parameters the caller passes as non-nil. Returns an error
// if every argument is nil — that would otherwise send a no-op gcode.
func (c *Client) SetVelocityLimit(velocity, accel, squareCornerVelocity, minCruiseRatio *float64) error {
	parts := []string{"SET_VELOCITY_LIMIT"}
	if velocity != nil {
		parts = append(parts, fmt.Sprintf("VELOCITY=%g", *velocity))
	}
	if accel != nil {
		parts = append(parts, fmt.Sprintf("ACCEL=%g", *accel))
	}
	if squareCornerVelocity != nil {
		parts = append(parts, fmt.Sprintf("SQUARE_CORNER_VELOCITY=%g", *squareCornerVelocity))
	}
	if minCruiseRatio != nil {
		parts = append(parts, fmt.Sprintf("MIN_CRUISE_RATIO=%g", *minCruiseRatio))
	}
	if len(parts) == 1 {
		return fmt.Errorf("set velocity limit: no parameters to set")
	}
	return c.GcodeScript(strings.Join(parts, " "))
}
