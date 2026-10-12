package promptfilter

import (
	"crypto/sha256"
	"fmt"
	"log"
	"strings"

	json "github.com/goccy/go-json"
)

const (
	engineCacheCapacity     = 64
	engineRuleCacheCapacity = 8
)

var (
	engineCache     = newImmutableCache[*Engine](engineCacheCapacity)
	engineRuleCache = newImmutableCache[*engineRuleResources](engineRuleCacheCapacity)
)

// engineRuleResources 的身份只由规则和敏感词决定，执行参数保留在 Engine 中。
type engineRuleResources struct {
	patterns               []compiledPattern
	sensitiveWords         []string
	literalIndex           *literalIndex
	viewMatchHintAutomaton *decodedSafetyHintAutomaton
	priorityScanner        decodedSafetyPriorityScanner
	exactScanner           decodedSafetyPriorityScanner
}

func engineForConfig(cfg Config) (*Engine, error) {
	return engineCache.getOrBuild(engineCacheKey(cfg), func() (*Engine, error) {
		return newEngineWithRuleCache(cfg, true)
	})
}

func engineRuleCacheKey(cfg Config) string {
	key := struct {
		SensitiveWords           string
		CustomPatterns           []PatternConfig
		DisabledPatterns         []string
		AuthorizedPentestAllowed bool
	}{
		SensitiveWords: cfg.SensitiveWords, CustomPatterns: cfg.CustomPatterns,
		DisabledPatterns:         cfg.DisabledPatterns,
		AuthorizedPentestAllowed: cfg.Advanced.Enforcement.AuthorizedPentestAllowed,
	}
	data, _ := json.Marshal(key)
	return string(data)
}

func NewEngine(cfg Config) (*Engine, error) {
	return newEngineWithRuleCache(cfg, false)
}

func newEngineWithRuleCache(cfg Config, retainRules bool) (*Engine, error) {
	cfg = NormalizeConfig(cfg)
	// 检测不使用远端审核凭证，避免旧配置长期保留密钥。
	cfg.Review.APIKey = ""
	resources, shared, err := loadEngineRuleResources(cfg, retainRules)
	if err != nil {
		return nil, err
	}
	rules, err := engineRulesForCaller(resources, shared)
	if err != nil {
		return nil, err
	}
	exactScanner := rules.exactScanner
	exactScanner.decisionThreshold = cfg.Threshold
	return &Engine{
		cfg: cfg, fingerprint: sha256.Sum256([]byte(engineCacheKey(cfg))),
		patterns: rules.patterns, sensitiveWords: rules.sensitiveWords,
		literalIndex: rules.literalIndex, viewMatchHintAutomaton: rules.viewMatchHintAutomaton,
		decodedPriorityScanner: rules.priorityScanner, exactPrecheckScanner: exactScanner,
	}, nil
}

func loadEngineRuleResources(cfg Config, retain bool) (*engineRuleResources, bool, error) {
	key := engineRuleCacheKey(cfg)
	if retain {
		resources, err := engineRuleCache.getOrBuild(key, func() (*engineRuleResources, error) {
			return buildEngineRuleResources(cfg)
		})
		return resources, true, err
	}
	if resources, ok := engineRuleCache.load(key); ok {
		return resources, true, nil
	}
	// 独立的一次性构建不占用全局规则缓存，也不复制已经独占的规则状态。
	resources, err := buildEngineRuleResources(cfg)
	return resources, false, err
}

func engineRulesForCaller(resources *engineRuleResources, shared bool) (engineRuleResources, error) {
	rules := *resources
	if !shared {
		return rules, nil
	}
	patterns, err := cloneEnginePatterns(resources.patterns)
	if err != nil {
		return engineRuleResources{}, err
	}
	rules.patterns = patterns
	rules.priorityScanner = cloneEngineScanner(resources.priorityScanner, patterns, false)
	rules.exactScanner = cloneEngineScanner(resources.exactScanner, patterns, true)
	return rules, nil
}

func buildEngineRuleResources(cfg Config) (*engineRuleResources, error) {
	patterns, err := compileEnginePatterns(cfg)
	if err != nil {
		return nil, err
	}
	words := parseSensitiveWords(cfg.SensitiveWords)
	priority := buildDecodedSafetyPriorityScanner(patterns)
	priority.sensitiveWords = words
	priority.hintIndex = buildDecodedSafetyHintIndex(priority)
	exact := buildDecodedSafetyExactPrecheckScanner(patterns, DefaultThreshold)
	exact.sensitiveWords = words
	exact.hintIndex = buildDecodedSafetyHintIndex(exact)
	return &engineRuleResources{
		patterns: patterns, sensitiveWords: words, literalIndex: buildLiteralIndex(patterns, words),
		viewMatchHintAutomaton: buildGuaranteedPatternHintAutomaton(patterns),
		priorityScanner:        priority, exactScanner: exact,
	}, nil
}

