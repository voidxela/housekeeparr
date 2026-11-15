package rtorrent

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// A minimal XML-RPC codec: string parameters out; nested values in.

func encodeCall(method string, params []string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
	_ = xml.EscapeText(&b, []byte(method))
	b.WriteString(`</methodName><params>`)
	for _, p := range params {
		b.WriteString(`<param><value><string>`)
		_ = xml.EscapeText(&b, []byte(p))
		b.WriteString(`</string></value></param>`)
	}
	b.WriteString(`</params></methodCall>`)
	return b.Bytes()
}

// Fault is an XML-RPC fault response.
type Fault struct {
	Code    int64
	Message string
}

func (f *Fault) Error() string { return fmt.Sprintf("fault %d: %s", f.Code, f.Message) }

const maxDepth = 16

// decodeResponse returns the single response value or a *Fault.
func decodeResponse(body []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("no methodResponse: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "fault":
			v, err := nextValue(d, 0)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			f := &Fault{}
			if c, ok := m["faultCode"].(int64); ok {
				f.Code = c
			}
			f.Message, _ = m["faultString"].(string)
			return nil, f
		case "param":
			return nextValue(d, 0)
		}
	}
}

// nextValue advances to the next <value> element and decodes it.
func nextValue(d *xml.Decoder, depth int) (any, error) {
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "value" {
			return decodeValue(d, depth)
		}
	}
}

// decodeValue decodes the contents of a <value> element (already opened).
func decodeValue(d *xml.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("value nesting too deep")
	}
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			// <value>text</value> without a type is a string.
			return text.String(), nil
		case xml.StartElement:
			v, err := decodeTyped(d, t, depth)
			if err != nil {
				return nil, err
			}
			// consume until </value>
			for {
				tok, err := d.Token()
				if err != nil {
					return nil, err
				}
				if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "value" {
					return v, nil
				}
			}
		}
	}
}

func elementText(d *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("unexpected element <%s> in scalar", t.Name.Local)
		}
	}
}

func decodeTyped(d *xml.Decoder, se xml.StartElement, depth int) (any, error) {
	switch se.Name.Local {
	case "i4", "i8", "int":
		s, err := elementText(d)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	case "boolean":
		s, err := elementText(d)
		if err != nil {
			return nil, err
		}
		return strings.TrimSpace(s) == "1", nil
	case "double":
		s, err := elementText(d)
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	case "string":
		return elementText(d)
	case "base64":
		s, err := elementText(d)
		if err != nil {
			return nil, err
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		return string(b), err
	case "array":
		var out []any
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "value" {
					v, err := decodeValue(d, depth+1)
					if err != nil {
						return nil, err
					}
					out = append(out, v)
				}
			case xml.EndElement:
				if t.Name.Local == "array" {
					return out, nil
				}
			}
		}
	case "struct":
		out := map[string]any{}
		var name string
		for {
			tok, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "name":
					if name, err = elementText(d); err != nil {
						return nil, err
					}
				case "value":
					v, err := decodeValue(d, depth+1)
					if err != nil {
						return nil, err
					}
					out[name] = v
				}
			case xml.EndElement:
				if t.Name.Local == "struct" {
					return out, nil
				}
			}
		}
	default:
		if err := d.Skip(); err != nil && err != io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("unsupported XML-RPC type <%s>", se.Name.Local)
	}
}
