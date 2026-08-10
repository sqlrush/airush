// Package config 是三 Go 组件共享的声明式配置加载层（spec-0.7 D1）。
//
// 约定（spec-0.7 §2.1）：env-only 12-factor；变量名 AIRUSH_<COMPONENT>_<KEY>，
// 标 common:"true" 的字段在组件级缺失时回退 AIRUSH_COMMON_<KEY>；
// 本地开发（AIRUSH_ENV != production）额外加载 .env，env 恒优先。
//
// 支持标签：env（键名，必填）、default、required、secret、oneof（逗号列表）、common。
// 校验失败聚合报告全部问题后返回（fail-fast 但一次报全）；secret 字段的任何
// 错误信息不含原始值。
package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// envMode 决定是否加载 .env（spec-0.7 §3：生产不加载）。
const envMode = "AIRUSH_ENV"

// FieldError 描述单个配置项的问题；Env 为完整变量名。
type FieldError struct {
	Env    string
	Reason string
}

// LoadError 聚合全部配置问题。
type LoadError struct {
	Fields []FieldError
}

func (e *LoadError) Error() string {
	lines := make([]string, 0, len(e.Fields)+1)
	lines = append(lines, fmt.Sprintf("配置校验失败（%d 项）:", len(e.Fields)))
	for _, f := range e.Fields {
		lines = append(lines, fmt.Sprintf("  %s: %s", f.Env, f.Reason))
	}
	return strings.Join(lines, "\n")
}

// Load 按组件前缀加载并校验 T；T 必须是纯 struct。
func Load[T any](component string) (T, error) {
	var cfg T
	if os.Getenv(envMode) != "production" {
		_ = godotenv.Load() // 文件缺失是常态，忽略
	}

	var errs []FieldError
	v := reflect.ValueOf(&cfg).Elem()
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		spec, err := parseTag(component, f)
		if err != nil {
			errs = append(errs, FieldError{Env: spec.envName, Reason: err.Error()})
			continue
		}
		raw, found := lookup(spec)
		if !found {
			if spec.required {
				errs = append(errs, FieldError{Env: spec.envName, Reason: "必填项未设置"})
			} else if spec.hasDefault {
				raw = spec.defaultVal
				found = true
			}
		}
		if !found {
			continue
		}
		if ferr := setField(v.Field(i), spec, raw); ferr != nil {
			errs = append(errs, *ferr)
		}
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Env < errs[j].Env })
		return cfg, &LoadError{Fields: errs}
	}
	return cfg, nil
}

type fieldSpec struct {
	envName    string // AIRUSH_<COMP>_<KEY>
	commonName string // AIRUSH_COMMON_<KEY>（common 字段才有）
	required   bool
	secret     bool
	hasDefault bool
	defaultVal string
	oneof      []string
}

func parseTag(component string, f reflect.StructField) (fieldSpec, error) {
	key := f.Tag.Get("env")
	spec := fieldSpec{envName: envName(component, key)}
	if key == "" {
		spec.envName = fmt.Sprintf("(field %s)", f.Name)
		return spec, fmt.Errorf("struct 字段缺少 env 标签")
	}
	spec.required = f.Tag.Get("required") == "true"
	spec.secret = f.Tag.Get("secret") == "true"
	if d, ok := f.Tag.Lookup("default"); ok {
		spec.hasDefault, spec.defaultVal = true, d
	}
	if o := f.Tag.Get("oneof"); o != "" {
		spec.oneof = strings.Split(o, ",")
	}
	if f.Tag.Get("common") == "true" {
		spec.commonName = envName("COMMON", key)
	}
	return spec, nil
}

func envName(component, key string) string {
	return "AIRUSH_" + strings.ToUpper(component) + "_" + key
}

func lookup(spec fieldSpec) (string, bool) {
	if v, ok := os.LookupEnv(spec.envName); ok && v != "" {
		return v, true
	}
	if spec.commonName != "" {
		if v, ok := os.LookupEnv(spec.commonName); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// setField 解析赋值；secret 字段的错误不回显原始值（spec-0.7 §3）。
func setField(fv reflect.Value, spec fieldSpec, raw string) *FieldError {
	fail := func(reason string) *FieldError {
		return &FieldError{Env: spec.envName, Reason: reason}
	}
	if len(spec.oneof) > 0 && !contains(spec.oneof, raw) {
		if spec.secret {
			return fail(fmt.Sprintf("取值不在允许集合 %v（值已隐藏）", spec.oneof))
		}
		return fail(fmt.Sprintf("取值 %q 不在允许集合 %v", raw, spec.oneof))
	}
	switch fv.Interface().(type) {
	case string:
		fv.SetString(raw)
	case int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fail(parseReason(spec, raw, "整数"))
		}
		fv.SetInt(int64(n))
	case bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fail(parseReason(spec, raw, "布尔"))
		}
		fv.SetBool(b)
	case time.Duration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fail(parseReason(spec, raw, "时长（如 30s/5m）"))
		}
		fv.SetInt(int64(d))
	case float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fail(parseReason(spec, raw, "浮点数"))
		}
		fv.SetFloat(f)
	default:
		return fail(fmt.Sprintf("不支持的字段类型 %s", fv.Type()))
	}
	return nil
}

func parseReason(spec fieldSpec, raw, kind string) string {
	if spec.secret {
		return fmt.Sprintf("无法解析为%s（值已隐藏）", kind)
	}
	return fmt.Sprintf("无法解析 %q 为%s", raw, kind)
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
