package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// TestComputeImageGenQuota_PerCallBranch 验证按次计费的两层 ModelPrice 命中。
func TestComputeImageGenQuota_PerCallBranch(t *testing.T) {
	cases := []struct {
		name              string
		modelPrice        string
		modelRatio        string
		completionRatio   string
		model             string
		quality           string
		size              string
		input             int
		output            int
		groupRatio        float64
		wantMode          ImageGenBillingMode
		wantPerCallPrice  float64
	}{
		{
			name:             "① model:quality 精确按次",
			modelPrice:       `{"gpt-image-2:high": 0.2, "gpt-image-2": 0.05}`,
			model:            "gpt-image-2",
			quality:          "high",
			size:             "1024x1024",
			groupRatio:       1.0,
			wantMode:         ImageGenBillingPerCall,
			wantPerCallPrice: 0.2,
		},
		{
			name:             "② model 一刀切按次",
			modelPrice:       `{"gpt-image-2": 0.05}`,
			model:            "gpt-image-2",
			quality:          "high",
			size:             "1024x1024",
			groupRatio:       1.0,
			wantMode:         ImageGenBillingPerCall,
			wantPerCallPrice: 0.05,
		},
		{
			name: "ModelPrice 优先：同时配 ModelPrice 和 ModelRatio，仍走按次",
			// 这是与全局 ModelPriceHelper 一致的语义。
			modelPrice:       `{"gpt-image-2": 1.5}`,
			modelRatio:       `{"gpt-image-2": 999}`,
			completionRatio:  `{"gpt-image-2": 999}`,
			model:            "gpt-image-2",
			input:            46,
			output:           196,
			groupRatio:       1.0,
			wantMode:         ImageGenBillingPerCall,
			wantPerCallPrice: 1.5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ratio_setting.UpdateModelPriceByJSONString(tc.modelPrice); err != nil {
				t.Fatalf("seed ModelPrice: %v", err)
			}
			if tc.modelRatio != "" {
				if err := ratio_setting.UpdateModelRatioByJSONString(tc.modelRatio); err != nil {
					t.Fatalf("seed ModelRatio: %v", err)
				}
			}
			if tc.completionRatio != "" {
				if err := ratio_setting.UpdateCompletionRatioByJSONString(tc.completionRatio); err != nil {
					t.Fatalf("seed CompletionRatio: %v", err)
				}
			}

			r := ComputeImageGenQuota(tc.model, tc.quality, tc.size, tc.input, tc.output, tc.groupRatio)
			if r.Mode != tc.wantMode {
				t.Errorf("Mode = %s, want %s", r.Mode, tc.wantMode)
			}
			if r.PerCallPrice != tc.wantPerCallPrice {
				t.Errorf("PerCallPrice = %v, want %v", r.PerCallPrice, tc.wantPerCallPrice)
			}
			// 按次 quota 公式校验
			wantQuota := int(tc.wantPerCallPrice * common.QuotaPerUnit * tc.groupRatio)
			if r.Quota != wantQuota {
				t.Errorf("Quota = %d, want %d", r.Quota, wantQuota)
			}
		})
	}
}

// TestComputeImageGenQuota_ByTokenBranch 验证按 token 模式。
// 公式与 new-api 文本模型计费完全一致：
//
//	quota = (input + output × completionRatio) × modelRatio × groupRatio
func TestComputeImageGenQuota_ByTokenBranch(t *testing.T) {
	// 只配 ModelRatio（不配 ModelPrice），按 token 路径生效
	if err := ratio_setting.UpdateModelPriceByJSONString(`{}`); err != nil {
		t.Fatalf("clear ModelPrice: %v", err)
	}
	if err := ratio_setting.UpdateModelRatioByJSONString(`{"gpt-image-2": 0.06}`); err != nil {
		t.Fatalf("seed ModelRatio: %v", err)
	}
	if err := ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-image-2": 1}`); err != nil {
		t.Fatalf("seed CompletionRatio: %v", err)
	}

	r := ComputeImageGenQuota("gpt-image-2", "low", "1024x1024", 46, 196, 0.6)
	if r.Mode != ImageGenBillingByToken {
		t.Fatalf("Mode = %s, want by_token", r.Mode)
	}
	if r.InputTokens != 46 || r.OutputTokens != 196 {
		t.Errorf("Tokens: input=%d output=%d", r.InputTokens, r.OutputTokens)
	}
	if r.ModelRatio != 0.06 || r.CompletionRatio != 1 {
		t.Errorf("Ratios: model=%v completion=%v", r.ModelRatio, r.CompletionRatio)
	}
	// 公式：(46 + 196×1) × 0.06 × 0.6 = 242 × 0.036 = 8.712 → round = 9
	if r.Quota < 8 || r.Quota > 10 {
		t.Errorf("Quota = %d, want ~9 (公式 (46+196×1)×0.06×0.6 = 8.712)", r.Quota)
	}
}

