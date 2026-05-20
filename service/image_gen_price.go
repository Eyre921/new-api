package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// ResolveImageGenPrice 解析单次图片生成的固定价（美元）。
//
// 完整兜底链（从高到低，先命中先返回）：
//
//  1. ModelPrice["<model>:<quality>"]                精细：按模型 + 质量分档
//     例：ModelPrice["gpt-image-2:high"] = 0.2
//
//  2. ModelPrice["<model>"]                          中等：按模型一刀切
//     例：ModelPrice["gpt-image-2"] = 0.05
//
//  3. ModelPrice["image_generation"]                 全局兜底（默认 0.3）
//     未来出 gpt-image-3 等新模型且管理员未配置时按此价收，避免亏损。
//     由 ratio_setting.ensureImageGenFallbackInMap 保证始终存在，
//     既有部署升级也会自动补回，后台「固定价格」UI 可见且可编辑。
//
//  4. operation_setting.GetGPTImage1PriceOnceCall(quality, size)
//     仅当 model 为空 / model 为 "gpt-image-1" 时使用，保留旧逻辑兼容。
//     未知新模型不再走这一步，避免按老 gpt-image-1 价亏损。
//
//  5. ratio_setting.DefaultImageGenFallbackPrice + 限流 warning 日志
//     极端兜底：运维主动清空 ModelPrice["image_generation"] 且 model 信息
//     齐全但不匹配前 3 层时使用。**永远不会返回 0**，确保任何情况下都
//     有合理计费；warning 日志让运维感知配置缺失。
//
// 调用方：
//   - service/text_quota.go: Responses API 触发 image_generation tool call 时
//   - service/tool_billing.go: 预留的统一 tool 计费入口（当前未启用）
func ResolveImageGenPrice(model, quality, size string) float64 {
	// ① model + quality 复合 key
	if model != "" && quality != "" {
		if p, ok := ratio_setting.GetModelPrice(model+":"+quality, false); ok {
			return p
		}
	}

	// ② model 一刀切
	if model != "" {
		if p, ok := ratio_setting.GetModelPrice(model, false); ok {
			return p
		}
	}

	// ③ 全局兜底（由 ensureImageGenFallbackInMap 保证升级后也存在）
	if p, ok := ratio_setting.GetModelPrice(ratio_setting.ImageGenFallbackKey, false); ok {
		return p
	}

	// ④ 仅对 gpt-image-1（或缺失 model 信息）回落到老硬编码
	if model == "" || model == "gpt-image-1" {
		return operation_setting.GetGPTImage1PriceOnceCall(quality, size)
	}

	// ⑤ 极端兜底：运维主动删除了 image_generation 这一行配置时，
	// 用编译期常量做最终保护，永远不会返回 0；同时打 warning 让运维感知。
	logUnknownImageModel(model, quality, size)
	return ratio_setting.DefaultImageGenFallbackPrice
}

// unknownImageModelLogMu 与 unknownImageModelLogAt 用于限流 warning 日志：
// 即使在高并发场景下，对同一未知模型也最多每 5 分钟打印一次，避免日志洪水。
var (
	unknownImageModelLogMu sync.Mutex
	unknownImageModelLogAt = map[string]time.Time{}
)

const unknownImageModelLogInterval = 5 * time.Minute

func logUnknownImageModel(model, quality, size string) {
	unknownImageModelLogMu.Lock()
	now := time.Now()
	last, seen := unknownImageModelLogAt[model]
	if seen && now.Sub(last) < unknownImageModelLogInterval {
		unknownImageModelLogMu.Unlock()
		return
	}
	unknownImageModelLogAt[model] = now
	unknownImageModelLogMu.Unlock()

	common.SysError(fmt.Sprintf(
		"[ImageGenPrice] 未知图片模型 %q (quality=%q size=%q)，"+
			"且 ModelPrice[%q] 被清空；本次按编译期兜底价 %.4f USD 计费。"+
			"请到后台「倍率设置 → 固定价格」补回 %q 或为 %q 添加专属价格。",
		model, quality, size,
		ratio_setting.ImageGenFallbackKey, ratio_setting.DefaultImageGenFallbackPrice,
		ratio_setting.ImageGenFallbackKey, model,
	))
}
