// Package view defines administrator-authored, self-contained HTML resources.
package view

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
)

const MIMEType = "text/html"
const MaxHTMLBytes = 256 * 1024

var identifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

type Definition struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Type        string `json:"type" yaml:"type"`
	HTML        string `json:"html" yaml:"html"`
	Published   bool   `json:"published" yaml:"published"`
}

func (v Definition) Validate() error {
	if !identifier.MatchString(v.ID) || strings.TrimSpace(v.Name) == "" || len(v.Name) > 200 || len(v.Description) > 2000 {
		return fmt.Errorf("视图需要合法标识、名称和最多 2000 字节的描述")
	}
	if v.Type != "view" {
		return fmt.Errorf("视图 type 必须为 view")
	}
	if strings.TrimSpace(v.HTML) == "" || len(v.HTML) > MaxHTMLBytes {
		return fmt.Errorf("视图 HTML 不能为空且最多 256 KiB")
	}
	return nil
}

func URI(database, id string) string { return "ui://datascope/" + database + "/views/" + id }

//go:embed templates/*
var templates embed.FS

func Templates() []Definition {
	items := []Definition{
		{ID: "query", Name: "通用查询视图", Description: "使用绑定参数执行只读 SQL，展示结果表格并重新查询。"},
		{ID: "action", Name: "动作预览与结果", Description: "根据动作参数生成表单，展示真实预览、本人确认链接和请求回执。"},
		{ID: "securities_account", Name: "证券账户详情", Description: "按资金账号查看客户、资金、持仓、委托和流水，发起客户资料、账户及委托动作。"},
	}
	for i := range items {
		body, err := templates.ReadFile("templates/" + items[i].ID + ".html")
		if err != nil {
			panic(err)
		}
		css, _ := templates.ReadFile("templates/shared.css")
		runtime, _ := templates.ReadFile("templates/runtime.js")
		items[i].HTML = strings.Replace(string(body), "<!--shared-->", "<style>"+string(css)+"</style><script>"+string(runtime)+"</script>", 1)
		items[i].Type, items[i].Published = "view", true
	}
	return items
}
