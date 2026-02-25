//go:build demo_llm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/riverfjs/agentsdk-go/pkg/api"
	modelpkg "github.com/riverfjs/agentsdk-go/pkg/model"
)

func main() {
	// 检查 API Key
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		log.Fatal("❌ 请设置 ANTHROPIC_API_KEY 或 ANTHROPIC_AUTH_TOKEN 环境变量")
	}

	fmt.Println("=== AskUserQuestion LLM 调用测试 ===")
	fmt.Println("测试 LLM 是否会主动使用 AskUserQuestion 工具")
	fmt.Println()

	// 创建 Anthropic provider
	provider := &modelpkg.AnthropicProvider{
		ModelName: "claude-sonnet-4-5-20250929",
		APIKey:    apiKey,
	}

	ctx := context.Background()

	// 初始化 runtime（自动注册 AskUserQuestion 工具）
	runtime, err := api.New(ctx, api.Options{
		ProjectRoot:  ".",
		ModelFactory: provider,
		EntryPoint:   api.EntryPointCLI,
	})
	if err != nil {
		log.Fatalf("❌ 初始化 runtime 失败: %v", err)
	}
	defer runtime.Close()

	fmt.Println("✅ Runtime 初始化成功，AskUserQuestion 工具已注册")
	fmt.Println()

	// 构造提示词：使用"必须使用工具"的措辞
	prompt := `我想开发 LangChain DeepResearch 项目，但我不确定技术栈。

你必须使用 AskUserQuestion 工具来收集我的偏好。创建三个问题：

1. 开发语言（单选：Python/TypeScript/Go）
2. 向量数据库（单选：Pinecone/Weaviate/Qdrant）
3. 需要的功能（多选：多源搜索/智能总结/引用追踪/协作功能）

使用工具来询问我，这样我才能做出选择。`

	fmt.Println("📝 发送提示词...")
	fmt.Println("----------------------------------------")
	fmt.Println(prompt)
	fmt.Println("----------------------------------------")
	fmt.Println()
	fmt.Println("⏳ 等待 LLM 响应...")
	fmt.Println()

	// 执行请求
	resp, err := runtime.Run(ctx, api.Request{
		Prompt:    prompt,
		SessionID: "deepresearch-demo",
	})
	if err != nil {
		log.Fatalf("❌ 执行失败: %v", err)
	}

	fmt.Println("✅ LLM 响应完成！")
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("📊 LLM 输出:")
	fmt.Println("========================================")
	if resp.Result != nil {
		fmt.Println(resp.Result.Output)
		fmt.Println("========================================")
		fmt.Println()

		// 检查是否使用了工具
		if resp.Result.StopReason == "tool_use" || len(resp.Result.ToolCalls) > 0 {
			fmt.Println("🎉 成功！LLM 调用了 AskUserQuestion 工具！")
			fmt.Printf("工具调用次数: %d\n", len(resp.Result.ToolCalls))
			fmt.Println()

			for i, tc := range resp.Result.ToolCalls {
				fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
				fmt.Printf("【工具调用 #%d】\n", i+1)
				fmt.Printf("  工具名称: %s\n", tc.Name)
				fmt.Printf("  调用 ID: %s\n", tc.ID)
				fmt.Println()
				if tc.Arguments != nil {
					fmt.Println("  📋 工具参数 (JSON):")
					jsonData, _ := json.MarshalIndent(tc.Arguments, "    ", "  ")
					fmt.Printf("    %s\n", string(jsonData))
				}
				fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
				fmt.Println()
			}
		} else {
			fmt.Println("❌ LLM 没有调用工具")
			fmt.Printf("停止原因: %s\n", resp.Result.StopReason)
			fmt.Println()
			fmt.Println("💡 这可能是因为：")
			fmt.Println("  1. 提示词不够明确")
			fmt.Println("  2. LLM 认为不需要调用工具")
			fmt.Println("  3. 模型配置问题")
		}

		// 显示使用统计
		fmt.Println("----------------------------------------")
		fmt.Println("📈 Token 使用统计:")
		fmt.Printf("  输入: %d tokens\n", resp.Result.Usage.InputTokens)
		fmt.Printf("  输出: %d tokens\n", resp.Result.Usage.OutputTokens)
		fmt.Printf("  总计: %d tokens\n", resp.Result.Usage.InputTokens+resp.Result.Usage.OutputTokens)
	} else {
		fmt.Println("⚠️  没有 Result 数据")
	}

	fmt.Println()
	fmt.Println("=== 测试完成 ===")
}
