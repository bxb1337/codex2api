package proxy

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/codex2api/auth"
	"github.com/codex2api/internal/jsonbody"
)

// Codex 账号绑定时区：把出站请求体里由客户端生成的 <environment_context> 时区与日期
// 改写成账号级恒定值。多人共享一个账号时，各下游客户端按本机生成
// <timezone>/<current_date>，上游看到同一账号在多个时区之间跳动；绑定后每个账号只
// 呈现一个时区。这是提示词正文而不是请求头，因此与设备指纹收敛（codex_fingerprint.go）
// 相互独立：绑定即生效，不依赖收敛档位。
//
// 改写规则：
//   - 只改已存在的标签，绝不新增；请求体没有 environment_context 时原样返回。
//   - <current_date> 不是简单替换成"账号时区的今天"：历史轮次的 environment_context
//     会随全量历史一起重发（store:false），把旧日期一律改成今天会篡改历史。这里按
//     "账号时区今天 − 客户端时区今天"的天数差整体平移，旧块保持相对关系不变，同一
//     请求内的新块恰好落在账号时区的今天。客户端时区缺失或无法加载时只改时区不动日期。
//   - 幂等：同值二次改写得到相同结果，重试链路把改写过的载荷再送进来不会漂移。

const (
	environmentContextOpenTag = "<environment_context>"
	environmentDateLayout     = "2006-01-02"
)

var (
	environmentTimezoneTagPattern = regexp.MustCompile(`<timezone>([^<]*)</timezone>`)
	environmentDateTagPattern     = regexp.MustCompile(`<current_date>(\d{4}-\d{2}-\d{2})</current_date>`)

	// time.LoadLocation 每次都读 zoneinfo，热路径上缓存已解析的位置。
	environmentLocationCache sync.Map // string -> *time.Location
)

func loadLocationCached(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if cached, ok := environmentLocationCache.Load(name); ok {
		loc, _ := cached.(*time.Location)
		return loc
	}
	loc, err := time.LoadLocation(name)
	// 客户端标签可能是大消息的子串，缓存键只持有时区名称。
	name = strings.Clone(name)
	if err != nil {
		// 也缓存失败结果，避免坏值反复触发磁盘读取。
		environmentLocationCache.Store(name, (*time.Location)(nil))
		return nil
	}
	environmentLocationCache.Store(name, loc)
	return loc
}

// ApplyCodexTimezoneToBody 按账号绑定时区改写请求体 input 里所有 environment_context
// 文本。账号未绑定时区、或请求体不含 environment_context 时原样返回。
func ApplyCodexTimezoneToBody(body []byte, account *auth.Account, now time.Time) []byte {
	timezone := account.EffectiveCodexTimezone()
	if timezone == "" || len(body) == 0 {
		return body
	}
	loc := loadLocationCached(timezone)
	if loc == nil {
		return body
	}
	editor := environmentBodyEditor{body: jsonbody.New(body), timezone: timezone, today: now.In(loc)}
	input := editor.body.Get("input")
	if !input.IsArray() {
		return body
	}
	input.ForEach(editor.appendMessage)
	return editor.body.Bytes()
}

type environmentBodyEditor struct {
	body     *jsonbody.Editor
	timezone string
	today    time.Time
}

func (e environmentBodyEditor) appendMessage(index, item gjson.Result) bool {
	path := fmt.Sprintf("input.%d.content", index.Int())
	content := item.Get("content")
	if content.Type == gjson.String {
		e.appendText(path, content)
	} else if content.IsArray() {
		content.ForEach(func(partIndex, part gjson.Result) bool {
			e.appendText(fmt.Sprintf("%s.%d.text", path, partIndex.Int()), part.Get("text"))
			return true
		})
	}
	return true
}

func (e environmentBodyEditor) appendText(path string, text gjson.Result) {
	if text.Type != gjson.String {
		return
	}
	if rewritten, changed := rewriteEnvironmentContextText(text.String(), e.timezone, e.today); changed {
		e.body.Set(path, rewritten)
	}
}

func setJSONStringAtPath(body []byte, path, value string) []byte {
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

// rewriteEnvironmentContextText 改写一段文本里的时区与日期标签。文本不含
// environment_context 时不动；含有时才扫描标签（同一文本里可能嵌着多份，例如
// 记忆整理提示词里引用的历史 rollout，统一改写以保持一致）。
func rewriteEnvironmentContextText(text, timezone string, accountToday time.Time) (string, bool) {
	if !strings.Contains(text, environmentContextOpenTag) {
		return text, false
	}
	// 客户端时区取自文本里第一个 <timezone>；平移天数只在客户端时区可加载时计算。
	dayDelta, haveDelta := 0, false
	if match := environmentTimezoneTagPattern.FindStringSubmatch(text); match != nil {
		if clientLoc := loadLocationCached(match[1]); clientLoc != nil {
			clientToday := truncateToDay(accountToday.In(clientLoc))
			dayDelta = int(truncateToDay(accountToday).Sub(clientToday).Hours() / 24)
			haveDelta = true
		}
	}

	patches := environmentTagValuePatches(text, environmentTimezoneTagPattern, func(string) string {
		return timezone
	})
	if haveDelta && dayDelta != 0 {
		dateValue := func(raw string) string { return environmentDateValue(raw, dayDelta) }
		// 非时区名称的直接调用保留先写时区、再扫描日期的原有顺序。
		if strings.Contains(timezone, "<") {
			rewritten, changed := applyEnvironmentValuePatches(text, patches)
			result, dateChanged := replaceEnvironmentTagValues(rewritten, environmentDateTagPattern, dateValue)
			return result, changed || dateChanged
		}
		patches = append(patches, environmentTagValuePatches(text, environmentDateTagPattern, dateValue)...)
	}
	return applyEnvironmentValuePatches(text, patches)
}

// truncateToDay 把时刻截到所在时区的当日零点，再换成 UTC 表达以便相减得到整天差。
func truncateToDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
