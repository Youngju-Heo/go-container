package main

import "encoding/json"

// FileRef identifies the input file in the output envelope.
type FileRef struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Report is the common JSON envelope. Section fields are pre-marshaled JSON:
// a nil RawMessage is omitted; jsonNull emits an explicit null (a section that
// was requested but has no value, e.g. mkv index).
type Report struct {
	File   FileRef         `json:"file"`
	Format string          `json:"format"`
	Title  *string         `json:"title"`
	Header json.RawMessage `json:"header,omitempty"`
	Media  json.RawMessage `json:"media,omitempty"`
	Tags   json.RawMessage `json:"tags,omitempty"`
	Index  json.RawMessage `json:"index,omitempty"`
}

// jsonNull marks a requested section that carries no value.
var jsonNull = json.RawMessage("null")

// marshal renders the report as 2-space indented JSON. MarshalIndent reflows
// the embedded RawMessage sections, so the whole document is consistently
// indented.
func (r *Report) marshal() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
