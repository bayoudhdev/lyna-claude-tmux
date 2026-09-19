package hookevent

import "encoding/json"

// Transcript returns the transcript a hook payload names: the file Claude Code
// writes the session to, which it puts on every event. It is "" for a payload
// that names none, and for one that is not a JSON object whose transcript_path
// is a string, since such a payload does not have the documented shape.
//
// The path is returned as the payload spells it. Where it is kept, it is
// checked first: it comes from a file the hook did not write.
func Transcript(payload []byte) string {
	var p struct {
		Path string `json:"transcript_path"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	return p.Path
}
