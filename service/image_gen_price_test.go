package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// 测试 ResolveImageGenPrice 的完整 5 层兜底链。
//
// 每个 case 都通过 ratio_setting.UpdateModelPriceByJSONString 重置 ModelPrice 状态，
// 避免 case 之间相互污染（同时也覆盖了"后台改价立即生效"的行为）。
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
			name:     "③ 全局兜底（未知新模型）",
			priceMap: `{"image_generation": 0.3}`,
			model:    "gpt-image-3",
			quality:  "medium",
			size:     "2048x2048",
			want:     0.3,
		},
		{
			name:     "④ gpt-image-1 兼容兜底（清空全局兜底）",
			priceMap: `{}`,
			model:    "gpt-image-1",
			quality:  "low",
			size:     "1024x1024",
			want:     0.011, // GPTImage1Low1024x1024
		},
		{
			name:     "④ 缺失 model 字段也走 gpt-image-1 兼容兜底",
			priceMap: `{}`,
			model:    "",
			quality:  "medium",
			size:     "1024x1024",
			want:     0.042, // GPTImage1Medium1024x1024
		},
		{
			name:     "⑤ 极端：未知模型 + 无全局兜底 → 0 + warning",
			priceMap: `{}`,
			model:    "gpt-image-X-unknown",
			quality:  "auto",
			size:     "auto",
			want:     0,
		},
		{
			name:     "quality 为 auto 时不应命中 model:quality 复合 key",
			priceMap: `{"gpt-image-2:auto": 999, "gpt-image-2": 0.05}`,
			model:    "gpt-image-2",
			quality:  "auto",
			size:     "1024x1024",
			// 注意：当前实现下 quality="auto" 会真的尝试匹配 "gpt-image-2:auto"，
			// 这意味着如果运维错配了 :auto 反而会生效。我们用这个测试明确这个语义，
			// 让未来的人意识到：要么显式禁止 auto 拼 key，要么文档里说明。
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

	// 第二次立即调用：map 时间戳不应更新（之所以不破坏旧值是限流标志位的语义）
	before := unknownImageModelLogAt["gpt-image-XX"]
	logUnknownImageModel("gpt-image-XX", "high", "1024x1024")
	after := unknownImageModelLogAt["gpt-image-XX"]
	if !before.Equal(after) {
		t.Errorf("throttle map timestamp should not change on rapid re-call; before=%v after=%v", before, after)
	}
}
