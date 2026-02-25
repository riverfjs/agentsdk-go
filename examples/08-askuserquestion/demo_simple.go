//go:build demo_simple

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	toolbuiltin "github.com/riverfjs/agentsdk-go/pkg/tool/builtin"
)

func main() {
	fmt.Println("=== AskUserQuestion 工具 Demo ===")
	fmt.Println("这是一个独立的工具测试，不需要 API Key")
	fmt.Println()

	tool := toolbuiltin.NewAskUserQuestionTool()
	ctx := context.Background()

	// 场景 1: 单个问题，单选
	fmt.Println("【场景 1】单个技术选型问题")
	fmt.Println("----------------------------------------")
	params1 := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question": "我们应该使用哪种数据库？",
				"header":   "数据库",
				"options": []interface{}{
					map[string]interface{}{
						"label":       "PostgreSQL",
						"description": "功能强大的关系型数据库，支持复杂查询",
					},
					map[string]interface{}{
						"label":       "MongoDB",
						"description": "灵活的文档型数据库，适合快速迭代",
					},
					map[string]interface{}{
						"label":       "Redis",
						"description": "高性能内存数据库，适合缓存场景",
					},
				},
				"multiSelect": false,
			},
		},
	}
	runScenario(tool, ctx, params1)

	// 场景 2: 多个问题
	fmt.Println("【场景 2】配置部署环境（多个问题）")
	fmt.Println("----------------------------------------")
	params2 := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question": "选择部署环境？",
				"header":   "环境",
				"options": []interface{}{
					map[string]interface{}{
						"label":       "Staging",
						"description": "测试环境，用于预发布测试",
					},
					map[string]interface{}{
						"label":       "Production",
						"description": "生产环境，面向真实用户",
					},
				},
				"multiSelect": false,
			},
			map[string]interface{}{
				"question": "需要启用哪些功能？",
				"header":   "功能",
				"options": []interface{}{
					map[string]interface{}{
						"label":       "缓存",
						"description": "启用 Redis 缓存层",
					},
					map[string]interface{}{
						"label":       "监控",
						"description": "启用 Prometheus 监控",
					},
					map[string]interface{}{
						"label":       "日志",
						"description": "启用 ELK 日志收集",
					},
				},
				"multiSelect": true,
			},
			map[string]interface{}{
				"question": "选择部署区域？",
				"header":   "区域",
				"options": []interface{}{
					map[string]interface{}{
						"label":       "US-East",
						"description": "美国东部数据中心",
					},
					map[string]interface{}{
						"label":       "EU-West",
						"description": "欧洲西部数据中心",
					},
				},
				"multiSelect": false,
			},
		},
	}
	runScenario(tool, ctx, params2)

	// 场景 3: 多选问题
	fmt.Println("【场景 3】功能选择（多选）")
	fmt.Println("----------------------------------------")
	params3 := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question": "需要集成哪些第三方服务？",
				"header":   "集成服务",
				"options": []interface{}{
					map[string]interface{}{
						"label":       "Stripe",
						"description": "支付处理服务",
					},
					map[string]interface{}{
						"label":       "SendGrid",
						"description": "邮件发送服务",
					},
					map[string]interface{}{
						"label":       "Twilio",
						"description": "短信验证服务",
					},
					map[string]interface{}{
						"label":       "AWS S3",
						"description": "文件存储服务",
					},
				},
				"multiSelect": true,
			},
		},
	}
	runScenario(tool, ctx, params3)

	// 场景 4: 带答案的示例
	fmt.Println("【场景 4】预填答案示例")
	fmt.Println("----------------------------------------")
	params4 := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"question": "选择认证方式？",
				"header":   "认证",
				"options": []interface{}{
					map[string]interface{}{
						"label":       "OAuth 2.0",
						"description": "标准的 OAuth 2.0 授权流程",
					},
					map[string]interface{}{
						"label":       "JWT",
						"description": "无状态的 JSON Web Token",
					},
				},
				"multiSelect": false,
			},
		},
		"answers": map[string]string{
			"认证": "JWT",
		},
	}
	runScenario(tool, ctx, params4)

	fmt.Println()
	fmt.Println("=== Demo 完成 ===")
	fmt.Println()
	fmt.Println("💡 实际使用场景：")
	fmt.Println("1. 在 AI Agent 运行时，当需要用户决策时调用此工具")
	fmt.Println("2. 前端 UI 展示问题和选项供用户选择")
	fmt.Println("3. 用户选择后，通过 'answers' 参数传回 Agent")
	fmt.Println("4. Agent 根据用户选择继续执行任务")
}

func runScenario(tool *toolbuiltin.AskUserQuestionTool, ctx context.Context, params map[string]interface{}) {
	result, err := tool.Execute(ctx, params)
	if err != nil {
		log.Printf("❌ 执行失败: %v\n", err)
		fmt.Println()
		return
	}

	fmt.Println("📋 格式化输出:")
	fmt.Println(result.Output)
	fmt.Println()

	fmt.Println("📊 结构化数据:")
	if data, ok := result.Data.(map[string]interface{}); ok {
		jsonData, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(jsonData))
	}
	fmt.Println()
}
