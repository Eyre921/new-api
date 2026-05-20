package service

import (
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// ResolveImageGenPrice 解析单次图片生成的固定价（美元）。
//
// 优先级（从高到低）：
//  1. ModelPrice["<model>:<quality>"] —— 如 "gpt-image-2:high"（按质量分档）
//  2. ModelPrice["<model>"]            —— 如 "gpt-image-2"（按模型一刀切）
//  3. operation_setting.GetGPTImage1PriceOnceCall(quality, size) —— 原硬编码兜底，保留向后兼容
//
// 这样运维只需要在后台「倍率设置 → 固定价格」里添加模型条目即可：
//   - 不分质量：加一行 "gpt-image-2": 0.05
//   - 想分质量：加 "gpt-image-2:high": 0.2 + "gpt-image-2:medium": 0.05 等
//   - 新模型出现（gpt-image-3 等）也无需改代码
//
// model 可能为空（来源：Responses API 响应里的 response.tools[].model）；
// 直接 Image API 调用时也可传入 info.OriginModelName。
func ResolveImageGenPrice(model, quality, size string) float64 {
	if model != "" && quality != "" {
		if p, ok := ratio_setting.GetModelPrice(model+":"+quality, false); ok {
			return p
		}
	}
	if model != "" {
		if p, ok := ratio_setting.GetModelPrice(model, false); ok {
			return p
		}
	}
	return operation_setting.GetGPTImage1PriceOnceCall(quality, size)
}
