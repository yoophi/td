package ghstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

const actionStringEncoding = "gzip+base64+strings-v1"
const actionStringRef = `{"$td_string":`

type actionStringArchive struct {
	Strings []json.RawMessage `json:"strings"`
	Data    json.RawMessage   `json:"data"`
}

// Preserve the original JSON token order and exact quoted string bytes. Only
// repeated large string tokens are factored; user bodies remain opaque strings.
func journalStringEnd(raw []byte, start int) int {
	for i := start + 1; i < len(raw); i++ {
		if raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] == '"' {
			return i + 1
		}
	}
	return len(raw)
}

func packJournalStrings(raw []byte) ([]byte, error) {
	counts := map[string]int{}
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			i++
			continue
		}
		end := journalStringEnd(raw, i)
		if end-i >= 256 {
			counts[string(raw[i:end])]++
		}
		i = end
	}
	archive := actionStringArchive{Strings: []json.RawMessage{}}
	indices := map[string]int{}
	var data bytes.Buffer
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			data.WriteByte(raw[i])
			i++
			continue
		}
		end := journalStringEnd(raw, i)
		token := raw[i:end]
		if counts[string(token)] > 1 {
			index, found := indices[string(token)]
			if !found {
				index = len(archive.Strings)
				indices[string(token)] = index
				archive.Strings = append(archive.Strings, json.RawMessage(token))
			}
			data.WriteString(actionStringRef)
			data.WriteString(strconv.Itoa(index))
			data.WriteByte('}')
		} else {
			data.Write(token)
		}
		i = end
	}
	archive.Data = data.Bytes()
	return json.Marshal(archive)
}

func unpackJournalStrings(raw []byte) ([]byte, error) {
	var archive actionStringArchive
	if err := strictJournalJSON(raw, &archive); err != nil {
		return nil, err
	}
	if archive.Strings == nil || !json.Valid(archive.Data) {
		return nil, fmt.Errorf("invalid journal string archive")
	}
	for _, token := range archive.Strings {
		var value string
		if len(token) < 256 || json.Unmarshal(token, &value) != nil {
			return nil, fmt.Errorf("invalid journal dictionary string")
		}
	}
	data := archive.Data
	var out bytes.Buffer
	appendBounded := func(value []byte) error {
		if len(value) > actionDecodedBudget-out.Len() {
			return fmt.Errorf("expanded journal exceeds decoded transport budget")
		}
		out.Write(value)
		return nil
	}
	for i := 0; i < len(data); {
		if data[i] == '"' {
			end := journalStringEnd(data, i)
			if err := appendBounded(data[i:end]); err != nil {
				return nil, err
			}
			i = end
			continue
		}
		if bytes.HasPrefix(data[i:], []byte(actionStringRef)) {
			end := bytes.IndexByte(data[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unterminated journal string reference")
			}
			token := data[i+len(actionStringRef) : i+end]
			index, err := strconv.Atoi(string(token))
			if err != nil || index < 0 || index >= len(archive.Strings) || strconv.Itoa(index) != string(token) {
				return nil, fmt.Errorf("invalid journal string reference")
			}
			if err := appendBounded(archive.Strings[index]); err != nil {
				return nil, err
			}
			i += end + 1
			continue
		}
		if err := appendBounded(data[i : i+1]); err != nil {
			return nil, err
		}
		i++
	}
	if !json.Valid(out.Bytes()) {
		return nil, fmt.Errorf("expanded journal JSON is invalid")
	}
	return out.Bytes(), nil
}
