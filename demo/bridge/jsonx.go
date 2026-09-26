package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// OrderedMap marshals its keys in insertion order. The upstream payload uses it
// so choice labels reach Djev in the fixed movement order.
type OrderedMap struct {
	keys   []string
	values map[string]any
}

func NewOrderedMap() *OrderedMap {
	return &OrderedMap{values: map[string]any{}}
}

func (m *OrderedMap) Set(key string, value any) *OrderedMap {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
	return m
}

func (m *OrderedMap) Get(key string) any { return m.values[key] }

func (m *OrderedMap) Keys() []string { return append([]string(nil), m.keys...) }

func (m *OrderedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := marshalCompact(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encodedValue, err := marshalCompact(m.values[key])
		if err != nil {
			return nil, err
		}
		buf.Write(encodedValue)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalCompact encodes without HTML escaping and without a trailing newline.
func marshalCompact(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// canonicalJSON sorts every object's keys, including OrderedMap values, for trace lines and event hashes.
func canonicalJSON(value any) ([]byte, error) {
	return marshalCompact(canonicalize(value))
}

func canonicalize(value any) any {
	switch typed := value.(type) {
	case *OrderedMap:
		out := make(map[string]any, len(typed.keys))
		for _, key := range typed.keys {
			out[key] = canonicalize(typed.values[key])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = canonicalize(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = canonicalize(item)
		}
		return out
	default:
		return value
	}
}

// decodeJSON keeps numbers as json.Number so observed values keep their exact precision.
func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, &json.SyntaxError{Offset: decoder.InputOffset()}
	}
	return value, nil
}

// numberValue reports whether value is a JSON number, and its float value.
func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		f, err := typed.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}

// intValue accepts only integer JSON literals (Python rejects 1.0 and 1e2 as non-int).
func intValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			return 0, false
		}
		n, err := strconv.ParseInt(text, 10, 64)
		return n, err == nil
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	}
	return 0, false
}

// round mirrors Python's round() to the given decimals on the shortest decimal representation.
func round(value float64, digits int) float64 {
	text := strconv.FormatFloat(value, 'f', digits, 64)
	out, _ := strconv.ParseFloat(text, 64)
	return out
}
