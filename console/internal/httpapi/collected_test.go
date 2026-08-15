package httpapi

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// 采集查询面的护栏是系统边界上的输入校验：窗口无上限会让一次请求扫穿整个保留期，
// 步长过小会让一次响应回上万个点。两者都必须**拒绝**而不是静默截断（规则 6），
// 否则调用方拿到的是"看起来正常的半份数据"。

func wantValidationFailed(t *testing.T, err error, field string) {
	t.Helper()
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeValidationFailed {
		t.Fatalf("err = %v, want AR_VALIDATION_FAILED", err)
	}
	if len(ae.Details) == 0 || ae.Details[0].Field != field {
		t.Fatalf("details = %+v, want field %q", ae.Details, field)
	}
}

func TestParseWindow(t *testing.T) {
	t.Run("缺省为最近一小时", func(t *testing.T) {
		from, to, err := parseWindow("", "")
		if err != nil {
			t.Fatalf("parseWindow: %v", err)
		}
		if d := to.Sub(from); d < 59*time.Minute || d > 61*time.Minute {
			t.Fatalf("缺省窗口 = %v, want ≈1h", d)
		}
	})

	t.Run("非RFC3339被拒", func(t *testing.T) {
		_, _, err := parseWindow("2026-08-15", "")
		wantValidationFailed(t, err, "from")
		_, _, err = parseWindow("", "昨天")
		wantValidationFailed(t, err, "to")
	})

	t.Run("from不早于to被拒", func(t *testing.T) {
		now := time.Now().UTC().Format(time.RFC3339)
		_, _, err := parseWindow(now, now)
		wantValidationFailed(t, err, "from")
	})

	t.Run("超过最粗一层保留期被拒", func(t *testing.T) {
		to := time.Now().UTC()
		from := to.Add(-maxQueryWindow - time.Hour)
		_, _, err := parseWindow(from.Format(time.RFC3339), to.Format(time.RFC3339))
		wantValidationFailed(t, err, "from")
	})
}

func TestParseStep(t *testing.T) {
	to := time.Now().UTC()
	from := to.Add(-time.Hour)

	t.Run("缺省按窗口切200个点", func(t *testing.T) {
		step, err := parseStep("", from, to)
		if err != nil {
			t.Fatalf("parseStep: %v", err)
		}
		if step != 18*time.Second { // 1h / 200
			t.Fatalf("step = %v, want 18s", step)
		}
	})

	t.Run("极窄窗口不会退化成零步长", func(t *testing.T) {
		step, err := parseStep("", to.Add(-time.Second), to)
		if err != nil {
			t.Fatalf("parseStep: %v", err)
		}
		if step != minQueryStep {
			t.Fatalf("step = %v, want 下限 %v（零步长会让 time_bucket 报错）", step, minQueryStep)
		}
	})

	t.Run("非法与越界被拒", func(t *testing.T) {
		_, err := parseStep("5 分钟", from, to)
		wantValidationFailed(t, err, "step")
		_, err = parseStep("100ms", from, to)
		wantValidationFailed(t, err, "step")
		// 400 天窗口 × 1s 步长 ≫ 5000 点
		_, err = parseStep("1s", to.Add(-maxQueryWindow), to)
		wantValidationFailed(t, err, "step")
	})
}

func TestParseLimit(t *testing.T) {
	if n, err := parseLimit("", defaultTopN, maxTopN); err != nil || n != defaultTopN {
		t.Fatalf("缺省 limit = %d, %v", n, err)
	}
	if n, err := parseLimit("25", defaultTopN, maxTopN); err != nil || n != 25 {
		t.Fatalf("limit=25 → %d, %v", n, err)
	}
	for _, bad := range []string{"0", "-3", "abc", "201"} {
		if _, err := parseLimit(bad, defaultTopN, maxTopN); err == nil {
			t.Fatalf("limit=%q 未被拒绝", bad)
		}
	}
}

// TestSnapshotTarget：慢查询走 series 面而不进快照表，故 kind=slowlog 必须**拒绝**，
// 不能返回空——返回空会让调用方以为"还没采到"，实际是问错了地方。
func TestSnapshotTarget(t *testing.T) {
	const dsID = "aaaaaaaa-0000-0000-0000-00000000000a"

	for _, kind := range []string{metrics.SnapshotKindSchema, metrics.SnapshotKindConfig} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", dsID)
		r.SetPathValue("kind", kind)
		gotDS, gotKind, err := snapshotTarget(r)
		if err != nil || gotDS != dsID || gotKind != kind {
			t.Fatalf("kind=%s → %q %q %v", kind, gotDS, gotKind, err)
		}
	}

	for _, kind := range []string{metrics.SnapshotKindSlowlog, "bogus", ""} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("id", dsID)
		r.SetPathValue("kind", kind)
		_, _, err := snapshotTarget(r)
		var ae *apierror.Error
		if !errors.As(err, &ae) || ae.Code != apierror.CodeCollectUnsupportedKind {
			t.Fatalf("kind=%q → err %v, want AR_COLLECT_UNSUPPORTED_KIND", kind, err)
		}
	}
}
