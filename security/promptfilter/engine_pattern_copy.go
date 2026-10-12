package promptfilter

import "regexp"

// copyCustomEnginePattern 复用只读的编译程序，保留每个引擎独立的正则选项。
// Regexp.Copy 的用途是隔离 Longest 等配置，避免重复编译同一规则。
func copyCustomEnginePattern(pattern compiledPattern) compiledPattern {
	pattern.cfg = cloneEnginePatternConfig(pattern.cfg)
	pattern.requires = append([]string(nil), pattern.requires...)
	pattern.re = copyEngineRegexp(pattern.re)
	pattern.all = copyEngineRegexpList(pattern.all)
	pattern.any = copyEngineRegexpList(pattern.any)
	pattern.exclude = copyEngineRegexpList(pattern.exclude)
	return pattern
}

func copyEngineRegexp(expression *regexp.Regexp) *regexp.Regexp {
	if expression == nil {
		return nil
	}
	return expression.Copy()
}

func copyEngineRegexpList(expressions []*regexp.Regexp) []*regexp.Regexp {
	if expressions == nil {
		return nil
	}
	out := make([]*regexp.Regexp, len(expressions))
	for index, expression := range expressions {
		out[index] = copyEngineRegexp(expression)
	}
	return out
}
