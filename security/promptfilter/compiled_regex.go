package promptfilter

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var builtinRegexPrograms = struct {
	sync.Mutex
	programs map[string]*regexp.Regexp
}{}

// 仅缓存发布内置表达式；规则权重、授权策略和自定义规则仍属于各自引擎。
// 共享正则只调用匹配方法，不能调用 Longest 等修改配置的方法。
func compilePatternRegexp(expression string, builtin bool) (*regexp.Regexp, error) {
	if !builtin {
		return regexp.Compile(expression)
	}
	builtinRegexPrograms.Lock()
	defer builtinRegexPrograms.Unlock()
	if program := builtinRegexPrograms.programs[expression]; program != nil {
		return program, nil
	}
	program, err := regexp.Compile(expression)
	if err != nil {
		return nil, err
	}
	if builtinRegexPrograms.programs == nil {
		builtinRegexPrograms.programs = make(map[string]*regexp.Regexp)
	}
	builtinRegexPrograms.programs[expression] = program
	return program, nil
}

func compilePatternRegexpList(items []string, builtin bool) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("empty regex")
		}
		program, err := compilePatternRegexp(item, builtin)
		if err != nil {
			return nil, err
		}
		out = append(out, program)
	}
	return out, nil
}