func compileEnginePatterns(cfg Config) ([]compiledPattern, error) {
	disabled := disabledPatternSet(cfg.DisabledPatterns)
	merged := append(append([]PatternConfig(nil), defaultPatternConfigs...), cfg.CustomPatterns...)
	patterns := make([]compiledPattern, 0, len(merged))
	for index, pattern := range merged {
		builtin := index < len(defaultPatternConfigs)
		pattern = resolveAuthorizationExcludes(pattern, cfg.Advanced.Enforcement.AuthorizedPentestAllowed)
		pattern.Name = strings.TrimSpace(pattern.Name)
		pattern.Pattern = strings.TrimSpace(pattern.Pattern)
		pattern.Category = strings.TrimSpace(pattern.Category)
		if disabledEnginePattern(pattern, disabled) {
			continue
		}
		if !builtin && quarantineCustomPattern(pattern) {
			continue
		}
		if invalidEnginePattern(pattern) {
			continue
		}
		compiled, err := compileEnginePattern(pattern, builtin)
		if err != nil {
			if builtin {
				return nil, err
			}
			log.Printf("prompt filter: custom rule skipped name=%q code=%s message=%s", pattern.Name, PatternQuarantineInvalidRegex, err)
			continue
		}
		patterns = append(patterns, compiled)
	}
	return patterns, nil
}

func quarantineCustomPattern(pattern PatternConfig) bool {
	issue := AuditPatternConfig(pattern)
	if issue == nil {
		return false
	}
	log.Printf("prompt filter: custom rule skipped name=%q code=%s message=%s", pattern.Name, issue.Code, issue.Message)
	return true
}

func disabledEnginePattern(pattern PatternConfig, disabled map[string]bool) bool {
	return disabled[strings.ToLower(pattern.Name)] || (pattern.Enabled != nil && !*pattern.Enabled)
}

func invalidEnginePattern(pattern PatternConfig) bool {
	return pattern.Name == "" || pattern.Weight <= 0 || (pattern.Pattern == "" && len(pattern.AllPatterns) == 0 && len(pattern.AnyPatterns) == 0)
}

func compileEnginePattern(pattern PatternConfig, builtin bool) (compiledPattern, error) {
	compiled := compiledPattern{builtin: builtin, cfg: cloneEnginePatternConfig(pattern), requires: patternRequires(pattern.Pattern)}
	var err error
	if pattern.Pattern != "" {
		compiled.re, err = compilePatternRegexp(pattern.Pattern, builtin)
		if err != nil {
			return compiledPattern{}, fmt.Errorf("compile pattern %q: %w", pattern.Name, err)
		}
	}
	compiled.all, err = compilePatternRegexpList(pattern.AllPatterns, builtin)
	if err != nil {
		return compiledPattern{}, fmt.Errorf("compile all pattern %q: %w", pattern.Name, err)
	}
	compiled.any, err = compilePatternRegexpList(pattern.AnyPatterns, builtin)
	if err != nil {
		return compiledPattern{}, fmt.Errorf("compile any pattern %q: %w", pattern.Name, err)
	}
	compiled.exclude, err = compilePatternRegexpList(pattern.ExcludePatterns, builtin)
	if err != nil {
		return compiledPattern{}, fmt.Errorf("compile exclude pattern %q: %w", pattern.Name, err)
	}
	compiled.guaranteedHintClauses = decodedSafetyGuaranteedHintClauses(compiled)
	return compiled, nil
}

func cloneEnginePatterns(source []compiledPattern) ([]compiledPattern, error) {
	patterns := append([]compiledPattern(nil), source...)
	for index, pattern := range patterns {
		if !pattern.builtin {
			patterns[index] = copyCustomEnginePattern(pattern)
			continue
		}
		patterns[index].cfg = cloneEnginePatternConfig(pattern.cfg)
		patterns[index].all = append(pattern.all[:0:0], pattern.all...)
		patterns[index].any = append(pattern.any[:0:0], pattern.any...)
		patterns[index].exclude = append(pattern.exclude[:0:0], pattern.exclude...)
	}
	return patterns, nil
}

func cloneEngineScanner(source decodedSafetyPriorityScanner, patterns []compiledPattern, includeAll bool) decodedSafetyPriorityScanner {
	source.patterns = append([]decodedSafetyPriorityPattern(nil), source.patterns...)
	index := 0
	for _, pattern := range patterns {
		if !includeAll && (pattern.cfg.SignalOnly || (!pattern.cfg.Strict && pattern.cfg.Weight < DefaultThreshold)) {
			continue
		}
		source.patterns[index].pattern = pattern
		index++
	}
	return source
}

func cloneEnginePatternConfig(pattern PatternConfig) PatternConfig {
	pattern.AllPatterns = append([]string(nil), pattern.AllPatterns...)
	pattern.AnyPatterns = append([]string(nil), pattern.AnyPatterns...)
	pattern.ExcludePatterns = append([]string(nil), pattern.ExcludePatterns...)
	pattern.AuthorizationExcludePatterns = append([]string(nil), pattern.AuthorizationExcludePatterns...)
	if pattern.Enabled != nil {
		enabled := *pattern.Enabled
		pattern.Enabled = &enabled
	}
	return pattern
}
