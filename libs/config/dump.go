package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Redacted 输出脱敏后的配置内容（--print-config 用，spec-0.7 D4）：
// secret 字段值恒显示 ***，任何输出通道不见明文（§3 契约）。
func Redacted[T any](component string, cfg T) string {
	v := reflect.ValueOf(cfg)
	t := v.Type()
	lines := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		spec, err := parseTag(component, f)
		if err != nil {
			continue
		}
		val := fmt.Sprintf("%v", v.Field(i).Interface())
		if spec.secret {
			val = "***"
		}
		lines = append(lines, spec.envName+"="+val)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// Keys 列出 T 声明的全部环境变量名（.env.example 一致性检查用，spec-0.7 T8）。
func Keys[T any](component string) []string {
	var zero T
	t := reflect.TypeOf(zero)
	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		spec, err := parseTag(component, t.Field(i))
		if err != nil {
			continue
		}
		keys = append(keys, spec.envName)
	}
	sort.Strings(keys)
	return keys
}
