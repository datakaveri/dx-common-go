package response

import (
	"encoding/json"
	"net/http"
)

// streamFlushEvery bounds how many items are written between flushes so the
// client sees steady progress without a syscall per item.
const streamFlushEvery = 64

// StreamJSONArray writes a JSON document containing one large array without
// buffering it: prologue, the items produced by next (comma-separated), then
// epilogue. next returns (item, more, err); the stream ends when more is
// false or err is non-nil.
//
// This is the sanctioned DX-envelope exemption: bulk documents (large GeoJSON
// FeatureCollections, exports) may skip the DxResponse envelope because
// buffering them to wrap in `result` would defeat the point — error handling
// must then happen BEFORE calling this (the status line is already sent once
// streaming starts). A mid-stream error aborts the document (leaving it
// truncated/invalid, which the client detects as a parse failure) and is
// returned for logging.
//
// Example (GeoJSON):
//
//	StreamJSONArray(w, 200, "application/geo+json",
//	    `{"type":"FeatureCollection","features":[`, `]}`, next)
func StreamJSONArray(w http.ResponseWriter, statusCode int, contentType, prologue, epilogue string, next func() (any, bool, error)) error {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(statusCode)

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	if _, err := w.Write([]byte(prologue)); err != nil {
		return err
	}

	enc := json.NewEncoder(w)
	first := true
	sinceFlush := 0
	for {
		item, more, err := next()
		if err != nil {
			return err
		}
		if !more {
			break
		}
		if !first {
			if _, err := w.Write([]byte(",")); err != nil {
				return err
			}
		}
		first = false
		// Encoder appends a newline after each value; harmless inside a JSON
		// array and keeps the output diffable.
		if err := enc.Encode(item); err != nil {
			return err
		}
		if sinceFlush++; sinceFlush >= streamFlushEvery {
			sinceFlush = 0
			flush()
		}
	}

	if _, err := w.Write([]byte(epilogue)); err != nil {
		return err
	}
	flush()
	return nil
}
