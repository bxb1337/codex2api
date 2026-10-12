package proxy

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

type environmentValuePatch struct {
	start, end int
	value      string
}

// 仅记录变化的标签值；相同值直接复用原文，修改时按最终大小分配一次。
func replaceEnvironmentTagValues(text string, pattern *regexp.Regexp, rewrite func(string) string) (string, bool) {
	return applyEnvironmentValuePatches(text, environmentTagValuePatches(text, pattern, rewrite))
}

func environmentTagValuePatches(text string, pattern *regexp.Regexp, rewrite func(string) string) []environmentValuePatch {
	var patches []environmentValuePatch
	for offset := 0; offset < len(text); {
		match := pattern.FindStringSubmatchIndex(text[offset:])
		if match == nil {
			break
		}
		start, end := offset+match[2], offset+match[3]
		offset += match[1]
		value := rewrite(text[start:end])
		if value == text[start:end] {
			continue
		}
		patches = append(patches, environmentValuePatch{start: start, end: end, value: value})
	}
	return patches
}

func applyEnvironmentValuePatches(text string, patches []environmentValuePatch) (string, bool) {
	if len(patches) == 0 {
		return text, false
	}
	if len(patches) > 1 {
		sort.Slice(patches, func(i, j int) bool { return patches[i].start < patches[j].start })
	}
	size := len(text)
	for _, patch := range patches {
		size += len(patch.value) - (patch.end - patch.start)
	}
	var out strings.Builder
	out.Grow(size)
	position := 0
	for _, patch := range patches {
		out.WriteString(text[position:patch.start])
		out.WriteString(patch.value)
		position = patch.end
	}
	out.WriteString(text[position:])
	return out.String(), true
}

func environmentDateValue(raw string, dayDelta int) string {
	parsed, err := time.ParseInLocation(environmentDateLayout, raw, time.UTC)
	if err != nil {
		return raw
	}
	return parsed.AddDate(0, 0, dayDelta).Format(environmentDateLayout)
}