// TestComputeImageGenQuota_ByTokenRequiresTokens 验证：即使 ModelRatio 配了，
// 但响应里没带 token 数（input=output=0），不会误走 by_token 分支，而是降级到兜底。
func TestComputeImageGenQuota_ByTokenRequiresTokens(t *testing.T) {
	if err := ratio_setting.UpdateModelPriceByJSONString(`{}`); err != nil {
		t.Fatalf("clear ModelPrice: %v", err)
	}
	if err := ratio_setting.UpdateModelRatioByJSONString(`{"gpt-image-X": 0.06}`); err != nil {
		t.Fatalf("seed ModelRatio: %v", err)
	}

	r := ComputeImageGenQuota("gpt-image-X", "high", "1024x1024", 0, 0, 1.0)
	// ModelRatio 配了但 tokens 都是 0 → 跳过 by_token，走 image_generation 兜底
	if r.Mode != ImageGenBillingFallbackPerCall {
		t.Errorf("Mode = %s, want fallback_per_call (ensureImageGenFallbackInMap 已自动补回 image_generation)", r.Mode)
	}
}

// TestComputeImageGenQuota_FallbackChain 验证后两层兜底。
func TestComputeImageGenQuota_FallbackChain(t *testing.T) {
	// 升级安全：空 JSON 也会自动补回 image_generation
	if err := ratio_setting.UpdateModelPriceByJSONString(`{}`); err != nil {
		t.Fatalf("clear ModelPrice: %v", err)
	}
	if err := ratio_setting.UpdateModelRatioByJSONString(`{}`); err != nil {
		t.Fatalf("clear ModelRatio: %v", err)
	}

	r := ComputeImageGenQuota("gpt-image-99", "auto", "auto", 100, 100, 1.0)
	if r.Mode != ImageGenBillingFallbackPerCall {
		t.Errorf("Mode = %s, want fallback_per_call (image_generation auto-injected)", r.Mode)
	}
	if r.PerCallPrice != ratio_setting.DefaultImageGenFallbackPrice {
		t.Errorf("PerCallPrice = %v, want %v",
			r.PerCallPrice, ratio_setting.DefaultImageGenFallbackPrice)
	}
}

// TestComputeImageGenQuota_GPTImage1Compat 验证旧硬编码兼容。
func TestComputeImageGenQuota_GPTImage1Compat(t *testing.T) {
	if err := ratio_setting.UpdateModelPriceByJSONString(`{}`); err != nil {
		t.Fatalf("clear ModelPrice: %v", err)
	}
	if err := ratio_setting.UpdateModelRatioByJSONString(`{}`); err != nil {
		t.Fatalf("clear ModelRatio: %v", err)
	}
	// 由于 ensureImageGenFallbackInMap，image_generation 在上面 clear 后会自动补回。
	// 想看 gpt-image-1 兼容路径需要在它之前命中（model="gpt-image-1" 时 ④ 命中
	// image_generation 而非 gpt-image-1 硬编码）。这是预期行为：image_generation
	// 兜底优先于 gpt-image-1 硬编码，因为后者价格表已过时。
	r := ComputeImageGenQuota("gpt-image-1", "high", "1024x1024", 0, 0, 1.0)
	// 应该命中 image_generation 全局兜底（0.3），而非老硬编码 (0.167)
	if r.Mode != ImageGenBillingFallbackPerCall {
		t.Errorf("Mode = %s, want fallback_per_call", r.Mode)
	}
	if r.PerCallPrice != ratio_setting.DefaultImageGenFallbackPrice {
		t.Errorf("PerCallPrice = %v, want %v (image_generation 优先于 gpt-image-1 硬编码)",
			r.PerCallPrice, ratio_setting.DefaultImageGenFallbackPrice)
	}
}

// TestLogUnknownImageModelThrottle 测试 warning 日志限流。
func TestLogUnknownImageModelThrottle(t *testing.T) {
	unknownImageModelLogMu.Lock()
	unknownImageModelLogAt = map[string]time.Time{}
	unknownImageModelLogMu.Unlock()

	logUnknownImageModel("gpt-image-XX", "high", "1024x1024")
	unknownImageModelLogMu.Lock()
	_, ok := unknownImageModelLogAt["gpt-image-XX"]
	unknownImageModelLogMu.Unlock()
	if !ok {
		t.Fatalf("expected throttle map entry for first call")
	}
}
