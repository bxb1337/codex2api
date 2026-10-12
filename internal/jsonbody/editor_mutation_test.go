package jsonbody

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func equivalentJSON(expected, actual []byte) bool {
	var want, got bytes.Buffer
	if json.Compact(&want, expected) != nil || json.Compact(&got, actual) != nil {
		return false
	}
	return bytes.Equal(want.Bytes(), got.Bytes())
}

func TestEditorPreservesSharedInput(t *testing.T) {
	const largeTextBytes = 4 * 1024 * 1024
	cases := []struct {
		name string
		body string
		edit func(*Editor)
	}{
		{"large", `{"input":"` + strings.Repeat("x", largeTextBytes) + `","a":1,"b":2}`, func(e *Editor) { e.Set("a", "replacement"); e.Delete("b"); e.Set("c", true) }},
		{"escaped", ` {"a":"a\\b\"c","b":{"text":"old"},"n":9007199254740993} `, func(e *Editor) { e.Set("b.text", "a\n\"long replacement\\"); e.Delete("a") }},
		{"no_op", `{"a":"same"}`, func(e *Editor) { e.Set("a", "same"); e.Delete("missing") }},
		{"overlap", `{"a":{"b":1},"other":2}`, func(e *Editor) { e.Set("a", map[string]int{"b": 3}); e.Set("a.b", 4) }},
		{"duplicate", `{"a":1}`, func(e *Editor) { e.Set("a", 2); e.Delete("a"); e.Set("a", 3) }},
		{"array", `{"input":[{"content":"old"},{"content":"second"}]}`, func(e *Editor) { e.Set("input.0.content", "longer"); e.Set("input.1.content", "short") }},
		{"missing_parent", `{"a":1}`, func(e *Editor) { e.Set("reasoning.context", "all_turns"); e.Set("stream", true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			snapshot := bytes.Clone(body)
			for range 2 {
				e := New(body)
				tc.edit(e)
				out := e.Bytes()
				if expected := e.sequential(); !bytes.Equal(expected, out) {
					// 删除字段的空白可保留，但 JSON 值及原始字符串必须一致。
					if !equivalentJSON(expected, out) {
						t.Fatal("edited body differs from sequential edits")
					}
				}
				if !bytes.Equal(snapshot, body) {
					t.Fatal("shared input was modified")
				}
			}
		})
	}
}

func TestEditorConcurrentSharedInput(t *testing.T) {
	const workers = 8
	body := []byte(`{"input":[{"content":"shared"}],"metadata":{"session":"original"}}`)
	snapshot := bytes.Clone(body)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := New(body)
			e.Set("metadata.session", "new longer value")
			e.Delete("input")
			_ = e.Bytes()
		}()
	}
	wg.Wait()
	if !bytes.Equal(snapshot, body) {
		t.Fatal("concurrent editing modified shared input")
	}
}
