package moonraker

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
)

// Notification method names Moonraker pushes that consumers commonly
// route on. Not exhaustive — anything else still arrives on
// Notifications() with its raw method name.
const (
	MethodStatusUpdate       = "notify_status_update"
	MethodGcodeResponse      = "notify_gcode_response"
	MethodKlippyReady        = "notify_klippy_ready"
	MethodKlippyShutdown     = "notify_klippy_shutdown"
	MethodKlippyDisconnected = "notify_klippy_disconnected"
	MethodHistoryChanged     = "notify_history_changed"
	MethodJobQueueChanged    = "notify_job_queue_changed"
	MethodProcStatUpdate     = "notify_proc_stat_update"
	MethodServiceChanged     = "notify_service_state_changed"
	MethodPowerChanged       = "notify_power_changed"
	MethodUpdateResponse     = "notify_update_response"
	MethodUpdateRefreshed    = "notify_update_refreshed"
	MethodAnnouncementUpdate = "notify_announcement_update"
	MethodAnnouncementGone   = "notify_announcement_dismissed"
	MethodFileListChanged    = "notify_filelist_changed"
)

// ParseStatusUpdate decodes notify_status_update params:
// params[0] = object diffs, params[1] = eventtime.
func ParseStatusUpdate(params []json.RawMessage) (StatusUpdate, error) {
	if len(params) < 1 {
		return StatusUpdate{}, fmt.Errorf("status update: no params")
	}
	var objects map[string]map[string]any
	if err := json.Unmarshal(params[0], &objects); err != nil {
		return StatusUpdate{}, fmt.Errorf("status update objects: %w", err)
	}
	var ts float64
	if len(params) >= 2 {
		_ = json.Unmarshal(params[1], &ts)
	}
	return StatusUpdate{Objects: objects, Timestamp: ts}, nil
}

// ParseGcodeResponse decodes notify_gcode_response params:
// params[0] = response string.
func ParseGcodeResponse(params []json.RawMessage) (string, error) {
	if len(params) < 1 {
		return "", fmt.Errorf("gcode response: no params")
	}
	var msg string
	if err := json.Unmarshal(params[0], &msg); err != nil {
		return "", fmt.Errorf("gcode response: %w", err)
	}
	return msg, nil
}

// markupTag matches the few HTML tags macros write for Mainsail and
// Fluidd to render, e.g. AFC's <span class=info--text>…</span>.
var markupTag = regexp.MustCompile(`(?i)</?(span|b|i|u|a|br|div|p|strong|em|font|small|code)(\s[^<>]*)?/?>`)

// StripMarkup removes those tags (and, when there were any, decodes the
// HTML entities they came with). A line without tags is returned
// unchanged: "<" in "x < 5" stays. The console keeps span markup to
// colour it (internal/console/markup.go); this is for plain text.
func StripMarkup(s string) string {
	if !markupTag.MatchString(s) {
		return s
	}
	return html.UnescapeString(markupTag.ReplaceAllString(s, ""))
}
