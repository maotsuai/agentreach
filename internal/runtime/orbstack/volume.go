package orbstack

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

func parsePlist(data []byte) (map[string]string, error) {
	d := xml.NewDecoder(strings.NewReader(string(data)))
	values := map[string]string{}
	root := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("missing plist dictionary")
		}
		if err != nil {
			return nil, err
		}
		s, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !root && s.Name.Local == "plist" {
			root = true
			continue
		}
		if !root || s.Name.Local != "dict" {
			return nil, fmt.Errorf("expected plist root dictionary")
		}
		break
	}
	seen := map[string]bool{}
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if end, ok := tok.(xml.EndElement); ok && end.Name.Local == "dict" {
			return values, nil
		}
		s, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if s.Name.Local != "key" {
			return nil, fmt.Errorf("expected dictionary key")
		}
		var key string
		if err := d.DecodeElement(&key, &s); err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate plist key %q", key)
		}
		seen[key] = true
		for {
			tok, err = d.Token()
			if err != nil {
				return nil, err
			}
			if s, ok = tok.(xml.StartElement); ok {
				break
			}
			if _, ok := tok.(xml.EndElement); ok {
				return nil, fmt.Errorf("missing value for %q", key)
			}
		}
		switch s.Name.Local {
		case "string", "integer":
			var value string
			if err := d.DecodeElement(&value, &s); err != nil {
				return nil, err
			}
			values[key] = value
		default:
			if s.Name.Local == "true" || s.Name.Local == "false" {
				values[key] = s.Name.Local
			}
			if err := d.Skip(); err != nil {
				return nil, err
			}
		}
	}
}
