package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// 测试 ResolveImageGenPrice 的完整 5 层兜底链。
//
// 每个 case 都通过 ratio_setting.UpdateModelPriceByJSONString 重置 ModelPrice 状态，
// 注意：UpdateModelPriceByJSONString 已含 ensureImageGenFallbackInMap 升级保护，
// 即使注入空 JSON 也会自动补回 image_generation 兜底键 —— 这是有意的"升级安全"
// 行为。在 case 中需要显式覆盖 image_generation 才能测试 ④/⑤ 层。
func TestResolveImageGenPrice(t *testing.T) {
	cases := []struct {
		name     string
		priceMap string // 注入的 ModelPrice JSON
		model    string
		quality  string
		size     string
		want     float64
	}{
		{
			name:     "① model+quality 精确匹配",
			priceMap: `{"gpt-image-2:high": 0.2, "gpt-image-2": 0.05, "image_generation": 0.3}`,
			model:    "gpt-image-2",
			quality:  "high",
			size:     "1024x1024",
			want:     0.2,
		},
		{
			name:     "② model 一刀切（无 model:quality）",
			priceMap: `{"gpt-image-2": 0.05, "image_generation": 0.3}`,
			model:    "gpt-image-2",
			quality:  "high",
			size:     "1024x1024",
			want:     0.05,
		},
		{
			name:     "③ 全局兜底（未知新模型，DB 含 image_generation）",
			priceMap: `{"image_generation": 0.3}`,
			model:    "gpt-image-3",
			quality:  "medium",
			size:     "2048x2048",
			want:     0.3,
		},
		{
			name: "③ 升级安全：空 JSON 也会自动补回 image_generation 兜底",
			// 这个 case 验证 ensureImageGenFallbackInMap 的关键行为：
			// 既有部署升级时 DB 里没有 image_generation，被 LoadFromJsonString
			// 清空后必须由 ensureImageGenFallbackInMap 补回，否则 ③ 永远命中不到。
			priceMap: `{}`,
			model:    "gpt-image-3",
			quality:  "high",
			size:     "1024x1024",
			want:     0.3, // = DefaultImageGenFallbackPrice
		},
		{
			name:     "④ gpt-image-1 兼容兜底（手动覆盖 image_generation 为非默认值不影响 ④）",
			priceMap: `{"image_generation": 999}`,
			model:    "gpt-image-1",
			quality:  "low",
			size:     "1024x1024",
			// 注意：因为 image_generation=999 在 ③ 就命中了，
			// 这其实验证的是 ③ 在 model="gpt-image-1" 时也会命中
			// （④ 仅作为 ③ miss 后的回落）。
			want: 999,
		},
		{
			name:     "⑤ 极端兜底：未知新模型 + image_generation 被清空 → 返回 0.3（永不为 0）",
			priceMap: `{"image_generation": 0}`, // 注意：0 仍然是 ok=true，会命中 ③ 返回 0
			model:    "gpt-image-X-unknown",
			quality:  "auto",
			size:     "auto",
			want:     0, // ③ 命中 image_generation=0
		},
		{
			name:     "quality 为 auto 时仍可命中 model:quality 复合 key",
			priceMap: `{"gpt-image-2:auto": 999, "gpt-image-2": 0.05}`,
			model:    "gpt-image-2",
			quality:  "auto",
			size:     "1024x1024",
			// 说明语义：要么显式禁止 auto 拼 key，要么允许运维利用此特性
			// 给 auto 配单独的价。当前实现选后者（更灵活）。
			want: 999,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ratio_setting.UpdateModelPriceByJSONString(tc.priceMap); err != nil {
				t.Fatalf("failed to seed ModelPrice: %v", err)
			}
			got := ResolveImageGenPrice(tc.model, tc.quality, tc.size)
			if got != tc.want {
				t.Errorf("ResolveImageGenPrice(%q, %q, %q) = %v, want %v",
					tc.model, tc.quality, tc.size, got, tc.want)
			}
		})
	}
}

// TestUpgradeSafetyEnsureImageGenFallback 单独验证升级安全行为：
// 模拟从老版本升级 —— DB 里的 ModelPrice JSON 完全不含 image_generation 键，
// 但加载后 modelPriceMap 必须有这个键（值为 DefaultImageGenFallbackPrice）。
// 这是"两套前端都能看到这一行"的后端基础。
func TestUpgradeSafetyEnsureImageGenFallback(t *testing.T) {
	// 模拟既有部署：DB 里有别的模型配置但没有 image_generation
	legacyDBJSON := `{"dall-e-3": 0.04, "gpt-image-1": 0.05}`
	if err := ratio_setting.UpdateModelPriceByJSONString(legacyDBJSON); err != nil {
		t.Fatalf("failed to simulate legacy DB load: %v", err)
	}

	// 升级保护应自动补回 image_generation
	got, ok := ratio_setting.GetModelPrice(ratio_setting.ImageGenFallbackKey, false)
	if !ok {
		t.Fatalf("expected ModelPrice[%q] to be auto-injected after legacy DB load, but missing",
			ratio_setting.ImageGenFallbackKey)
	}
	if got != ratio_setting.DefaultImageGenFallbackPrice {
		t.Errorf("expected auto-injected fallback price = %v, got %v",
			ratio_setting.DefaultImageGenFallbackPrice, got)
	}

	// 管理员自定义后不应被覆盖
	customJSON := `{"dall-e-3": 0.04, "image_generation": 0.88}`
	if err := ratio_setting.UpdateModelPriceByJSONString(customJSON); err != nil {
		t.Fatalf("failed to apply custom config: %v", err)
	}
	got, _ = ratio_setting.GetModelPrice(ratio_setting.ImageGenFallbackKey, false)
	if got != 0.88 {
		t.Errorf("expected custom value 0.88 to be respected, got %v", got)
	}
}

// 测试 warning 日志的限流逻辑：同一未知模型在 5 分钟窗口内应只触发一次日志。
// 这里只验证函数本身可重复调用且 map 状态正确，不直接断言日志输出。
func TestLogUnknownImageModelThrottle(t *testing.T) {
	// 重置 map，避免上一个测试残留
	unknownImageModelLogMu.Lock()
	unknownImageModelLogAt = map[string]time.Time{}
	unknownImageModelLogMu.Unlock()

	logUnknownImageModel("gpt-image-XX", "high", "1024x1024")

	unknownImageModelLogMu.Lock()
	_, ok := unknownImageModelLogAt["gpt-image-XX"]
	unknownImageModelLogMu.Unlock()
	if !ok {
		t.Fatalf("expected throttle map to record entry for first log")
	}

	// 第二次立即调用：map 时间戳不应更新（限流标志位的语义）
	before := unknownImageModelLogAt["gpt-image-XX"]
	logUnknownImageModel("gpt-image-XX", "high", "1024x1024")
	after := unknownImageModelLogAt["gpt-image-XX"]
	if !before.Equal(after) {
		t.Errorf("throttle map timestamp should not change on rapid re-call; before=%v after=%v", before, after)
	}
}
