package chain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func CanonicalMessage(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var typ string
	if v, ok := fields["@type"]; ok {
		if err := json.Unmarshal(v, &typ); err != nil || typ == "" {
			return nil, fmt.Errorf("message needs a nonempty @type")
		}
		return raw, nil
	}
	if err := json.Unmarshal(fields["type"], &typ); err != nil || typ == "" {
		return nil, fmt.Errorf("message needs @type or type and value")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(fields["value"], &value); err != nil || value == nil {
		return nil, fmt.Errorf("legacy message needs an object value")
	}
	value["@type"], _ = json.Marshal(typ)
	return normalizeLegacyValues(value)
}

func (p Proposal) Draft(deposit string) (ProposalFile, error) {
	d := ProposalFile{Title: p.Title, Summary: p.Summary, Metadata: p.Metadata, Deposit: deposit, Expedited: p.Expedited, Messages: []any{}}
	for _, raw := range p.MessagePayloads {
		msg, err := CanonicalMessage(raw)
		if err != nil {
			return d, err
		}
		d.Messages = append(d.Messages, msg)
	}
	if len(p.Messages) != len(p.MessagePayloads) {
		return d, fmt.Errorf("proposal message payloads are unavailable")
	}
	return d, nil
}

func ParseProposalDocument(data []byte) (ProposalFile, error) {
	var d ProposalFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return d, fmt.Errorf("expected one JSON document")
	}
	if strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Summary) == "" {
		return d, fmt.Errorf("title and summary are required")
	}
	if strings.TrimSpace(d.Deposit) == "" {
		return d, fmt.Errorf("deposit is required")
	}
	if d.Messages == nil {
		return d, fmt.Errorf("messages must be an array")
	}
	if len(d.Messages) == 0 && strings.TrimSpace(d.Metadata) == "" {
		return d, fmt.Errorf("metadata is required for a text proposal")
	}
	for i, msg := range d.Messages {
		raw, err := json.Marshal(msg)
		if err != nil {
			return d, err
		}
		canonical, err := CanonicalMessage(raw)
		if err != nil {
			return d, fmt.Errorf("message %d: %w", i+1, err)
		}
		d.Messages[i] = canonical
	}
	return d, nil
}

func normalizeLegacyValues(fields map[string]json.RawMessage) (json.RawMessage, error) {
	for key, raw := range fields {
		normalized, err := normalizeLegacyValue(raw)
		if err != nil {
			return nil, err
		}
		fields[key] = normalized
	}
	return json.Marshal(fields)
}

func normalizeLegacyValue(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		var typ string
		if len(object) == 2 && json.Unmarshal(object["type"], &typ) == nil && strings.HasPrefix(typ, "/") && object["value"] != nil {
			if typ == "/cosmos.crypto.ed25519.PubKey" || typ == "/cosmos.crypto.secp256k1.PubKey" {
				var key string
				if json.Unmarshal(object["value"], &key) == nil {
					at, _ := json.Marshal(typ)
					return json.Marshal(map[string]json.RawMessage{"@type": at, "key": object["value"]})
				}
			}
			var value map[string]json.RawMessage
			if json.Unmarshal(object["value"], &value) == nil && value != nil {
				return CanonicalMessage(raw)
			}
		}
		return normalizeLegacyValues(object)
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) == nil && array != nil {
		for i, v := range array {
			n, err := normalizeLegacyValue(v)
			if err != nil {
				return nil, err
			}
			array[i] = n
		}
		return json.Marshal(array)
	}
	return raw, nil
}
